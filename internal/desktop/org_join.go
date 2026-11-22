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

// Joining an organization's Pairbook space from Desktop. Desktop is its own
// person here: a person key made in its data directory on the first join,
// with no recovery code (none ever passes through a conversation), kept
// across restarts. It is separate from the person on any Box until binding
// another machine is supported. The join is confirmed by the person in a
// browser; a follower here finishes it, and picks it up again after a
// restart.

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
		"Desktop joins as its own identity, separate from the person's other machines."
	pairbookStatusDescription = "Where this machine's joins to company Pairbook spaces stand: waiting for sign-in, waiting for an admin, joined, declined or expired."
)

// enrollments follows this node's enrollments.
type enrollments struct {
	mu     sync.Mutex
	person *spaceowner.Me
	follow map[string]*PairbookJoinResult // by space
}

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
	case u.Scheme != "https", u.Host == "":
		return false
	case u.Path != "" && u.Path != "/":
		return false
	}
	return u.RawQuery == "" && u.Fragment == ""
}

// pairbookJoin starts or picks up an enrollment and leaves a follower to
// finish it.
func (n *Node) pairbookJoin(ctx context.Context, args PairbookJoinArgs) (PairbookJoinResult, error) {
	set := pairbookOrigin(n.cfg.Pairbook)
	if args.Link == "" {
		return joinGuidance(set), nil
	}
	l, err := space.ParseOrgLink(args.Link)
	if err != nil {
		return PairbookJoinResult{}, errors.New("that isn't a Pairbook join link; it looks like https://HOST/join#space=...&owner=..., from the admin's space page")
	}
	if set != "" && origin(l.Base) != set {
		return PairbookJoinResult{}, errors.New("that link is for " + l.Base + ", not the Pairbook set for this extension (" + string(set) + ")")
	}
	f, err := n.flow(joinLink(args.Link))
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
	go n.finish(f, st)
	return out, nil
}

func joinGuidance(o origin) PairbookJoinResult {
	if o == "" {
		return PairbookJoinResult{Guidance: "Ask the company's Pairbook admin for the space's join link, then call pairbook_join with it."}
	}
	return PairbookJoinResult{Guidance: "Open " + string(o) + "/join and sign in with the company account to see the spaces you can join, " +
		"or ask the admin for the space's join link; then call pairbook_join with that link."}
}

func (n *Node) joinName() string {
	if n.cfg.Name != "" {
		return n.cfg.Name
	}
	return "Desktop"
}

// flow is an enrollment flow for link as this node's person.
func (n *Node) flow(link joinLink) (*enroll.Flow, error) {
	me, err := n.person()
	if err != nil {
		return nil, err
	}
	call := func(ctx context.Context, r protocol.SpaceRequest) (*protocol.SpaceResult, error) {
		return n.space(ctx, r)
	}
	return &enroll.Flow{Call: call, Me: me, Link: string(link), Saves: enroll.Saves(filepath.Join(n.cfg.Dir, "enrollments")),
		Poll: desktopEnrollPoll, Wait: desktopEnrollWait}, nil
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

// track records where f stands and returns it.
func (n *Node) track(f *enroll.Flow, state enrollState) PairbookJoinResult {
	r := PairbookJoinResult{Space: f.Enroll.Space, URL: f.Enroll.URL, Code: f.Enroll.Code, Fingerprint: f.Fingerprint(), ExpiresAt: f.Enroll.ExpiresAt, State: string(state),
		Guidance: "Show the person the URL and this machine's fingerprint. They open it, sign in with the company account and confirm the fingerprint matches."}
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	if n.joins.follow == nil {
		n.joins.follow = map[string]*PairbookJoinResult{}
	}
	n.joins.follow[r.Space] = &r
	return r
}

// finish follows f in the background until it ends, then records how.
func (n *Node) finish(f *enroll.Flow, st protocol.SpaceEnrollState) {
	out, err := f.Follow(n.base, st)
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
		go n.resumeJoin(joinLink(s.Link))
	}
}

func spaceOfSave(path string) string {
	base := filepath.Base(path)
	return base[:len(base)-len(filepath.Ext(base))]
}

func (n *Node) resumeJoin(link joinLink) {
	f, err := n.flow(link)
	if err != nil {
		return
	}
	st, err := f.Begin(n.base, n.joinName())
	if err != nil {
		n.cfg.Log.Info("pairbook enrollment not picked up", "err", err)
		return
	}
	n.track(f, enrollState(st.State))
	n.finish(f, st)
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
