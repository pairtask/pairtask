//go:build linux

package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

type enrollmentDirectory struct {
	flowClient
	link    space.OrgLink
	start   space.Enrollment
	newer   pairbook.Mark
	sent    []pairbook.SignedChange
	started int
	states  []space.EnrollState
}

func (d *enrollmentDirectory) StartEnroll(_ context.Context, pin space.Pin, name string) (space.Enrollment, error) {
	d.started++
	got := space.OrgLink{Space: pin.Space, Owner: pin.Owner, Base: pin.Base}
	if got != d.link || name != "Ada" {
		return space.Enrollment{}, errors.New("incorrect enrollment pin or name")
	}
	return d.start, nil
}

func (d *enrollmentDirectory) SubmitEnroll(_ context.Context, pin space.Pin, id string, ch pairbook.SignedChange) (space.EnrollState, error) {
	if id != d.start.ID || pin.Space != ch.Space {
		return space.EnrollState{}, errors.New("wrong enrollment target")
	}
	if ch.Name != "Ada" || ch.Action != pairbook.ChangeJoin {
		return space.EnrollState{}, errors.New("incorrect join")
	}
	if ch.TokenHash != sha256.Sum256(d.start.Challenge) {
		return space.EnrollState{}, errors.New("incorrect enrollment change")
	}
	if !ed25519.Verify(ch.Person, ch.SigningBytes(), ch.Sig) {
		return space.EnrollState{}, errors.New("incorrect person signature")
	}
	d.sent = append(d.sent, ch)
	return space.EnrollState{State: space.EnrollPendingSignIn}, nil
}

func (d *enrollmentDirectory) EnrollState(_ context.Context, pin space.Pin, id string) (space.EnrollState, error) {
	if id != d.start.ID || pin.Space != d.link.Space {
		return space.EnrollState{}, errors.New("incorrect enrollment poll")
	}
	if len(d.states) == 0 {
		return space.EnrollState{}, errors.New("unexpected extra poll")
	}
	state := d.states[0]
	d.states = d.states[1:]
	return state, nil
}

func newEnrollmentFlow(t *testing.T) (*enrollmentDirectory, *flowNode) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	poll := enrollPoll
	enrollPoll = time.Millisecond
	t.Cleanup(func() { enrollPoll = poll })
	d := &enrollmentDirectory{flowClient: flowClient{d: &flowDirectory{}}, link: space.OrgLink{Base: "https://directory.example", Space: strings.Repeat("a", 32), Owner: "sha256:" + strings.Repeat("b", 64)},
		start: space.Enrollment{ID: strings.Repeat("c", 32), Challenge: []byte("enrollment challenge"), Base: pairbook.Mark{Version: 1}, Code: "ABCD-EFGH", URL: "https://directory.example/e/ABCD-EFGH", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, newer: pairbook.Mark{Version: 2}}
	node := startConfiguredFlowNode(t, hub.SpaceConfig{Maps: func(_ string, _ crypto.Signer) (space.MapDirectory, error) { return d, nil }})
	return d, node
}

func expectEnrollValue[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

func (d *enrollmentDirectory) verifyRetry(t *testing.T, node *flowNode) {
	t.Helper()
	expectEnrollValue(t, "signed changes", len(d.sent), 2)
	first, next := d.sent[0], d.sent[1]
	expectEnrollValue(t, "first base", first.Base, d.start.Base)
	expectEnrollValue(t, "new base", next.Base, d.newer)
	expectEnrollValue(t, "person lineage", next.Lineage, first.Lineage)
	expectEnrollValue(t, "initial workspace", first.Binding.Workspace(), node.id.Fingerprint())
	expectEnrollValue(t, "resumed workspace", next.Binding.Workspace(), node.id.Fingerprint())
}

func TestOrgJoinCLIResignsStaleEnrollmentThroughBroker(t *testing.T) {
	d, node := newEnrollmentFlow(t)
	d.states = []space.EnrollState{{State: space.EnrollStale, Base: &d.newer}, {State: space.EnrollAdmitted}}
	result := node.command(t, "join", d.link.String(), "--name", "Ada")
	if !strings.Contains(result, "Joined") {
		t.Fatal(result)
	}
	d.verifyRetry(t, node)
}

func TestOrgJoinCLIResumesAfterLateApproval(t *testing.T) {
	for _, state := range []string{space.EnrollAdmitted, space.EnrollStale} {
		t.Run(state, func(t *testing.T) {
			d, node := newEnrollmentFlow(t)
			d.states = []space.EnrollState{{State: space.EnrollAwaitingApproval}}
			wait := enrollWait
			t.Cleanup(func() { enrollWait = wait })
			enrollWait = 0
			args := []string{"join", d.link.String(), "--name", "Ada"}
			result := node.command(t, args...)
			if !strings.Contains(result, "same pairtask space join") {
				t.Fatal(result)
			}
			folder := enrollSaves(filepath.Join(node.keyDir, "enrollments"))
			saved, ok := folder.load(d.link.Space)
			expectEnrollValue(t, "saved enrollment exists", ok, true)
			expectEnrollValue(t, "saved enrollment ID", saved.Enroll.Enrollment, d.start.ID)
			info, err := os.Stat(folder.path(d.link.Space))
			offlineOK(t, err)
			expectEnrollValue(t, "file permissions", info.Mode().Perm(), os.FileMode(0600))
			enrollWait = wait
			d.states = []space.EnrollState{{State: state, Base: &d.newer}, {State: space.EnrollAdmitted}}
			result = node.command(t, args...)
			if !strings.Contains(result, "Joined") {
				t.Fatal(result)
			}
			expectEnrollValue(t, "enrollments started", d.started, 1)
			if state == space.EnrollStale {
				d.verifyRetry(t, node)
			} else {
				expectEnrollValue(t, "signed changes", len(d.sent), 1)
			}
			_, err = os.Stat(folder.path(d.link.Space))
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("settled enrollment file: %v", err)
			}
		})
	}
}
