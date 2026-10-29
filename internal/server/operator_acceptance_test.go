//go:build linux

package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type operatorFixture struct {
	t               *testing.T
	agent, operator string
	db              *sql.DB
	endpoint        *OperatorServer
}
type operatorPeer struct {
	net.Conn
	r *bufio.Reader
	t *testing.T
}

func operatorDial(t *testing.T, path string) *operatorPeer {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		c, e := net.DialTimeout("unix", path, 100*time.Millisecond)
		if e == nil {
			t.Cleanup(func() { c.Close() })
			return &operatorPeer{c, bufio.NewReader(c), t}
		}
		if time.Now().After(until) {
			t.Fatal(e)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func (p *operatorPeer) write(m protocol.Message) {
	p.t.Helper()
	p.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if e := protocol.WriteFrame(p, m); e != nil {
		p.t.Fatal(e)
	}
}
func (p *operatorPeer) read() protocol.Message {
	p.t.Helper()
	p.SetReadDeadline(time.Now().Add(2 * time.Second))
	var m protocol.Message
	if e := protocol.ReadFrame(p.r, &m); e != nil {
		p.t.Fatal(e)
	}
	return m
}
func newOperatorFixture(t *testing.T, uid int, database string) *operatorFixture {
	t.Helper()
	if database == "" {
		database = filepath.Join(t.TempDir(), "operator.db")
	}
	st, e := store.Open(database)
	operatorNoError(t, e)
	seq, e := st.LastSeq(context.Background())
	operatorNoError(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	h := hub.New(st, seq, testLog())
	go h.Run(ctx)
	f := &operatorFixture{t: t, agent: socketPath(t), operator: socketPath(t)}
	f.db = openFixtureInspection(t, database)
	a := New(f.agent, h, testLog())
	o := NewOperator(f.operator, h, uid, testLog())
	f.endpoint = o
	done := make(chan error, 2)
	go func() { done <- a.Listen(ctx) }()
	go func() { done <- o.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		a.Close()
		o.Close()
		awaitOperatorShutdown(t, done, h.Done())
		f.db.Close()
		st.Close()
	})
	return f
}
func (f *operatorFixture) peer(id string, v int) *operatorPeer {
	f.t.Helper()
	p := operatorDial(f.t, f.agent)
	p.write(protocol.Message{Type: "hello", AgentID: id, ProtocolVersion: v})
	m := p.read()
	if m.Type != "welcome" {
		f.t.Fatalf("hello: %+v", m)
	}
	return p
}
func (f *operatorFixture) send(m protocol.Message) protocol.Message {
	f.t.Helper()
	p := operatorDial(f.t, f.operator)
	p.write(m)
	r := p.read()
	p.SetReadDeadline(time.Now().Add(time.Second))
	if _, e := p.r.ReadByte(); e != io.EOF {
		f.t.Fatalf("operator connection must close after response: %v", e)
	}
	return r
}
func operatorRequest(id, to string) protocol.Message {
	return protocol.Message{Type: "operator_send", MessageID: id, To: to, Payload: json.RawMessage(`{"text":"PRIVATE_OPERATOR_PAYLOAD"}`)}
}
func requireOperatorAccepted(t *testing.T, m protocol.Message, ids ...string) {
	t.Helper()
	operatorEqual(t, "acceptance type", m.Type, "ack")
	operatorEqual(t, "acceptance stage", m.Stage, "accepted")
	operatorEqual(t, "sequence assigned", m.Seq != 0, true)
	got := []string{}
	for _, r := range m.Recipients {
		got = append(got, r.AgentID)
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("recipients %v want %v", got, ids)
	}
}
func (f *operatorFixture) count(table string) int {
	f.t.Helper()
	var n int
	if e := f.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
		f.t.Fatal(e)
	}
	return n
}

func TestOperatorAcceptanceDirectAndFrozenRetry(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("alpha", 4)
	req := operatorRequest("direct", "alpha")
	accepted := f.send(req)
	requireOperatorAccepted(t, accepted, "alpha")
	m := p.read()
	requireOperatorDelivery(t, m, req.MessageID)
	retry := f.send(req)
	requireOperatorAccepted(t, retry, "alpha")
	if !retry.Repeated || retry.Seq != accepted.Seq {
		t.Fatalf("retry not frozen: %+v", retry)
	}
	req.Payload = json.RawMessage(`{"text":"changed"}`)
	if r := f.send(req); r.Code != "duplicate_message" {
		t.Fatalf("changed retry: %+v", r)
	}
	if f.count("messages") != 1 || f.count("operator_requests") != 1 {
		t.Fatal("retry persisted another message")
	}
	requireOperatorAuditPrivate(t, f.db)
}
func TestOperatorAcceptanceRejectUIDBeforeReading(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid()+1, "")
	p := operatorDial(t, f.operator)
	m := p.read() // Deliberately send no bytes.
	if m.Type != "error" || m.Code != "forbidden" {
		t.Fatalf("unauthorized peer: %+v", m)
	}
	var uid int
	var action, result string
	var id, scope sql.NullString
	operatorNoError(t, f.db.QueryRow("SELECT principal_uid,action,result,message_id,scope FROM operator_audit").Scan(&uid, &action, &result, &id, &scope))
	operatorEqual(t, "denied UID", uid, os.Getuid())
	operatorEqual(t, "denied action", action, "connect")
	operatorEqual(t, "denied result", result, "forbidden")
	operatorEqual(t, "denied message ID present", id.Valid, false)
	operatorEqual(t, "denied scope present", scope.Valid, false)
	if f.count("messages") != 0 {
		t.Fatal("unauthorized route persisted")
	}
}
func TestOperatorAcceptanceGroupAndBroadcast(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("zulu", 4)
	f.peer("alpha", 4)
	f.peer("legacy", 3)
	req := operatorRequest("group", "")
	req.Group = []string{"zulu", "alpha", "alpha"}
	requireOperatorAccepted(t, f.send(req), "alpha", "zulu")
	req.Group = []string{"alpha", "zulu"}
	if r := f.send(req); !r.Repeated {
		t.Fatalf("canonical group retry: %+v", r)
	}
	bad := operatorRequest("bad-group", "")
	bad.Group = []string{"alpha", "missing"}
	if r := f.send(bad); r.Type != "error" {
		t.Fatalf("partial group accepted: %+v", r)
	}
	if f.count("messages") != 1 || f.count("durable_inbox") != 2 {
		t.Fatal("failed group partially persisted")
	}
	requireOperatorFrozenBroadcast(t, f)
}
func TestOperatorAcceptanceFullInboxAtomicity(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := f.peer("full", 4)
	f.peer("free", 4)
	for i := 0; i < store.MaxPendingMessages; i++ {
		requireOperatorAccepted(t, f.send(operatorRequest(fmt.Sprintf("fill-%d", i), "full")), "full")
		p.read()
	}
	group := operatorRequest("overflow-group", "")
	group.Group = []string{"free", "full"}
	if r := f.send(group); r.Code != "inbox_full" {
		t.Fatalf("full group: %+v", r)
	}
	if f.count("messages") != store.MaxPendingMessages {
		t.Fatal("full group partially persisted")
	}
	r := f.send(operatorRequest("broadcast-full", "*"))
	requireOperatorAccepted(t, r, "free")
	if !reflect.DeepEqual(r.Excluded, []string{"full"}) {
		t.Fatalf("full exclusion: %+v", r)
	}
}
func TestOperatorAcceptanceAuditFailurePreventsRoute(t *testing.T) {
	for _, event := range []string{"INSERT", "UPDATE"} {
		t.Run(event, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			f.peer("alpha", 4)
			if _, e := f.db.Exec("CREATE TRIGGER fail_audit BEFORE " + event + " ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END"); e != nil {
				t.Fatal(e)
			}
			if r := f.send(operatorRequest("audit-failure", "alpha")); r.Code != "internal" {
				t.Fatalf("audit failure accepted: %+v", r)
			}
			for _, table := range []string{"messages", "message_metadata", "message_receipts", "durable_inbox", "operator_requests"} {
				if f.count(table) != 0 {
					t.Fatalf("audit failure left %s", table)
				}
			}
		})
	}
}
func TestOperatorAcceptanceAgentCannotForgeOrigin(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 4)
	f.peer("beta", 4)
	for _, kind := range []string{"msg", "ack"} {
		a.write(protocol.Message{Type: kind, MessageID: "spoof-" + kind, To: "beta", Origin: "operator", Payload: json.RawMessage(`{}`), Stage: "agent_acknowledged"})
		if r := a.read(); r.Code != "malformed_frame" {
			t.Fatalf("spoof %s: %+v", kind, r)
		}
	}
	if f.count("messages") != 0 {
		t.Fatal("forged origin persisted")
	}
	a.write(protocol.Message{Type: "msg", MessageID: "reply", To: "operator", Payload: json.RawMessage(`{}`)})
	if r := a.read(); r.Code != "unknown_recipient" {
		t.Fatalf("operator reply accepted in PR1: %+v", r)
	}
	p := operatorDial(t, f.agent)
	p.write(protocol.Message{Type: "hello", AgentID: "operator", ProtocolVersion: 4})
	if r := p.read(); r.Type != "error" {
		t.Fatalf("reserved operator registered: %+v", r)
	}
}

func TestOperatorAcceptanceRestartAndDowngrade(t *testing.T) {
	database := filepath.Join(t.TempDir(), "restart.db")
	t.Run("persist", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("alpha", 4)
		requireOperatorAccepted(t, f.send(operatorRequest("held", "alpha")), "alpha")
		p.read()
	})
	var originalSession string
	db, e := sql.Open("sqlite", database)
	operatorNoError(t, e)
	defer db.Close()
	operatorNoError(t, db.QueryRow("SELECT session_id FROM durable_inbox WHERE message_id='held'").Scan(&originalSession))
	t.Run("downgrade", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("alpha", 3)
		var session string
		operatorNoError(t, f.db.QueryRow("SELECT session_id FROM durable_inbox WHERE message_id='held'").Scan(&session))
		if session != originalSession {
			t.Fatal("v3 claimed an operator message")
		}
		// A normal message proves the v3 connection still works and is not replaying the held operator frame.
		b := f.peer("beta", 3)
		b.write(protocol.Message{Type: "msg", To: "alpha", Payload: json.RawMessage(`{"text":"ordinary"}`)})
		if m := p.read(); m.From != "beta" {
			t.Fatalf("v3 received operator replay: %+v", m)
		}
	})
	t.Run("upgrade", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := f.peer("alpha", 4)
		m := p.read()
		operatorEqual(t, "replay ID", m.MessageID, "held")
		operatorEqual(t, "replay origin", m.Origin, "operator")
		operatorEqual(t, "replay marker", m.Replayed, true)
	})
}

func TestOperatorAcceptanceOfflineCapabilityAfterRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "offline.db")
	t.Run("register", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		f.peer("modern", 4)
		f.peer("old", 3)
	})
	t.Run("offline", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		requireOperatorAccepted(t, f.send(operatorRequest("offline-modern", "modern")), "modern")
		if r := f.send(operatorRequest("offline-old", "old")); r.Code != "unknown_recipient" {
			t.Fatalf("offline v3 admitted: %+v", r)
		}
	})
}

func TestOperatorAcceptancePreservesExistingSocketPaths(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "live socket", "stale socket"} {
		t.Run(kind, func(t *testing.T) {
			path := socketPath(t)
			createOperatorExistingPath(t, path, kind)
			before, e := os.Lstat(path)
			operatorNoError(t, e)
			srv := NewOperator(path, nil, os.Getuid(), testLog())
			if e := srv.Listen(context.Background()); e == nil {
				t.Fatal("preexisting path replaced")
			}
			srv.Close()
			after, e := os.Lstat(path)
			if e != nil || !os.SameFile(before, after) {
				t.Fatalf("preexisting path changed: %v", e)
			}
		})
	}
}

func TestOperatorAcceptanceRejectMalformedAndLegacyCollision(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("alpha", 4)
	for i, mutate := range []func(*protocol.Message){func(m *protocol.Message) { m.MessageID = "" }, func(m *protocol.Message) { m.Group = []string{"alpha"} }, func(m *protocol.Message) { m.To = "" }, func(m *protocol.Message) { m.Type = "msg" }} {
		m := operatorRequest(fmt.Sprintf("invalid-%d", i), "alpha")
		mutate(&m)
		if r := f.send(m); r.Type != "error" {
			t.Fatalf("malformed request accepted: %+v", r)
		}
	}
	if f.count("messages") != 0 {
		t.Fatal("malformed request persisted")
	}
	// A pre-v4 agent named operator is historical data, never an operator retry.
	if _, e := f.db.Exec(`INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(900,CURRENT_TIMESTAMP,'operator','alpha','{"text":"PRIVATE_OPERATOR_PAYLOAD"}');
 INSERT INTO message_metadata(message_id,seq,sender_session,in_reply_to,ack_requested,origin) VALUES('legacy-collision',900,'legacy','',1,NULL);`); e != nil {
		t.Fatal(e)
	}
	if r := f.send(operatorRequest("legacy-collision", "alpha")); r.Code != "duplicate_message" {
		t.Fatalf("legacy sender trusted as operator: %+v", r)
	}
	if f.count("operator_requests") != 0 {
		t.Fatal("legacy collision became operator request")
	}
}

func TestOperatorAcceptanceGlobalCapacityRejectsWholeBroadcast(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("alpha", 4)
	f.peer("beta", 4)
	// Seed accounting with unrelated inboxes, leaving room for exactly one delivery.
	if _, e := f.db.Exec(`INSERT INTO durable_messages(message_id,wire_bytes,storage_bytes) VALUES('quota-fixture',1,1);
 WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<4095)
 INSERT INTO durable_inbox(message_id,agent_id,session_id) SELECT 'quota-fixture','unrelated-'||x,'' FROM n;`); e != nil {
		t.Fatal(e)
	}
	if r := f.send(operatorRequest("global-full", "*")); r.Code != "inbox_full" {
		t.Fatalf("global quota must reject whole scope: %+v", r)
	}
	operatorEqual(t, "global quota history", f.count("messages"), 0)
	operatorEqual(t, "global quota requests", f.count("operator_requests"), 0)
	operatorEqual(t, "global quota inbox", f.count("durable_inbox"), store.MaxGlobalPending-1)
}

func TestOperatorAcceptanceEmptyBroadcast(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("legacy", 3)
	if r := f.send(operatorRequest("empty-all", "*")); r.Code != "recipient_unavailable" {
		t.Fatalf("empty broadcast: %+v", r)
	}
	if f.count("messages") != 0 || f.count("operator_requests") != 0 {
		t.Fatal("empty broadcast persisted")
	}
}

func TestOperatorAcceptanceCloseCancelsUnreadConnectionAndPreservesReplacement(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := operatorDial(t, f.operator)
	operatorNoError(t, os.Remove(f.operator))
	operatorNoError(t, os.WriteFile(f.operator, []byte("replacement"), 0600))
	operatorNoError(t, f.endpoint.Close())
	p.SetReadDeadline(time.Now().Add(time.Second))
	_, readErr := p.r.ReadByte()
	var netErr net.Error
	operatorEqual(t, "closed connection returned error", readErr != nil, true)
	if errors.As(readErr, &netErr) {
		operatorEqual(t, "close did not time out", netErr.Timeout(), false)
	}
	data, e := os.ReadFile(f.operator)
	if e != nil || string(data) != "replacement" {
		t.Fatalf("replacement removed: %v", e)
	}
}

func TestOperatorAcceptanceMalformedWireAuditOmitsPayload(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := operatorDial(t, f.operator)
	p.SetWriteDeadline(time.Now().Add(time.Second))
	if _, e := io.WriteString(p, "{\"payload\":\"PRIVATE_OPERATOR_PAYLOAD\",invalid}\n"); e != nil {
		t.Fatal(e)
	}
	if r := p.read(); r.Code != "malformed_frame" {
		t.Fatalf("invalid JSON response: %+v", r)
	}
	var detail, result string
	var id, scope sql.NullString
	operatorNoError(t, f.db.QueryRow("SELECT result,detail,message_id,scope FROM operator_audit").Scan(&result, &detail, &id, &scope))
	operatorEqual(t, "malformed audit result", result, "rejected")
	operatorEqual(t, "malformed audit leaked payload", strings.Contains(detail, "PRIVATE_OPERATOR_PAYLOAD"), false)
	operatorEqual(t, "malformed audit message ID present", id.Valid, false)
	operatorEqual(t, "malformed audit scope present", scope.Valid, false)
	if f.count("messages") != 0 {
		t.Fatal("malformed frame routed")
	}
}

func operatorEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func operatorNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireOperatorDelivery(t *testing.T, m protocol.Message, id string) {
	t.Helper()
	operatorEqual(t, "delivery type", m.Type, "msg")
	operatorEqual(t, "delivery sender", m.From, "operator")
	operatorEqual(t, "delivery origin", m.Origin, "operator")
	operatorEqual(t, "delivery durable", m.Durable, true)
	operatorEqual(t, "delivery requests acknowledgment", m.AckRequested, true)
	operatorEqual(t, "delivery ID", m.MessageID, id)
}

func requireOperatorAuditPrivate(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, e := db.Query("SELECT coalesce(detail,'') FROM operator_audit")
	operatorNoError(t, e)
	defer rows.Close()
	for rows.Next() {
		var detail string
		operatorNoError(t, rows.Scan(&detail))
		if strings.Contains(detail, "PRIVATE_OPERATOR_PAYLOAD") {
			t.Fatal("audit leaked payload")
		}
	}
	operatorNoError(t, rows.Err())
}

func awaitOperatorShutdown(t *testing.T, done <-chan error, hubDone <-chan struct{}) {
	t.Helper()
	awaitFixtureShutdown(t, 2, done, hubDone)
}

func createOperatorExistingPath(t *testing.T, path, kind string) {
	t.Helper()
	switch kind {
	case "file":
		operatorNoError(t, os.WriteFile(path, []byte("keep"), 0600))
	case "symlink":
		operatorNoError(t, os.Symlink("missing", path))
	default:
		ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		operatorNoError(t, e)
		ln.SetUnlinkOnClose(false)
		t.Cleanup(func() { ln.Close() })
		if kind == "stale socket" {
			ln.Close()
		}
	}
}

func requireOperatorFrozenBroadcast(t *testing.T, f *operatorFixture) {
	t.Helper()
	all := operatorRequest("all", "*")
	r := f.send(all)
	requireOperatorAccepted(t, r, "alpha", "zulu")
	if !reflect.DeepEqual(r.Excluded, []string{"legacy"}) {
		t.Fatalf("exclusions: %+v", r)
	}
	f.peer("newcomer", 4)
	r = f.send(all)
	requireOperatorAccepted(t, r, "alpha", "zulu")
	if !r.Repeated || !reflect.DeepEqual(r.Excluded, []string{"legacy"}) {
		t.Fatalf("broadcast retry changed: %+v", r)
	}
	all.To = ""
	all.Group = []string{"alpha", "zulu"}
	if r := f.send(all); r.Code != "duplicate_message" {
		t.Fatalf("scope collision: %+v", r)
	}
}
