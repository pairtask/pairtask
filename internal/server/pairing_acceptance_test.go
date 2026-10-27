//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Exercise the public JSON protocol at the authenticated socket boundary.
type pairingInvitationWire struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	ExpiresAt    time.Time `json:"expires_at"`
	AttemptsLeft int       `json:"attempts_left"`
}
type pairingRequestWire struct {
	IdentityKey   string    `json:"identity_key"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	FirstPairedAt time.Time `json:"first_paired_at"`
}
type pairingSnapshotWire struct {
	Invitation *pairingInvitationWire `json:"invitation"`
	Requests   []pairingRequestWire   `json:"requests"`
	Total      int                    `json:"requests_total"`
	Truncated  bool                   `json:"requests_truncated"`
}

func pairingCommand(f *operatorFixture, op, target string) sessionWire {
	f.t.Helper()
	return roomOperator(f, map[string]any{"type": "operator_pairing", "pairing_op": op, "pairing_target": target})
}
func pairingSnapshot(t *testing.T, frame sessionWire) pairingSnapshotWire {
	t.Helper()
	operatorEqual(t, "pairing response: "+sessionJSON(frame), frame.text("type"), "operator_pairing_result")
	return sessionDecode[pairingSnapshotWire](t, frame, "pairing_result")
}
func pairingOpen(f *operatorFixture) *pairingInvitationWire {
	f.t.Helper()
	got := pairingSnapshot(f.t, pairingCommand(f, "open", "")).Invitation
	if got == nil {
		f.t.Fatal("open omitted invitation")
	}
	if !regexp.MustCompile(`^[0-9]{3}-[0-9]{3}$`).MatchString(got.Code) {
		f.t.Fatal("code must contain six formatted decimal digits")
	}
	if got.ID == "" {
		f.t.Fatal("open omitted invitation ID")
	}
	operatorEqual(f.t, "initial attempt budget", got.AttemptsLeft, 3)
	remaining := time.Until(got.ExpiresAt)
	if remaining < 9*time.Minute || remaining > 10*time.Minute {
		f.t.Fatalf("unexpected invitation lifetime: %v", remaining)
	}
	return got
}
func pairingNoCode(t *testing.T, frame any, code string) {
	t.Helper()
	if strings.Contains(sessionJSON(frame), code) {
		t.Fatal("pairing code escaped its open response")
	}
}
func pairingSeed(f *operatorFixture, key, state string) {
	f.t.Helper()
	_, err := f.db.Exec(`INSERT INTO peer_pairing_requests(identity_key,name,state,first_paired_at) VALUES(?,?,?,?)`, key, "same display name", state, "2026-10-27T12:00:00Z")
	operatorNoError(f.t, err)
}
func pairingStoredState(f *operatorFixture, key string) string {
	f.t.Helper()
	var state string
	operatorNoError(f.t, f.db.QueryRow("SELECT state FROM peer_pairing_requests WHERE identity_key=?", key).Scan(&state))
	return state
}

func TestPairingOperatorAcceptanceLifecycleAndStaleCancel(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	operatorEqual(t, "initial invitation absent", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation == nil, true)
	first := pairingOpen(f)
	for _, op := range []string{"status", "list"} {
		frame := pairingCommand(f, op, "")
		pairingNoCode(t, frame, first.Code)
		status := pairingSnapshot(t, frame).Invitation
		if status == nil {
			t.Fatal("active invitation missing")
		}
		operatorEqual(t, "same invitation", status.ID, first.ID)
		operatorEqual(t, "code omitted", status.Code, "")
	}
	second := pairingOpen(f)
	operatorEqual(t, "replacement has new ID", second.ID != first.ID, true)
	operatorEqual(t, "stale cancellation refused", pairingCommand(f, "cancel", first.ID).text("type"), "error")
	operatorEqual(t, "replacement survives stale cancel", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation.ID, second.ID)
	pairingSnapshot(t, pairingCommand(f, "cancel", second.ID))
	operatorEqual(t, "cancel closes invitation", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation == nil, true)
}

func TestPairingOperatorAcceptanceAgentAndWrongUIDCannotOperate(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	active := pairingOpen(f)
	a, _ := roomPeer(f, "worker", 9)
	for _, op := range []string{"open", "status", "list", "cancel", "accept", "reject"} {
		response, _ := a.request("operator_pairing", map[string]any{"pairing_op": op, "pairing_target": active.ID})
		operatorEqual(t, "agent pairing denied", response.text("type"), "error")
		pairingNoCode(t, response, active.Code)
	}
	operatorEqual(t, "agent did not mutate invitation", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation.ID, active.ID)
	denied := newOperatorFixture(t, os.Getuid()+1, "")
	// UID authentication must reject without even waiting for a command.
	p := operatorDial(t, denied.operator)
	operatorEqual(t, "wrong UID rejected before frame", p.read().Code, "forbidden")
}

func TestPairingOperatorAcceptanceCodeStaysOutOfMessagesAuditAndSessionEvents(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, _ := roomPeer(f, "worker", 9)
	s, hello := sessionConnect(f)
	var info struct {
		Capabilities []string `json:"capabilities"`
	}
	operatorNoError(t, json.Unmarshal(hello["session"], &info))
	operatorEqual(t, "pairing capability advertised", slices.Contains(info.Capabilities, "pairing"), true)
	s.request("operator_snapshot", nil) // Subscribe before opening.
	opened, events := s.request("operator_pairing", map[string]any{"pairing_op": "open"})
	invitation := pairingSnapshot(t, opened).Invitation
	if invitation == nil || invitation.Code == "" {
		t.Fatal("session open omitted code")
	}
	pairingNoCode(t, events, invitation.Code)
	for _, op := range []string{"status", "list"} {
		r, ev := s.request("operator_pairing", map[string]any{"pairing_op": op})
		pairingSnapshot(t, r)
		pairingNoCode(t, []any{r, ev}, invitation.Code)
	}
	for _, kind := range []string{"operator_snapshot", "operator_history"} {
		r, ev := s.request(kind, nil)
		operatorEqual(t, "read succeeded", r.text("type") != "error", true)
		pairingNoCode(t, []any{r, ev}, invitation.Code)
	}
	// A correlated agent read is a barrier: unsolicited pairing deliveries
	// would be returned among its preceding frames.
	r, ev := a.request("room_list", nil)
	pairingNoCode(t, []any{r, ev}, invitation.Code)
	operatorEqual(t, "no unsolicited agent delivery", len(ev), 0)
	for _, table := range []string{"messages", "message_metadata", "message_receipts", "durable_inbox", "operator_requests"} {
		operatorEqual(t, "pairing did not write "+table, f.count(table), 0)
	}
	rows, err := f.db.Query(`SELECT action,coalesce(message_id,''),coalesce(scope,''),result,coalesce(detail,'') FROM operator_audit`)
	operatorNoError(t, err)
	defer rows.Close()
	var pairingRows int
	for rows.Next() {
		var action, id, scope, result, detail string
		operatorNoError(t, rows.Scan(&action, &id, &scope, &result, &detail))
		pairingNoCode(t, []string{action, id, scope, result, detail}, invitation.Code)
		if strings.HasPrefix(action, "pairing ") {
			pairingRows++
		}
	}
	operatorNoError(t, rows.Err())
	operatorEqual(t, "each pairing operation audited", pairingRows, 3)
}

func TestPairingOperatorAcceptanceDecisionsAreDurableAndInvitationsAreNot(t *testing.T) {
	database := filepath.Join(t.TempDir(), "pairing.db")
	t.Run("decide", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		pairingSeed(f, "key-a", "pending_acceptance")
		pairingSeed(f, "key-b", "pending_acceptance")
		pairingOpen(f)
		for _, decision := range []struct{ key, op, state, opposite string }{
			{"key-a", "accept", "accepted", "reject"}, {"key-b", "reject", "rejected", "accept"},
		} {
			for range 2 {
				pairingSnapshot(t, pairingCommand(f, decision.op, decision.key))
			}
			refusal := pairingCommand(f, decision.opposite, decision.key)
			operatorEqual(t, "opposite decision refused", refusal.text("code"), "pairing_refused")
			operatorEqual(t, "decision retained", pairingStoredState(f, decision.key), decision.state)
		}
		operatorEqual(t, "prefix not accepted on wire", pairingCommand(f, "accept", "key").text("type"), "error")
	})
	t.Run("restart", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		snap := pairingSnapshot(t, pairingCommand(f, "list", ""))
		operatorEqual(t, "restart invalidated invitation", snap.Invitation == nil, true)
		operatorEqual(t, "requests survive", snap.Total, 2)
		operatorEqual(t, "accepted survives", pairingStoredState(f, "key-a"), "accepted")
		operatorEqual(t, "rejected survives", pairingStoredState(f, "key-b"), "rejected")
		operatorEqual(t, "same names do not merge identities", len(snap.Requests), 2)
		for _, r := range snap.Requests {
			operatorEqual(t, "timestamp retained", r.FirstPairedAt.Format(time.RFC3339), "2026-10-27T12:00:00Z")
		}
	})
}

func TestPairingOperatorAcceptanceBoundedListAndFullKeyDecision(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	pairingSeed(f, "000-accepted", "accepted")
	for i := 64; i >= 0; i-- {
		pairingSeed(f, fmt.Sprintf("pending-%02d", i), "pending_acceptance")
	}
	snap := pairingSnapshot(t, pairingCommand(f, "list", ""))
	operatorEqual(t, "bounded list", len(snap.Requests), 64)
	operatorEqual(t, "honest total", snap.Total, 66)
	operatorEqual(t, "omission visible", snap.Truncated, true)
	for i, r := range snap.Requests {
		operatorEqual(t, "pending first and keys sorted", r.IdentityKey, fmt.Sprintf("pending-%02d", i))
	}
	// A truncated display must not restrict the broker's full-key decision.
	pairingSnapshot(t, pairingCommand(f, "accept", "pending-64"))
	operatorEqual(t, "unlisted identity remains addressable", pairingStoredState(f, "pending-64"), "accepted")
}

func TestPairingOperatorAcceptanceMalformedCommandsDoNotMutate(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	active := pairingOpen(f)
	for _, op := range []string{"", "unknown", "accept", "reject", "cancel"} {
		response := pairingCommand(f, op, "")
		operatorEqual(t, "invalid command refused", response.text("type"), "error")
		pairingNoCode(t, response, active.Code)
	}
	oversizedID := roomOperator(f, map[string]any{"type": "operator_pairing", "pairing_op": "open", "message_id": strings.Repeat("x", 129)})
	operatorEqual(t, "oversized correlation ID rejected before mutation", oversizedID.text("type"), "error")
	operatorEqual(t, "invalid commands preserve invitation", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation.ID, active.ID)
	operatorEqual(t, "no manufactured request", f.count("peer_pairing_requests"), 0)
}

func TestPairingOperatorAcceptanceByteBoundKeepsResponsesUsable(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	for i := range 4 {
		key := fmt.Sprintf("large-%d", i)
		pairingSeed(f, key, "pending_acceptance")
		_, err := f.db.Exec("UPDATE peer_pairing_requests SET name=? WHERE identity_key=?", strings.Repeat("large name ", 40000), key)
		operatorNoError(t, err)
	}
	snap := pairingSnapshot(t, pairingCommand(f, "list", ""))
	operatorEqual(t, "byte-bound total", snap.Total, 4)
	operatorEqual(t, "byte-bound omission visible", snap.Truncated, true)
	if len(snap.Requests) >= 4 {
		t.Fatal("oversized listing was not bounded")
	}
	// Even a single oversized row cannot prevent open from returning its code.
	_, err := f.db.Exec("UPDATE peer_pairing_requests SET name=?", strings.Repeat("x", protocol.MaxFrameBytes+1))
	operatorNoError(t, err)
	pairingOpen(f)
}

func TestPairingOperatorAcceptanceAuditUnavailablePreventsMutation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	pairingSeed(f, "pending", "pending_acceptance")
	inboxExec(f, `CREATE TRIGGER fail_pairing_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	for _, op := range []string{"open", "accept", "reject"} {
		target := ""
		if op != "open" {
			target = "pending"
		}
		operatorEqual(t, "audit failure refused", pairingCommand(f, op, target).text("type"), "error")
	}
	inboxExec(f, `DROP TRIGGER fail_pairing_audit`)
	operatorEqual(t, "no unaudited invitation", pairingSnapshot(t, pairingCommand(f, "status", "")).Invitation == nil, true)
	operatorEqual(t, "no unaudited decision", pairingStoredState(f, "pending"), "pending_acceptance")
}
