//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type claimsStatusView struct {
	Schema                int                 `json:"schema_version"`
	Health                string              `json:"health"`
	Connected             int                 `json:"connected_sessions"`
	Available             bool                `json:"claims_available"`
	Claims                []protocol.ClaimSet `json:"claims"`
	Total                 int                 `json:"claims_total"`
	Truncated             bool                `json:"claims_truncated"`
	DeclarationsAvailable bool                `json:"agent_status_available"`
}

func readClaimsStatus(t *testing.T, f *operatorFixture) claimsStatusView {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.agent)}
	defer p.Close()
	p.write(protocol.Message{Type: "status"})
	response := p.frame()
	operatorEqual(t, "read-only reply", response.text("type"), "status")
	var out claimsStatusView
	operatorNoError(t, json.Unmarshal(response["status"], &out))
	return out
}

func TestClaimsSurfacesAcceptanceStatusIsReadOnlyAndPreservesClaimState(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := claimPeer(f, "alpha")
	claimParent(t, f, "claim-parent", a)
	operatorEqual(t, "claim accepted", claimCall(a, "claim_set", claimSpec{"claim-parent", "build", "owner"}).text("type"), "claim_result")
	beforeMessages, beforeAgents := f.count("messages"), f.count("agents")
	out := readClaimsStatus(t, f)
	operatorEqual(t, "claims schema", out.Schema, 3)
	operatorEqual(t, "claims available", out.Available, true)
	operatorEqual(t, "one claimed parent", out.Total, 1)
	operatorEqual(t, "one claim set", len(out.Claims), 1)
	operatorEqual(t, "owner retained", out.Claims[0].Claims[0].AgentID, "alpha")
	operatorEqual(t, "active retained", out.Claims[0].Claims[0].State, "active")
	claimCall(a, "claim_done", claimSpec{"claim-parent", "build", "owner"})
	done := readClaimsStatus(t, f)
	operatorEqual(t, "completed state visible", done.Claims[0].Claims[0].State, "done")
	operatorEqual(t, "reads create no messages", f.count("messages"), beforeMessages)
	operatorEqual(t, "reads register no agent", f.count("agents"), beforeAgents)
}

func TestClaimsSurfacesAcceptanceStatusEmptyAndFailuresRemainDistinct(t *testing.T) {
	for _, table := range []string{"claims", "agent_status"} {
		t.Run(table, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			f.peer("silent", protocol.Version)
			empty := readClaimsStatus(t, f)
			operatorEqual(t, "empty is available", empty.Available, true)
			if empty.Claims == nil || len(empty.Claims) != 0 {
				t.Fatal("known empty claims must be an empty JSON array")
			}
			inboxExec(f, "DROP TABLE "+table)
			failed := readClaimsStatus(t, f)
			operatorEqual(t, "failed store read degrades health", failed.Health, "degraded")
			operatorEqual(t, "transport survives", failed.Connected, 1)
			operatorEqual(t, "independent claims availability", failed.Available, table != "claims")
			operatorEqual(t, "independent status availability", failed.DeclarationsAvailable, table != "agent_status")
			if failed.Claims == nil {
				t.Fatal("unavailable claims must still encode as an array")
			}
		})
	}
}

func TestClaimsSurfacesAcceptanceStatusBoundsEachLevel(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	base := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 33; i++ {
		parent := fmt.Sprintf("parent-%02d", i)
		_, err := f.db.Exec(`INSERT INTO claims(operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES(?,?,?,?,?,?,?)`, parent, "active", "owner", "alpha", "topic", "active", base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339))
		operatorNoError(t, err)
	}
	for i := 0; i < 20; i++ {
		_, err := f.db.Exec(`INSERT INTO claims(operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES(?,?,?,?,?,?,?)`, "parent-32", fmt.Sprintf("old-%02d", i), "owner", "alpha", "old topic", "released", base.Format(time.RFC3339))
		operatorNoError(t, err)
	}
	out := readClaimsStatus(t, f)
	operatorEqual(t, "parent count retained", out.Total, 33)
	operatorEqual(t, "parent page bounded", len(out.Claims), 32)
	operatorEqual(t, "parent omission visible", out.Truncated, true)
	latest := out.Claims[0]
	operatorEqual(t, "newest claimed parent first", latest.OperatorMessageID, "parent-32")
	operatorEqual(t, "individual total retained", latest.Total, 21)
	operatorEqual(t, "individual page bounded", len(latest.Claims), 16)
	operatorEqual(t, "individual omission visible", latest.Truncated, true)
	operatorEqual(t, "holder not displaced by released history", latest.Claims[0].State, "active")
}
