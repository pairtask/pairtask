package desktop

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/enroll"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
)

// Joining an organization's Pairbook space from Desktop. In a space whose
// people are accounts, the person is the company account the browser signs
// in with: every machine of that account (Desktop, a Box) joins under that
// one person, each with its own machine key, and the broker proves this
// machine's key itself. Only a space still on person keys asks for a
// person: then Desktop uses one made in its data directory on first need,
// with no recovery code (none ever passes through a conversation), kept
// across restarts. The join is confirmed by the person in a browser; a
// follower here finishes it, and picks it up again after a restart.

const (
	desktopEnrollPoll = 5 * time.Second
	desktopEnrollWait = 7 * 24 * time.Hour // as long as an admin has to decide
)

// PairbookJoinArgs are pairbook_join's arguments.
type PairbookJoinArgs struct {
	Link string `json:"link,omitempty" jsonschema:"The space's join link from the company's Pairbook (https://HOST/join#space=...&owner=...); ask the admin for it"`
}

// PairbookJoinResult is where the person confirms this machine.
type PairbookJoinResult struct {
	Space       string    `json:"space,omitempty"`
	URL         string    `json:"url,omitempty"`
	Code        string    `json:"code,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
	State       string    `json:"state,omitempty"`
	Guidance    string    `json:"guidance"`
}

// PairbookStatusResult is each enrollment this node follows.
type PairbookStatusResult struct {
	Enrollments []PairbookJoinResult `json:"enrollments"`
}

const (
	pairbookJoinDescription = "Join a space in the company's Pairbook with its join link (the admin's space page shows it). " +
		"Answers at once with a URL, a code and this machine's fingerprint: show them to the person, who opens the URL, signs in with the company account " +
		"and confirms the fingerprint. Joining then finishes by itself; pairbook_join_status shows where it stands. " +
		"The person is the company account they sign in with: this machine joins under it with its own machine key, alongside their other machines."
	pairbookStatusDescription = "Where this machine's joins to company Pairbook spaces stand: waiting for sign-in, waiting for an admin, joined, declined or expired."
)

// enrollments follows this node's enrollments: at most one follower per
// space in this process, and, by a lock file, across Desktop processes.
type enrollments struct {
	mu     sync.Mutex
	person *spaceowner.Me
	follow map[string]*PairbookJoinResult // by space: the last state seen
	active map[string]bool                // by space: a follower runs here
	// life is what followers and retries run under; Close ends it and
	// waits for them, so none outlives the node or holds its lock file.
	life    context.Context
	stop    context.CancelFunc
	running sync.WaitGroup
}

var (
	errNodeClosed        = errors.New("this Desktop node is closing")
	errFollowedElsewhere = errors.New("another Claude Desktop window follows this enrollment")
)

// stopJoins ends every follower and retry and waits for them.
func (n *Node) stopJoins() {
	n.joins.mu.Lock()
	if n.joins.stop != nil {
		n.joins.stop()
	}
	n.joins.mu.Unlock()
	n.joins.running.Wait()
}

// spawn runs fn as a follower of this node's lifetime, unless it ended.
func (n *Node) spawn(fn func()) bool {
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.life == nil || n.joins.life.Err() != nil {
		return false
	}
	n.joins.running.Add(1)
	go func() {
		defer n.joins.running.Done()
		fn()
	}()
	return true
}

const resumeRetry = 30 * time.Second

// origin is a Pairbook's address, https://HOST[:PORT]; "" for none.
type origin string

// joinLink is a space's join link, https://HOST/join#space=...&owner=...
type joinLink string

// enrollState is where an enrollment stands, as pairbook_join_status says.
type enrollState string

// pairbookOrigin is the Pairbook setting as an origin, or "" when unset or
// not just an https address.
func pairbookOrigin(setting string) origin {
	u, err := url.Parse(setting)
	if err != nil || !bareHTTPS(u) {
		return ""
	}
	return origin("https://" + u.Host)
}

// bareHTTPS: u is https://HOST[:PORT] and nothing more but a trailing /.
func bareHTTPS(u *url.URL) bool {
	switch {
	case u.Scheme != "https", u.Host == "", u.User != nil:
		return false
	case u.Path != "" && u.Path != "/":
		return false
	}
	return u.RawQuery == "" && u.Fragment == ""
}

// pairbookJoin starts or picks up an enrollment and leaves a follower to
// finish it.
func (n *Node) pairbookJoin(ctx context.Context, args PairbookJoinArgs) (PairbookJoinResult, error) {
	set := pairbookOrigin(n.pairbookSetting())
	if args.Link == "" {
		return joinGuidance(set), nil
	}
	if n.joins.life == nil || n.joins.life.Err() != nil {
		return PairbookJoinResult{}, errNodeClosed
	}
	l, err := space.ParseOrgLink(args.Link)
	if err != nil {
		return PairbookJoinResult{}, errors.New("that isn't a Pairbook join link; it looks like https://HOST/join#space=...&owner=..., from the admin's space page")
	}
	if set != "" && origin(l.Base) != set {
		return PairbookJoinResult{}, errors.New("that link is for " + l.Base + ", not the Pairbook set for this extension (" + string(set) + ")")
	}
	return n.startJoin(ctx, joinLink(args.Link), l.Space)
}

func (n *Node) startJoin(ctx context.Context, link joinLink, spaceID string) (PairbookJoinResult, error) {
	if cur, ok := n.following(spaceID); ok {
		return cur, nil // one enrollment per space: this is where it stands
	}
	lock, err := takeFollow(n.lockPath(spaceID))
	if err != nil {
		n.stopFollowing(spaceID, nil) // no lock taken: nothing follows it here
		if errors.Is(err, errFollowedElsewhere) {
			return PairbookJoinResult{Space: spaceID, State: "followed by another Claude Desktop window", Guidance: "Ask in that window, or call pairbook_join_status there."}, nil
		}
		return PairbookJoinResult{}, err
	}
	out, err := n.beginJoin(ctx, link, lock)
	if err != nil {
		n.stopFollowing(spaceID, lock)
	}
	return out, err
}

// beginJoin starts or picks up the enrollment, keeps it, and leaves a
// follower holding lock to finish it.
func (n *Node) beginJoin(ctx context.Context, link joinLink, lock *followLock) (PairbookJoinResult, error) {
	f, err := n.flow(link)
	if err != nil {
		return PairbookJoinResult{}, err
	}
	st, err := f.Begin(ctx, n.joinName())
	if err != nil {
		return PairbookJoinResult{}, err
	}
	if err := f.Saves.Save(enroll.Saved{Link: f.Link, Name: f.Name, Enroll: f.Enroll}); err != nil {
		return PairbookJoinResult{}, err
	}
	out := n.track(f, enrollState(st.State))
	if !n.spawn(func() { n.finish(f, st, lock) }) {
		return out, errNodeClosed
	}
	return out, nil
}

// following is where space's enrollment stands, if a follower runs here;
// otherwise it marks one as starting.
func (n *Node) following(space string) (PairbookJoinResult, bool) {
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.active[space] {
		if r := n.joins.follow[space]; r != nil {
			return *r, true
		}
		return PairbookJoinResult{Space: space, State: "starting", Guidance: "Call pairbook_join_status in a moment."}, true
	}
	if n.joins.active == nil {
		n.joins.active = map[string]bool{}
	}
	n.joins.active[space] = true
	return PairbookJoinResult{}, false
}

func (n *Node) stopFollowing(space string, lock *followLock) {
	lock.release()
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	delete(n.joins.active, space)
}

func (n *Node) lockPath(space string) string {
	return filepath.Join(n.cfg.Dir, "enrollments", space+".lock")
}

func joinGuidance(o origin) PairbookJoinResult {
	if o == "" {
		return PairbookJoinResult{Guidance: "Ask the company's Pairbook admin for the space's join link, then call pairbook_join with it."}
	}
	return PairbookJoinResult{Guidance: "Open " + string(o) + "/join and sign in with the company account to see the spaces you can join, " +
		"or ask the admin for the space's join link; then call pairbook_join with that link."}
}

func (n *Node) joinName() string {
	if name := n.nameSetting(); name != "" {
		return name
	}
	return "Desktop"
}

// flow is an enrollment flow for link as this node's person.
func (n *Node) flow(link joinLink) (*enroll.Flow, error) {
	call := func(ctx context.Context, r protocol.SpaceRequest) (*protocol.SpaceResult, error) {
		return n.space(ctx, r)
	}
	f := &enroll.Flow{Call: call, Person: n.person, Link: string(link), Saves: enroll.Saves(filepath.Join(n.cfg.Dir, "enrollments")),
		Poll: desktopEnrollPoll, Wait: desktopEnrollWait}
	f.OnState = func(st protocol.SpaceEnrollState) { n.track(f, enrollState(st.State)) }
	return f, nil
}

// person is this node's person, made on first use: keys and an anchor
// without recovery in the data directory's person folder.
func (n *Node) person() (spaceowner.Me, error) {
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.person != nil {
		return *n.joins.person, nil
	}
	dir := filepath.Join(n.cfg.Dir, "person")
	keys, err := personkey.Load(dir)
	if errors.Is(err, personkey.ErrNoKeys) {
		keys, err = personkey.Create(dir)
	}
	if err != nil {
		return spaceowner.Me{}, err
	}
	if err := ensureAnchor(dir, keys); err != nil {
		return spaceowner.Me{}, err
	}
	me, err := spaceowner.Load(keys)
	if err == nil {
		n.joins.person = &me
	}
	return me, err
}

// ensureAnchor makes the person's anchor, without recovery, if it has none
// yet (also after a start that stopped between the keys and the anchor).
func ensureAnchor(dir string, keys *personkey.Keys) error {
	if _, err := keys.Anchor(); err == nil {
		return nil
	}
	_, _, err := personkey.CreateAnchor(dir, false, time.Now())
	if errors.Is(err, personkey.ErrAnchorExists) {
		return nil
	}
	return err
}

func enrollmentGuidance(state enrollState) string {
	switch state {
	case space.EnrollPendingSignIn:
		return "Open the enrollment URL, sign in with your company account, and check this machine's fingerprint."
	case space.EnrollStale:
		return "The space changed. This app is refreshing the enrollment."
	case space.EnrollAwaitingApproval:
		return "Waiting for admin approval. This app is following the enrollment."
	case enrollState(enroll.Waiting):
		return "Waiting for admin approval. Use the same join link to resume this enrollment."
	case space.EnrollAdmitted:
		return "This workspace has joined the space."
	case space.EnrollDenied:
		return "An admin declined this workspace's enrollment."
	case space.EnrollExpired:
		return "This enrollment expired. Use the join link to start again."
	default:
		return string(state)
	}
}

// track records where f stands and returns it.
func (n *Node) track(f *enroll.Flow, state enrollState) PairbookJoinResult {
	r := PairbookJoinResult{Space: f.Enroll.Space, URL: f.Enroll.URL, Code: f.Enroll.Code, Fingerprint: f.Fingerprint(), ExpiresAt: f.Enroll.ExpiresAt, State: string(state),
		Guidance: enrollmentGuidance(state)}
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.follow == nil {
		n.joins.follow = map[string]*PairbookJoinResult{}
	}
	n.joins.follow[r.Space] = &r
	return r
}

// finish follows f in the background until it ends, records how, and
// lets go of the space.
func (n *Node) finish(f *enroll.Flow, st protocol.SpaceEnrollState, lock *followLock) {
	defer n.stopFollowing(f.Enroll.Space, lock)
	out, err := f.Follow(n.joins.life, st)
	n.track(f, finalState(out, err))
}

func finalState(out enroll.Outcome, err error) enrollState {
	switch {
	case errors.Is(err, enroll.ErrDenied):
		return space.EnrollDenied
	case errors.Is(err, enroll.ErrExpired):
		return space.EnrollExpired
	case err != nil:
		return enrollState("interrupted: " + err.Error())
	}
	return enrollState(out)
}

// resumeJoins picks up the enrollments kept in the data directory, at start.
func (n *Node) resumeJoins() {
	kept, _ := filepath.Glob(filepath.Join(n.cfg.Dir, "enrollments", "*.json"))
	for _, path := range kept {
		saves := enroll.Saves(filepath.Dir(path))
		s, ok := saves.Load(spaceOfSave(path))
		if !ok {
			continue
		}
		n.spawn(func() { n.resumeJoin(s) })
	}
}

func spaceOfSave(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// resumeJoin follows a kept enrollment again, unless another follower has
// it, retrying while the broker or Pairbook can't be reached; meanwhile its
// status says it is retrying.
func (n *Node) resumeJoin(s enroll.Saved) {
	if _, ok := n.following(s.Enroll.Space); ok {
		return
	}
	lock, err := takeFollow(n.lockPath(s.Enroll.Space))
	if err != nil {
		n.stopFollowing(s.Enroll.Space, nil)
		if !errors.Is(err, errFollowedElsewhere) {
			n.trackSaved(s, enrollState("not followed: "+err.Error()))
		}
		return
	}
	n.resumeLocked(s, lock)
}

// resumeLocked retains the enrollment lock across retries until Close or a successful handoff.
func (n *Node) resumeLocked(s enroll.Saved, lock *followLock) {
	for {
		_, err := n.beginJoin(n.joins.life, joinLink(s.Link), lock)
		if err == nil {
			return // a follower holds the lock now
		}
		n.trackSaved(s, enrollState("retrying: "+err.Error()))
		select {
		case <-n.joins.life.Done():
			n.stopFollowing(s.Enroll.Space, lock)
			return
		case <-time.After(resumeRetry):
		}
	}
}

// trackSaved records a kept enrollment's state before it is followed.
func (n *Node) trackSaved(s enroll.Saved, state enrollState) {
	r := PairbookJoinResult{Space: s.Enroll.Space, URL: s.Enroll.URL, Code: s.Enroll.Code, ExpiresAt: s.Enroll.ExpiresAt, State: string(state), Guidance: enrollmentGuidance(state)}
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.follow == nil {
		n.joins.follow = map[string]*PairbookJoinResult{}
	}
	n.joins.follow[r.Space] = &r
}

// pairbookStatus is every enrollment this node follows.
func (n *Node) pairbookStatus() PairbookStatusResult {
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	out := PairbookStatusResult{Enrollments: []PairbookJoinResult{}}
	for _, r := range n.joins.follow {
		out.Enrollments = append(out.Enrollments, *r)
	}
	return out
}

// PairbookMembersArgs are pairbook_members' arguments.
type PairbookMembersArgs struct {
	Space string `json:"space,omitempty" jsonschema:"The company space's ID; may be left out when this machine is in exactly one space"`
}

// PairbookMembersResult is who this machine may see in a company space.
type PairbookMembersResult struct {
	People      []protocol.SpacePerson `json:"people"`
	StatusKnown bool                   `json:"status_known"`
}

const pairbookMembersDescription = "List the people in a company Pairbook space this machine joined: only itself and the members its access lets it see, " +
	"each machine online or not and the agents it lists there. status_known is false when the Pairbook keeps no status. " +
	"A listed agent is advertised, not reachable by that alone; members connect as contacts on their own once both are in."

// pairbookMembers is the space's people as this machine may see them.
func (n *Node) pairbookMembers(ctx context.Context, args PairbookMembersArgs) (PairbookMembersResult, error) {
	out, err := n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapPeople, Space: args.Space})
	if err != nil {
		return PairbookMembersResult{}, err
	}
	people := out.People
	if people == nil {
		people = []protocol.SpacePerson{}
	}
	return PairbookMembersResult{People: people, StatusKnown: out.StatusKnown}, nil
}
