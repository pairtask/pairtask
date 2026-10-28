package hub

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestMessageSyncAcceptanceDeterministicDialOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, own string
		contact   peer.Contact
		want      bool
	}{
		{"joiner", "b", peer.Contact{IdentityKey: "a", Destination: "peer:443", Joined: true}, true},
		{"inviter-no-address", "a", peer.Contact{IdentityKey: "b", Invited: true}, false},
		{"reciprocal-lower", "a", peer.Contact{IdentityKey: "b", Destination: "peer:443", Joined: true, Invited: true}, true},
		{"reciprocal-higher", "b", peer.Contact{IdentityKey: "a", Destination: "peer:443", Joined: true, Invited: true}, false},
		{"lower-without-address", "a", peer.Contact{IdentityKey: "b", Joined: true, Invited: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dials(tc.contact, tc.own); got != tc.want {
				t.Fatalf("dials=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMessageSyncAcceptanceInboundOutboundExclusionAndRelease(t *testing.T) {
	h := newTestHub(t)
	h.SetPairing(PairingConfig{Clock: time.Now})
	release, ok := h.beginMessages("contact")
	if !ok {
		t.Fatal("first session refused")
	}
	if _, ok := h.beginMessages("contact"); ok {
		t.Fatal("concurrent contact session admitted")
	}
	other, ok := h.beginMessages("other")
	if !ok {
		t.Fatal("another contact unnecessarily blocked")
	}
	other(false)
	release(true)
	again, ok := h.beginMessages("contact")
	if !ok {
		t.Fatal("completed contact never released")
	}
	again(false)
}

func TestMessageSyncAcceptanceStatusIntervalsAndRecovery(t *testing.T) {
	h := newTestHub(t)
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	h.SetPairing(PairingConfig{Clock: func() time.Time { return now }})
	h.msgs.init()
	h.recordSync("contact", "", true)
	status := h.MessageSyncStatus("contact")
	if status.NextAt.Sub(now) != 30*time.Second || !status.LastSuccess.Equal(now) {
		t.Fatalf("active status %+v", status)
	}
	h.recordSync("contact", "", false)
	if got := h.MessageSyncStatus("contact").NextAt.Sub(now); got != 5*time.Minute {
		t.Fatalf("idle interval %v", got)
	}
	now = now.Add(time.Minute)
	h.recordSync("contact", SyncUnreachable, false)
	status = h.MessageSyncStatus("contact")
	if status.Failures != 1 || status.LastFailure != SyncUnreachable {
		t.Fatalf("failure status %+v", status)
	}
	delay := status.NextAt.Sub(now)
	if delay < 48*time.Second || delay > 72*time.Second {
		t.Fatalf("unbounded first backoff %v", delay)
	}
	now = now.Add(time.Minute)
	h.recordSync("contact", "", true)
	status = h.MessageSyncStatus("contact")
	if status.Failures != 0 || status.LastFailure != "" || !status.LastSuccess.Equal(now) {
		t.Fatalf("success did not clear failure %+v", status)
	}
}

func TestMessageSyncAcceptanceFailureClassesStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{messaging.ErrUnsupported, SyncUnsupported},
		{peer.ErrPeerMismatch, SyncPinMismatch},
		{context.DeadlineExceeded, SyncTimeout},
		{errUnreachable{errors.New("connection refused")}, SyncUnreachable},
		{messaging.ErrProtocol, SyncProtocol},
		{errors.New("database unavailable"), SyncStore},
		{&net.OpError{Op: "remote error", Err: errors.New("tls: no application protocol")}, SyncUnsupported},
	} {
		if got := classifySync(tc.err); got != tc.want {
			t.Fatalf("%v classified %s want%s", tc.err, got, tc.want)
		}
	}
}
