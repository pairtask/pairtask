package hub

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
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

func messageSyncEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMessageSyncAcceptanceStatusIntervalsAndRecovery(t *testing.T) {
	h := newTestHub(t)
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	h.SetPairing(PairingConfig{Clock: func() time.Time { return now }})
	h.msgs.init()
	h.recordSync("contact", "", true)
	status := h.MessageSyncStatus("contact")
	messageSyncEqual(t, status.NextAt.Sub(now), 30*time.Second)
	messageSyncEqual(t, status.LastSuccess.Equal(now), true)
	h.recordSync("contact", "", false)
	messageSyncEqual(t, h.MessageSyncStatus("contact").NextAt.Sub(now), 5*time.Minute)
	now = now.Add(time.Minute)
	h.recordSync("contact", SyncUnreachable, false)
	status = h.MessageSyncStatus("contact")
	messageSyncEqual(t, status.Failures, 1)
	messageSyncEqual(t, status.LastFailure, SyncUnreachable)
	delay := status.NextAt.Sub(now)
	messageSyncEqual(t, delay >= 48*time.Second, true)
	messageSyncEqual(t, delay <= 72*time.Second, true)
	now = now.Add(time.Minute)
	h.recordSync("contact", "", true)
	status = h.MessageSyncStatus("contact")
	messageSyncEqual(t, status.Failures, 0)
	messageSyncEqual(t, status.LastFailure, "")
	messageSyncEqual(t, status.LastSuccess.Equal(now), true)
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
		{messaging.StoreError{Err: errors.New("database unavailable")}, SyncStore},
		{messaging.StoreError{Err: context.DeadlineExceeded}, SyncStore},
		{errors.New("unclassified failure"), SyncOther},
		{io.EOF, SyncClosed},
		{io.ErrUnexpectedEOF, SyncClosed},
		{syscall.ECONNRESET, SyncClosed},
		{syscall.EPIPE, SyncClosed},
		{net.ErrClosed, SyncClosed},
		{&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, SyncTimeout},
		{errUnreachable{context.DeadlineExceeded}, SyncTimeout},
		{peer.ErrPairingProtocol, SyncProtocol},
		{&net.OpError{Op: "remote error", Err: errors.New("tls: no application protocol")}, SyncUnsupported},
	} {
		if got := classifySync(tc.err); got != tc.want {
			t.Fatalf("%v classified %s want%s", tc.err, got, tc.want)
		}
	}
}
