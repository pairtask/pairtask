package desktop

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

func desktopConnectedSpace(t *testing.T) (*Node, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "desktop.db"))
	desktopSpaceCheck(t, err == nil, "%v", err)
	t.Cleanup(func() { st.Close() })
	f := &desktopSpaceDirectory{}
	h := hub.New(st, 0, slog.New(slog.DiscardHandler))
	h.SetPairing(hub.PairingConfig{Exchanger: pake.New()})
	h.SetSpaces(hub.SpaceConfig{Directory: func(string, crypto.Signer) (space.Directory, error) { return f, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done() })
	n := &Node{own: &owner{hub: h}}
	status := n.Call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
	desktopSpaceCheck(t, status.Type == protocol.TypeOperatorPairingResult, "identity load: %+v", status)
	ep, err := h.PairingEndpoint()
	desktopSpaceCheck(t, err == nil, "%v", err)
	seed := sha256.Sum256([]byte("desktop connect target"))
	key := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	target := pairbook.KeyID(key)
	pin := space.Pin{Space: "0123456789abcdef0123456789abcdef", Owner: "sha256:" + strings.Repeat("a", 64), Base: "https://directory.example", JoinedAt: time.Now()}
	desktopSpaceCheck(t, st.PinSpace(ctx, pin) == nil, "pin failed")
	f.members = []pairbook.Member{{Space: pin.Space, Node: ep.Identity.PublicKey(), Name: "Desktop"}, {Space: pin.Space, Node: key, Name: "Bob"}}
	desktopSeedCodeContact(t, st, target)
	return n, target
}

func TestDesktopSpaceConnectAcceptanceToolReturnsContact(t *testing.T) {
	n, target := desktopConnectedSpace(t)
	client := desktopSpaceMCP(t, n)
	tools, err := client.ListTools(context.Background(), nil)
	desktopSpaceCheck(t, err == nil, "%v", err)
	seen := false
	for _, tool := range tools.Tools {
		if tool.Name == "space_connect" {
			seen = true
		}
	}
	desktopSpaceCheck(t, seen, "space_connect not advertised")
	for _, args := range []map[string]any{{"member": target}, {"member": target, "space": "0123456789abcdef0123456789abcdef"}} {
		out := desktopSpaceTool(t, client, "space_connect", args)
		data, err := json.Marshal(out.StructuredContent)
		desktopSpaceCheck(t, err == nil, "%v", err)
		var result SpaceConnectResult
		desktopSpaceCheck(t, json.Unmarshal(data, &result) == nil, "invalid result: %s", data)
		desktopSpaceCheck(t, result.Contact.IdentityKey == target, "wrong contact: %+v", result)
		desktopSpaceCheck(t, result.Contact.Confirmed, "unconfirmed result: %+v", result)
		desktopSpaceCheck(t, result.Contact.PairingEpoch == 1, "contact epoch changed: %+v", result)
	}
}

func desktopSeedCodeContact(t *testing.T, st *store.Store, target string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	mark, err := st.PairingMark(ctx)
	desktopSpaceCheck(t, err == nil, "%v", err)
	outcome := peer.PairingOutcome{OperationID: strings.Repeat("1", 32), InvitationID: strings.Repeat("2", 32), IdentityKey: target, Name: "Bob", TranscriptID: bytes.Repeat([]byte{7}, 64), At: now, Mode: peer.ModeEpoch, BegunMark: mark}
	_, err = st.Commit(ctx, outcome)
	desktopSpaceCheck(t, err == nil, "%v", err)
	_, err = st.Accept(ctx, target)
	desktopSpaceCheck(t, err == nil, "%v", err)
	_, err = st.DeliverExchange(ctx, peer.ExchangeDelivery{IdentityKey: target, OperationID: outcome.OperationID, Decision: peer.PairingAccepted, At: now})
	desktopSpaceCheck(t, err == nil, "%v", err)
}
