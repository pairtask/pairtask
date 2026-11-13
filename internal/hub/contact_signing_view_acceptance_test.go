package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactSigningViewAcceptanceVersionAndHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		seen    bool
		want    string
	}{
		{"unknown", 0, false, ""},
		{"unknown with history", 0, true, ""},
		{"old never signed", int(messaging.PairingVersion), false, protocol.ContactSigningUnsupported},
		{"old after signing", int(messaging.PairingVersion), true, protocol.ContactSigningDowngraded},
		{"signed current", int(messaging.SealVersion), true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var view protocol.PairingContactView
			noticeView(&view, store.ContactNoticeState{PeerVersion: tc.version, SealsSeen: tc.seen})
			if view.Signing != tc.want {
				t.Fatalf("version %d seen %t: signing=%q, want %q", tc.version, tc.seen, view.Signing, tc.want)
			}
			b, err := json.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), `"signing"`) != (tc.want != "") {
				t.Fatalf("signing field is not omitted/present as expected: %s", b)
			}
		})
	}
}

type signingVersionStore struct {
	messaging.Store
	moved bool
}

func (s signingVersionStore) VersionMoved() bool { return s.moved }

func TestContactSigningViewAcceptanceSessionVersionRefreshesOperator(t *testing.T) {
	h := newTestHub(t)
	h.SessionVersionSeen(signingVersionStore{moved: false})
	if h.pair.dirty.Load() || len(h.pair.changed) != 0 {
		t.Fatal("unchanged session version announced a pairing change")
	}
	h.SessionVersionSeen(signingVersionStore{moved: true})
	if !h.pair.dirty.Load() || len(h.pair.changed) != 1 {
		t.Fatal("newly observed version did not request an operator refresh")
	}
	h.SessionVersionSeen(signingVersionStore{moved: true})
	if len(h.pair.changed) != 1 {
		t.Fatal("repeated version changes should coalesce into one refresh")
	}
}
