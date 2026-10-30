//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Session requests deliberately use the public wire format rather than new
// production types, so these tests also exercise decoding at the UID boundary.
type sessionWire map[string]json.RawMessage

func (m sessionWire) text(key string) string {
	var value string
	_ = json.Unmarshal(m[key], &value)
	return value
}

type sessionProbe struct {
	*operatorPeer
	next          int
	reviewNotices map[string]bool
	readTimeout   time.Duration
}

func sessionConnect(f *operatorFixture) (*sessionProbe, sessionWire) {
	f.t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.operator)}
	p.send(map[string]any{"type": "operator_session_open", "request_id": "open", "client": "acceptance"})
	return p, p.frame()
}

func sessionOpen(f *operatorFixture) *sessionProbe {
	f.t.Helper()
	p, response := sessionConnect(f)
	operatorEqual(f.t, "open correlation", response.text("request_id"), "open")
	if response.text("type") == "error" {
		f.t.Fatalf("session open rejected: %s", sessionJSON(response))
	}
	return p
}

func sessionJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (p *sessionProbe) send(request any) {
	p.t.Helper()
	operatorNoError(p.t, p.SetWriteDeadline(time.Now().Add(2*time.Second)))
	operatorNoError(p.t, json.NewEncoder(p.Conn).Encode(request))
}

func (p *sessionProbe) frame() sessionWire {
	p.t.Helper()
	timeout := p.readTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	operatorNoError(p.t, p.SetReadDeadline(time.Now().Add(timeout)))
	line, err := p.r.ReadBytes('\n')
	operatorNoError(p.t, err)
	operatorEqual(p.t, "bounded session frame", len(line) <= protocol.MaxFrameBytes, true)
	var frame sessionWire
	operatorNoError(p.t, json.Unmarshal(line, &frame))
	return frame
}

func (p *sessionProbe) request(kind string, fields map[string]any) (sessionWire, []sessionWire) {
	p.t.Helper()
	if fields == nil {
		fields = make(map[string]any)
	}
	p.next++
	id := fmt.Sprintf("request-%d", p.next)
	fields["type"], fields["request_id"] = kind, id
	p.send(fields)
	var events []sessionWire
	for i := 0; i < 300; i++ {
		frame := p.frame()
		if frame.text("request_id") == id {
			return frame, events
		}
		operatorEqual(p.t, "unsolicited frame correlation", frame.text("request_id"), "")
		events = append(events, frame)
	}
	p.t.Fatal("response starved by events")
	return nil, nil
}

func (p *sessionProbe) closed() {
	p.t.Helper()
	operatorNoError(p.t, p.SetReadDeadline(time.Now().Add(time.Second)))
	_, err := p.r.ReadByte()
	operatorEqual(p.t, "session closes after protocol error", err, io.EOF)
}

// Closing the transport and claiming ownership are independent hub inputs.
// Retry only owner_busy until the disconnect has been processed.
func sessionAcquireAfterClose(f *operatorFixture) *sessionProbe {
	f.t.Helper()
	until := time.Now().Add(2 * time.Second)
	for {
		p, response := sessionConnect(f)
		if response.text("type") != "error" {
			return p
		}
		p.Close()
		operatorEqual(f.t, "waiting for release", response.text("code"), "owner_busy")
		if time.Now().After(until) {
			f.t.Fatal("closed owner was not released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOperatorSessionOwnsReceiptsButAllowsReadsAndSends(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	author := f.peer("author", 5)
	inboxSend(t, author, inboxMessage("owned-inbox"))
	owner := sessionOpen(f)
	_, busy := sessionConnect(f)
	operatorEqual(t, "second owner", busy.text("code"), "owner_busy")
	operatorEqual(t, "read while owned", len(inboxReadPage(f, 0, 0, 16).Messages), 1)
	operatorEqual(t, "one-shot received refused", inboxReceived(f, "owned-inbox").Code, "owner_busy")
	operatorEqual(t, "one-shot ack refused", inboxAck(f, "owned-inbox").Code, "owner_busy")
	operatorEqual(t, "read and rejected writes preserve receipts", inboxReceiptCount(f, "received_at"), 0)
	requireOperatorAccepted(t, f.send(operatorRequest("external-send", "author")), "author")
	requireOperatorDelivery(t, author.read(), "external-send")
	r, events := owner.request("operator_received", map[string]any{"message_ids": []string{"owned-inbox"}})
	operatorEqual(t, "owner receipt", r.text("stage"), "adapter_received")
	operatorEqual(t, "no events before snapshot", len(events), 0)
	inboxStage(t, author, "owned-inbox", "adapter_received")
	r, _ = owner.request("operator_ack", map[string]any{"message_id": "owned-inbox"})
	operatorEqual(t, "owner ack", r.text("stage"), "agent_acknowledged")
	inboxStage(t, author, "owned-inbox", "agent_acknowledged")
	owner.Close()
	sessionAcquireAfterClose(f)
}

func TestOperatorSessionOpenAuditFailureDoesNotClaim(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	inboxExec(f, `CREATE TRIGGER fail_session_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	p, r := sessionConnect(f)
	operatorEqual(t, "open fails closed", r.text("code"), "internal")
	p.Close()
	inboxExec(f, `DROP TRIGGER fail_session_audit`)
	sessionOpen(f)
}

func TestOperatorSessionCloseAuditFailureStillReleases(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	owner := sessionOpen(f)
	inboxExec(f, `CREATE TRIGGER fail_session_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	owner.Close()
	// A new open must reach the failed audit, rather than remain owner_busy.
	until := time.Now().Add(2 * time.Second)
	for {
		p, r := sessionConnect(f)
		p.Close()
		if r.text("code") == "internal" {
			break
		}
		operatorEqual(t, "release pending", r.text("code"), "owner_busy")
		if time.Now().After(until) {
			t.Fatal("audit failure retained closed ownership")
		}
		time.Sleep(5 * time.Millisecond)
	}
	inboxExec(f, `DROP TRIGGER fail_session_audit`)
	sessionOpen(f)
}

func TestOperatorSessionBadArgumentsRemainCorrelatedAndUsable(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := sessionOpen(f)
	for _, tc := range []struct{ kind, code string }{
		{"future_operator_operation", "unsupported_operation"},
		{"operator_session_open", "invalid_state"},
		{"operator_received", "malformed_frame"},
	} {
		r, _ := p.request(tc.kind, nil)
		operatorEqual(t, tc.kind, r.text("code"), tc.code)
	}
	r, _ := p.request("operator_snapshot", nil)
	operatorEqual(t, "usable after errors", r.text("type") == "error", false)
}

func TestOperatorSessionInvalidEnvelopeClosesAndReleases(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("x", 129)} {
		t.Run(fmt.Sprintf("id_bytes_%d", len(id)), func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := sessionOpen(f)
			p.send(map[string]any{"type": "operator_snapshot", "request_id": id})
			operatorEqual(t, "bad envelope", p.frame().text("code"), "malformed_frame")
			p.closed()
			sessionAcquireAfterClose(f)
		})
	}
}

func TestOperatorSessionMalformedFrameClosesAndReleases(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := sessionOpen(f)
	_, err := io.WriteString(p.Conn, "{bad-json}\n")
	operatorNoError(t, err)
	operatorEqual(t, "bad framing", p.frame().text("code"), "malformed_frame")
	p.closed()
	sessionAcquireAfterClose(f)
}
