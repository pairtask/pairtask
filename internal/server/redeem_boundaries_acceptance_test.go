//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRedeemOperatorAcceptanceSecretFieldsAndAgentsRefused(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	before := f.count("operator_audit")
	for _, field := range []string{"pairing_code", "pairing_destination", "pairing_name"} {
		request := map[string]any{"type": "operator_pairing", "message_id": "bad-field", "pairing_op": "open", field: "739-281"}
		r := roomOperator(f.operatorFixture, request)
		operatorEqual(t, "unrelated redemption field refused", r.text("type"), "error")
		pairingNoCode(t, r, "739-281")
	}
	operatorEqual(t, "invalid commands never audited", f.count("operator_audit"), before)
	a, _ := roomPeer(f.operatorFixture, "agent", 9)
	r, events := a.request("operator_pairing", map[string]any{"pairing_op": "redeem", "pairing_destination": "host:443", "pairing_code": "739-281"})
	operatorEqual(t, "agent cannot redeem", r.text("code"), "forbidden")
	pairingNoCode(t, []any{r, events}, "739-281")
	operatorEqual(t, "agent created no join", f.count("peer_joins"), 0)
	operatorEqual(t, "agent created no audit", f.count("operator_audit"), before)
}

func TestRedeemOperatorAcceptanceAuditFailurePreventsDialAndIntent(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	_, err := f.db.Exec(`CREATE TRIGGER deny_redeem_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	operatorNoError(t, err)
	r := redeemCommand(f, "audit-failure", "127.0.0.1:1", "739-281")
	operatorEqual(t, "audit failure refused", r.text("type"), "error")
	pairingNoCode(t, r, "739-281")
	operatorEqual(t, "audit failure never persisted intent", f.count("peer_joins"), 0)
}

func TestRedeemOperatorAcceptanceBoundedListingKeepsExactOperationAddressable(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	self := pairingSnapshot(t, pairingCommand(f.operatorFixture, "status", "")).SelfIdentity
	for n := 0; n < 20; n++ {
		_, err := f.st.BeginJoin(context.Background(), peer.JoinStart{OperationID: fmt.Sprintf("join-%02d", n), Joiner: self, Name: "Bob", Destination: "host:443", At: time.Now().Add(time.Duration(n) * time.Second)})
		operatorNoError(t, err)
	}
	r := pairingCommand(f.operatorFixture, "status", "")
	var view struct {
		Joins     []redeemJoinWire `json:"joins"`
		Total     int              `json:"joins_total"`
		Truncated bool             `json:"joins_truncated"`
		Available bool             `json:"redeem_available"`
	}
	view = sessionDecode[struct {
		Joins     []redeemJoinWire `json:"joins"`
		Total     int              `json:"joins_total"`
		Truncated bool             `json:"joins_truncated"`
		Available bool             `json:"redeem_available"`
	}](t, r, "pairing_result")
	operatorEqual(t, "join row bound", len(view.Joins), 16)
	operatorEqual(t, "honest total", view.Total, 20)
	operatorEqual(t, "omission visible", view.Truncated, true)
	operatorEqual(t, "available", view.Available, true)
	operatorEqual(t, "newest first", view.Joins[0].OperationID, "join-19")
	operatorEqual(t, "bounded JSON frame", len(sessionJSON(r)) <= protocol.MaxFrameBytes, true)
	old := redeemReply(t, pairingCommand(f.operatorFixture, "status", "join-00"))
	operatorEqual(t, "unlisted operation addressable", old.OperationID, "join-00")
}

func pairingClient(t *testing.T, alice, bob *redeemOperatorFixture) *firstcontact.Conn {
	t.Helper()
	id, err := peer.LoadOrCreateIdentity(context.Background(), bob.st.PeerDir(), bob.st, rand.Reader)
	operatorNoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", alice.pairSocket)
	operatorNoError(t, err)
	c, err := firstcontact.Client(ctx, raw, id, time.Now)
	operatorNoError(t, err)
	t.Cleanup(func() { raw.Close(); c.Close() })
	return c
}

func TestRedeemOperatorAcceptancePeerEndpointNeverCarriesLocalAuthority(t *testing.T) {
	alice, bob := newRedeemOperator(t, redeemConfig(), ""), newRedeemOperator(t, redeemConfig(), "")
	before := alice.count("operator_audit")
	c := pairingClient(t, alice, bob)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	operatorNoError(t, c.Frames(ctx).Send([]byte(`{"type":"operator_pairing","pairing_op":"open"}`)))
	_, _ = c.Frames(ctx).Recv()
	operatorEqual(t, "peer cannot invoke local operator", alice.count("operator_audit"), before)
	operatorEqual(t, "no incoming request", alice.count("peer_pairing_requests"), 0)
	operatorEqual(t, "no agent traffic", alice.count("messages"), 0)
	operatorEqual(t, "no invitation opened", pairingSnapshot(t, pairingCommand(alice.operatorFixture, "status", "")).Invitation == nil, true)
}

func TestRedeemOperatorAcceptanceInboundConnectionLimit(t *testing.T) {
	alice, bob := newRedeemOperator(t, redeemConfig(), ""), newRedeemOperator(t, redeemConfig(), "")
	for range 4 {
		pairingClient(t, alice, bob)
	}
	raw, err := net.DialTimeout("unix", alice.pairSocket, time.Second)
	operatorNoError(t, err)
	defer raw.Close()
	operatorNoError(t, raw.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = raw.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("fifth unauthenticated connection was admitted")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("fifth connection waited instead of immediate refusal")
	}
	operatorEqual(t, "no attempt without hello", alice.count("peer_pairing_requests"), 0)
}

func TestRedeemOperatorAcceptanceAuditContainsNoCode(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	redeemReply(t, redeemCommand(f, "code-audit", "127.0.0.1:1", "739-281"))
	awaitRedeemState(t, f, "code-audit", peer.JoinFailed)
	rows, err := f.db.Query(`SELECT action,coalesce(message_id,''),coalesce(scope,''),result,coalesce(detail,'') FROM operator_audit`)
	operatorNoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var action, id, scope, result, detail string
		operatorNoError(t, rows.Scan(&action, &id, &scope, &result, &detail))
		for _, value := range []string{action, id, scope, result, detail} {
			operatorEqual(t, "code absent from audit", strings.Contains(value, "739-281"), false)
			operatorEqual(t, "normalized code absent from audit", strings.Contains(value, "739281"), false)
		}
	}
	operatorNoError(t, rows.Err())
}
