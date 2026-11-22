package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

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
	name  string
	own   pairbook.Binding
	since time.Time
}

// pairbookEnroll joins the organization space link names.
func pairbookEnroll(ctx context.Context, s *pairbookSession, a pairbookArgs, link string) (any, error) {
	name, err := joinName(a.name)
	if err != nil {
		return nil, err
	}
	me, err := s.person()
	if err != nil {
		return nil, err
	}
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnroll, Link: link, Name: name})
	if err != nil {
		return nil, err
	}
	if res.Enroll == nil {
		return nil, errors.New("the broker didn't start an enrollment")
	}
	en := &enrolling{s: s, me: me, e: *res.Enroll, name: name, since: time.Now()}
	if en.own, err = s.binding(ctx, me, time.Now().Truncate(time.Millisecond)); err != nil {
		return nil, err
	}
	en.announce()
	return en.run(ctx)
}

// announce tells the person where to confirm, and which machine.
func (en *enrolling) announce() {
	w := en.s.progressOut()
	fmt.Fprintf(w, "To join, open %s\n", en.e.URL)
	fmt.Fprintf(w, "sign in with your company account, and check this machine's fingerprint: %s\n", fingerprintOf(pairbook.KeyID(en.own.WorkspaceKey)))
	fmt.Fprintf(w, "Code: %s (valid until %s)\n", en.e.Code, en.e.ExpiresAt.Local().Format("15:04"))
	openBrowser(en.e.URL)
}

// run sends the join and follows the enrollment to its end.
func (en *enrolling) run(ctx context.Context) (any, error) {
	st, err := en.send(ctx, en.e.Base)
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

// settled is the command's answer once the enrollment has one.
func (en *enrolling) settled(st protocol.SpaceEnrollState) (any, bool, error) {
	switch st.State {
	case space.EnrollAdmitted:
		return done("Joined " + en.e.Space + " as " + en.name + ". Members connect within minutes: pairtask space status shows it."), true, nil
	case space.EnrollDenied:
		return nil, true, errors.New("an admin declined this enrollment")
	case space.EnrollExpired:
		return nil, true, errors.New("the enrollment expired before it was confirmed; run pairtask space join again")
	case space.EnrollAwaitingApproval:
		if time.Since(en.since) > enrollWait {
			return done("Confirmed; an admin hasn't approved yet. You join " + en.e.Space + " once they do; pairtask space status shows it."), true, nil
		}
	}
	return nil, false, nil
}

// next waits a little, then re-signs a stale join or checks again.
func (en *enrolling) next(ctx context.Context, st protocol.SpaceEnrollState) (protocol.SpaceEnrollState, error) {
	select {
	case <-ctx.Done():
		return st, ctx.Err()
	case <-time.After(enrollPoll):
	}
	if st.State == space.EnrollStale && st.Base != nil {
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
