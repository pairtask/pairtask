//go:build linux

package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func (f *controlServerFixture) view(t *testing.T) map[string]any {
	t.Helper()
	snapshot := settleSnapshot(t, pairingCommand(f.f.operatorFixture, "status", ""))
	for _, c := range snapshot.Contacts {
		if c.IdentityKey == f.id.Fingerprint() {
			wire, err := json.Marshal(c)
			operatorNoError(t, err)
			var view map[string]any
			operatorNoError(t, json.Unmarshal(wire, &view))
			return view
		}
	}
	t.Fatal("contact missing from operator snapshot")
	return nil
}
func TestControlServerViewAcceptancePendingAndOlderPeerAreHonest(t *testing.T) {
	f := newControlServerFixture(t)
	pauseServerContact(t, f.f, f.id.Fingerprint(), true)
	operatorEqual(t, "no-address notice is pending", f.view(t)["notice"], any("pending"))
	operatorEqual(t, "not attempted without an address", f.f.count("contact_notices_out"), 1)
	// A known old version is unavailable; an unknown version is not labelled old.
	_, err := f.f.db.Exec(`UPDATE peer_contacts SET peer_version=8 WHERE identity_key=?`, f.id.Fingerprint())
	operatorNoError(t, err)
	operatorEqual(t, "known older version", f.view(t)["peer_state_available"], any("unavailable"))
	_, err = f.f.db.Exec(`UPDATE peer_contacts SET peer_version=NULL WHERE identity_key=?`, f.id.Fingerprint())
	operatorNoError(t, err)
	_, unavailable := f.view(t)["peer_state_available"]
	operatorEqual(t, "unknown version is unknown", unavailable, false)
}
func TestControlServerViewAcceptanceRemotePauseStateAndSuccessfulV9Clear(t *testing.T) {
	f := newControlServerFixture(t)
	c, err := f.f.st.PeerMessages().AdmitControl(context.Background(), f.id.Fingerprint())
	operatorNoError(t, err)
	_, err = c.AcceptNotice(context.Background(), messaging.ContactState{Gen: 5, State: 1})
	operatorNoError(t, err)
	operatorEqual(t, "remote paused view", f.view(t)["peer_state"], any("paused"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := f.message(t, ctx)
	bound, err := f.remote.PeerMessages().Admit(ctx, f.route.pin)
	operatorNoError(t, err)
	s := messaging.Session{Frames: conn.MessageFrames(ctx), Store: bound, Contact: f.route.pin, Now: time.Now}
	_, version, err := s.OpenDial(ctx)
	operatorNoError(t, err)
	operatorEqual(t, "v9 session", version, byte(9))
	_, err = s.OfferLive(false)
	operatorNoError(t, err)
	var state string
	var gen int64
	operatorNoError(t, f.f.db.QueryRow(`SELECT peer_state,peer_state_gen FROM peer_contacts WHERE identity_key=?`, f.id.Fingerprint()).Scan(&state, &gen))
	operatorEqual(t, "successful session cleared display", state, "")
	operatorEqual(t, "watermark retained", gen, int64(5))
}

func TestControlServerViewAcceptanceFailedMessageSessionDoesNotClearPause(t *testing.T) {
	f := newControlServerFixture(t)
	c, err := f.f.st.PeerMessages().AdmitControl(context.Background(), f.id.Fingerprint())
	operatorNoError(t, err)
	_, err = c.AcceptNotice(context.Background(), messaging.ContactState{Gen: 5, State: 1})
	operatorNoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frames := f.message(t, ctx).MessageFrames(ctx)
	b, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: 9})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(b))
	_, err = frames.Recv()
	operatorNoError(t, err)
	b, err = messaging.EncodePairingEpoch(messaging.PairingEpoch{})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(b))
	_, err = frames.Recv()
	operatorNoError(t, err)
	b, err = messaging.EncodeStored(messaging.Stored{ID: "out-of-order"})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(b))
	_, err = frames.Recv()
	if err == nil {
		t.Fatal("malformed message phase did not close")
	}
	var state string
	operatorNoError(t, f.f.db.QueryRow(`SELECT peer_state FROM peer_contacts WHERE identity_key=?`, f.id.Fingerprint()).Scan(&state))
	operatorEqual(t, "failed session kept remote pause", state, "paused")
}
