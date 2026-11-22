//go:build linux

package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

type enrollmentDirectory struct {
	flowClient
	link   space.OrgLink
	start  space.Enrollment
	newer  pairbook.Mark
	sent   []pairbook.SignedChange
	polled bool
}

func (d *enrollmentDirectory) StartEnroll(_ context.Context, pin space.Pin, name string) (space.Enrollment, error) {
	if pin.Space != d.link.Space || pin.Owner != d.link.Owner || pin.Base != d.link.Base || name != "Ada" {
		return space.Enrollment{}, errors.New("incorrect enrollment pin or name")
	}
	return d.start, nil
}

func (d *enrollmentDirectory) SubmitEnroll(_ context.Context, pin space.Pin, id string, ch pairbook.SignedChange) (space.EnrollState, error) {
	if id != d.start.ID || ch.Space != pin.Space || ch.Name != "Ada" || ch.Action != pairbook.ChangeJoin || ch.TokenHash != sha256.Sum256(d.start.Challenge) {
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
	if !d.polled {
		d.polled = true
		return space.EnrollState{State: space.EnrollStale, Base: &d.newer}, nil
	}
	return space.EnrollState{State: space.EnrollAdmitted}, nil
}

func TestOrgJoinCLIResignsStaleEnrollmentThroughBroker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	poll := enrollPoll
	enrollPoll = time.Millisecond
	t.Cleanup(func() { enrollPoll = poll })
	d := &enrollmentDirectory{flowClient: flowClient{d: &flowDirectory{}}, link: space.OrgLink{Base: "https://directory.example", Space: strings.Repeat("a", 32), Owner: "sha256:" + strings.Repeat("b", 64)},
		start: space.Enrollment{ID: strings.Repeat("c", 32), Challenge: []byte("enrollment challenge"), Base: pairbook.Mark{Version: 1}, Code: "ABCD-EFGH", URL: "https://directory.example/e/ABCD-EFGH", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, newer: pairbook.Mark{Version: 2}}
	node := startConfiguredFlowNode(t, hub.SpaceConfig{Maps: func(_ string, _ crypto.Signer) (space.MapDirectory, error) { return d, nil }})
	result := node.command(t, "join", d.link.String(), "--name", "Ada")
	if !strings.Contains(result, "Joined") || len(d.sent) != 2 {
		t.Fatalf("join result %q, %d changes", result, len(d.sent))
	}
	first, next := d.sent[0], d.sent[1]
	if first.Base != d.start.Base || next.Base != d.newer || first.Lineage != next.Lineage || first.Binding.Workspace() != next.Binding.Workspace() || next.Binding.Workspace() != node.id.Fingerprint() {
		t.Fatal("retry failed to keep the person and workspace bound to the new base")
	}
}
