//go:build linux

package server

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestWorkspaceAcceptanceOverOperatorSocket(t *testing.T) {
	f := newConfiguredRedeemOperator(t, redeemConfig(), "", func(h *hub.Hub) { h.SetSpaces(hub.SpaceConfig{}) })
	req := protocol.SpaceRequest{Op: protocol.SpaceOpMapAccept, Lineage: strings.Repeat("ab", 32), Epoch: 2, Person: "sha256:" + strings.Repeat("cd", 32), At: time.Now().UTC().Truncate(time.Millisecond)}
	out := f.send(protocol.Message{Type: protocol.TypeOperatorSpace, SpaceRequest: &req, MessageID: "workspace-accept"})
	if out.SpaceResult == nil {
		t.Fatalf("acceptance failed: %s %s", out.Code, out.Detail)
	}
	a, err := identity.ParseAcceptance(out.SpaceResult.Acceptance)
	operatorNoError(t, err)
	operatorNoError(t, a.Check(ed25519.PublicKey(out.SpaceResult.WorkspaceKey)))
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	operatorEqual(t, "workspace signer", a.Workspace, ep.Identity.Fingerprint())
	operatorEqual(t, "person", a.Person, req.Person)
	operatorEqual(t, "epoch", a.Epoch, req.Epoch)
	operatorEqual(t, "at", a.At.Equal(req.At), true)
}
