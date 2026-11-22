package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// Joining an organization space: enroll this workspace, show the person
// where to confirm it, and wait until the organization admits it.

// enrollPoll is how often the enrollment is checked; enrollWait is how long
// the command waits for an admin before leaving the rest to the broker.
var (
	enrollPoll = 2 * time.Second
	enrollWait = 30 * time.Minute
)

// enrolling is one enrollment in progress on this host.
type enrolling struct {
	s     *pairbookSession
	me    spaceowner.Me
	e     protocol.SpaceEnroll
	link  string
	name  string
	own   pairbook.Binding
	since time.Time
	saved enrollSaves
}

// pairbookEnroll joins the organization space link names: it picks up an
// enrollment this host left waiting for an admin, or starts one.
func pairbookEnroll(ctx context.Context, s *pairbookSession, a pairbookArgs, link string) (any, error) {
	l, err := space.ParseOrgLink(link)
	if err != nil {
		return nil, err
	}
	me, err := s.person()
	if err != nil {
		return nil, err
	}
	en := &enrolling{s: s, me: me, link: link, since: time.Now(), saved: s.enrollSaves()}
	if kept, ok := en.saved.load(l.Space); ok && kept.Link == link {
		return en.resume(ctx, kept)
	}
	return en.start(ctx, a.name)
}

// start begins a new enrollment.
func (en *enrolling) start(ctx context.Context, given string) (any, error) {
	name, err := joinName(given)
	if err != nil {
		return nil, err
	}
	res, err := en.s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnroll, Link: en.link, Name: name})
	if err != nil {
		return nil, err
	}
	if res.Enroll == nil {
		return nil, errors.New("the broker didn't start an enrollment")
	}
	en.e, en.name = *res.Enroll, name
	if err := en.bind(ctx); err != nil {
		return nil, err
	}
	en.announce()
	st, err := en.send(ctx, en.e.Base)
	return en.follow(ctx, st, err)
}

// resume follows an enrollment this host started before.
func (en *enrolling) resume(ctx context.Context, kept savedEnroll) (any, error) {
	en.e, en.name = kept.Enroll, kept.Name
	if err := en.bind(ctx); err != nil {
		return nil, err
	}
	fmt.Fprintf(en.s.progressOut(), "Picking up the enrollment started earlier (code %s).\n", en.e.Code)
	res, err := en.s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnrollState, Space: en.e.Space, Enrollment: en.e.Enrollment})
	st, err := enrollStateOf(res, err)
	if err != nil {
		en.saved.drop(en.e.Space) // unknown to the Pairbook now: start afresh next time
		return nil, err
	}
	return en.follow(ctx, st, nil)
}

func (en *enrolling) bind(ctx context.Context) error {
	var err error
	en.own, err = en.s.binding(ctx, en.me, time.Now().Truncate(time.Millisecond))
	return err
}

// announce tells the person where to confirm, and which machine.
func (en *enrolling) announce() {
	w := en.s.progressOut()
	fmt.Fprintf(w, "To join, open %s\n", en.e.URL)
	fmt.Fprintf(w, "sign in with your company account, and check this machine's fingerprint: %s\n", fingerprintOf(pairbook.KeyID(en.own.WorkspaceKey)))
	fmt.Fprintf(w, "Code: %s (valid until %s)\n", en.e.Code, en.e.ExpiresAt.Local().Format("15:04"))
	openBrowser(en.e.URL)
}

// follow follows the enrollment from st to its end.
func (en *enrolling) follow(ctx context.Context, st protocol.SpaceEnrollState, err error) (any, error) {
	for err == nil {
		var out any
		var finished bool
		if out, finished, err = en.settled(st); finished || err != nil {
			return out, err
		}
		st, err = en.next(ctx, st)
	}
	return nil, err
}

// settled is the command's answer once the enrollment has one. Waiting
// for an admin, the command stops after enrollWait and keeps the
// enrollment, so running the same join again picks it up: only this host
// can sign the join again if the space changes meanwhile.
func (en *enrolling) settled(st protocol.SpaceEnrollState) (any, bool, error) {
	switch st.State {
	case space.EnrollAdmitted:
		en.saved.drop(en.e.Space)
		return done("Joined " + en.e.Space + " as " + en.name + ". Members connect within minutes: pairtask space status shows it."), true, nil
	case space.EnrollDenied:
		en.saved.drop(en.e.Space)
		return nil, true, errors.New("an admin declined this enrollment")
	case space.EnrollExpired:
		en.saved.drop(en.e.Space)
		return nil, true, errors.New("the enrollment expired; run pairtask space join again")
	case space.EnrollAwaitingApproval, space.EnrollStale:
		return en.waiting()
	case space.EnrollPendingSignIn:
		return nil, false, en.confirmInTime()
	}
	return nil, true, fmt.Errorf("the Pairbook reported an unknown enrollment state %q", st.State)
}

// waiting keeps waiting for an admin, until enrollWait has passed.
func (en *enrolling) waiting() (any, bool, error) {
	if time.Since(en.since) <= enrollWait {
		return nil, false, nil
	}
	if err := en.saved.save(savedEnroll{Link: en.link, Name: en.name, Enroll: en.e}); err != nil {
		return nil, true, fmt.Errorf("an admin hasn't approved yet, and this enrollment couldn't be kept to resume: %w", err)
	}
	return done("Confirmed; an admin hasn't approved yet. After they do, run this same pairtask space join command again to finish."), true, nil
}

// confirmInTime: the person still has time to confirm in the browser.
func (en *enrolling) confirmInTime() error {
	if time.Now().After(en.e.ExpiresAt.Add(time.Minute)) {
		return errors.New("the enrollment wasn't confirmed in time; run pairtask space join again")
	}
	return nil
}

// next waits a little, then re-signs a stale join or checks again.
func (en *enrolling) next(ctx context.Context, st protocol.SpaceEnrollState) (protocol.SpaceEnrollState, error) {
	select {
	case <-ctx.Done():
		return st, ctx.Err()
	case <-time.After(enrollPoll):
	}
	if st.State == space.EnrollStale {
		if st.Base == nil {
			return st, errors.New("the Pairbook asked for the join again without saying on which map")
		}
		return en.send(ctx, *st.Base)
	}
	res, err := en.s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnrollState, Space: en.e.Space, Enrollment: en.e.Enrollment})
	return enrollStateOf(res, err)
}

// send signs the join on base and sends it: only the base and the
// signatures change between sends.
func (en *enrolling) send(ctx context.Context, base protocol.SpaceMark) (protocol.SpaceEnrollState, error) {
	mark, err := markOf(base)
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	ch := en.me.Change(spaceowner.Request{Space: en.e.Space, Action: pairbook.ChangeJoin, Base: mark, Name: en.name,
		TokenHash: sha256.Sum256(en.e.Challenge), Binding: en.own}, time.Now().Truncate(time.Millisecond))
	record, err := json.Marshal(ch)
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	res, err := en.s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnrollSend, Space: en.e.Space, Enrollment: en.e.Enrollment, Record: record})
	return enrollStateOf(res, err)
}

func enrollStateOf(res *protocol.SpaceResult, err error) (protocol.SpaceEnrollState, error) {
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	if res.EnrollState == nil {
		return protocol.SpaceEnrollState{}, errors.New("the broker didn't report the enrollment's state")
	}
	return *res.EnrollState, nil
}

// markOf reads a mark the broker reported.
func markOf(m protocol.SpaceMark) (pairbook.Mark, error) {
	out := pairbook.Mark{Version: m.Version}
	d, err := hex.DecodeString(m.Digest)
	if err != nil || len(d) != len(out.Digest) {
		return out, errors.New("the broker reported a malformed map mark")
	}
	copy(out.Digest[:], d)
	return out, nil
}

// fingerprintOf is a key ID as people compare it: its first 16 hex digits.
func fingerprintOf(keyID string) string {
	h := strings.TrimPrefix(keyID, "sha256:")
	if len(h) > 16 {
		h = h[:16]
	}
	return h
}

// openBrowser opens url on a desktop, best effort; the URL is printed
// either way.
func openBrowser(url string) {
	if !strings.HasPrefix(url, "https://") {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Start()
}

// savedEnroll is an enrollment kept to resume: the link, the name and what
// the Pairbook gave when it started.
type savedEnroll struct {
	Link   string               `json:"link"`
	Name   string               `json:"name"`
	Enroll protocol.SpaceEnroll `json:"enroll"`
}

// enrollSaves is the folder, beside the person's keys, that keeps
// enrollments waiting for an admin; "" keeps none.
type enrollSaves string

func (s *pairbookSession) enrollSaves() enrollSaves {
	if s.enrollDir != "" {
		return enrollSaves(s.enrollDir)
	}
	dir, err := personkey.ResolveDir()
	if err != nil {
		return ""
	}
	return enrollSaves(filepath.Join(dir, "enrollments"))
}

func (d enrollSaves) path(space string) string { return filepath.Join(string(d), space+".json") }

func (d enrollSaves) load(space string) (savedEnroll, bool) {
	var e savedEnroll
	if d == "" {
		return e, false
	}
	b, err := os.ReadFile(d.path(space))
	if err != nil {
		return e, false
	}
	if json.Unmarshal(b, &e) != nil {
		return e, false
	}
	return e, e.Enroll.Space == space
}

func (d enrollSaves) save(e savedEnroll) error {
	if d == "" {
		return errors.New("no place to keep it")
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(d.path(e.Enroll.Space), b, 0o600)
}

func (d enrollSaves) drop(space string) {
	if d != "" {
		_ = os.Remove(d.path(space))
	}
}
