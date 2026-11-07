//go:build linux

package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairOperatorAcceptanceAuditedResultAndReplayKeepFirstGeneration(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	req := f.request(t, "stable-unpair", 1)
	out := f.send(req)
	operatorEqual(t, "operator unpair", out.Type, protocol.TypeOperatorContactResult)
	var result struct {
		Changed           bool
		Generation, Epoch int64
		UnpairedAt        string
	}
	operatorNoError(t, json.Unmarshal(f.fields(t, out)["contact_unpaired"], &result))
	operatorEqual(t, "first unpair changed", result.Changed, true)
	operatorEqual(t, "seen epoch", result.Epoch, int64(1))
	operatorEqual(t, "mutation audited", f.count("operator_audit") > 0, true)
	replay := f.send(req)
	operatorEqual(t, "replay result", string(f.fields(t, replay)["contact_unpaired"]), string(f.fields(t, out)["contact_unpaired"]))
	var generation int64
	operatorNoError(t, f.db.QueryRow(`SELECT generation FROM peer_contacts WHERE identity_key=?`, f.key).Scan(&generation))
	operatorEqual(t, "generation moved once", generation, result.Generation)
	operatorEqual(t, "one operation registry", f.count("operator_contact_requests"), 1)
	operatorEqual(t, "one durable unpair result", f.count("contact_unpair_results"), 1)
}
func TestUnpairOperatorAcceptanceAgentCannotMutateOrCreateAudit(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	before := f.count("operator_audit")
	agent := f.peer("unpair-agent", protocol.Version)
	agent.write(f.request(t, "forged-unpair", 1))
	operatorEqual(t, "agent refused", agent.read().Code, protocol.ErrForbidden)
	operatorEqual(t, "no audit for forbidden request", f.count("operator_audit"), before)
	admitted, err := f.st.PeerMessages().Admit(context.Background(), f.key)
	operatorNoError(t, err)
	operatorEqual(t, "contact stayed active", admitted != nil, true)
}
func TestUnpairOperatorAcceptanceStaleEpochCannotRevokeCurrentPairing(t *testing.T) {
	f := newUnpairOperatorFixture(t)
	out := f.send(f.request(t, "wrong-epoch", 2))
	operatorEqual(t, "stale epoch rejected", out.Type, protocol.TypeError)
	var ended string
	operatorNoError(t, f.db.QueryRow(`SELECT unpaired_at FROM peer_contacts WHERE identity_key=?`, f.key).Scan(&ended))
	operatorEqual(t, "unchanged contact", ended, "")
	// The correctly confirmed operation still works after that refusal.
	out = f.send(f.request(t, "right-epoch", 1))
	operatorEqual(t, "current epoch accepted", out.Type, protocol.TypeOperatorContactResult)
}
