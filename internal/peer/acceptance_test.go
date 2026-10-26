package peer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptanceLocalDecisionCannotBeForgedByPeer(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	cert := acceptanceCertificate(t, "alice")
	c := p.connect(t, cert)
	record := acceptanceRecord("local-decision")
	acceptanceStatus(t, acceptanceSend(t, c, record), StatusPending)
	for _, kind := range []Kind{KindAccept, KindIgnore} {
		forged := record
		forged.Kind = kind
		acceptanceStatus(t, acceptanceSend(t, c, forged), StatusRefused)
	}
	fp := acceptanceFingerprint(t, cert)
	state, ok, err := p.store.Get(context.Background(), fp, record.RequestID)
	if err != nil || !ok || state.Status != StatusPending {
		t.Fatalf("remote changed local request: %+v, %v, %v", state, ok, err)
	}
	decided := make(chan error, 1)
	go func() { _, err := p.store.Accept(context.Background(), fp, record.RequestID); decided <- err }()
	select {
	case err := <-decided:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("local acceptance deadlocked")
	}
	state, ok, err = p.store.Get(context.Background(), fp, record.RequestID)
	if err != nil || !ok || state.Status != StatusAccepted {
		t.Fatalf("local decision missing: %+v, %v, %v", state, ok, err)
	}
}

func TestAcceptancePeerSettlementRequiresMatchingOutboundRequest(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	alice, mallory := acceptanceCertificate(t, "alice"), acceptanceCertificate(t, "mallory")
	fp := acceptanceFingerprint(t, alice)
	if _, err := p.store.SaveOutbound(context.Background(), fp, "outbound"); err != nil {
		t.Fatal(err)
	}
	record := acceptanceRecord("outbound")
	record.Kind = KindAccept
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, mallory), record), StatusRefused)
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, alice), record), StatusAccepted)
	if state, err := p.store.Settle(context.Background(), fp, "outbound", StatusAccepted); err != nil || state.Status != StatusAccepted || !state.Existing {
		t.Fatalf("settlement not persisted: %+v, %v", state, err)
	}
	record.Kind = KindIgnore
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, alice), record), StatusRefused)
}

func TestAcceptancePendingLimitsDoNotBreakRetries(t *testing.T) {
	p := acceptanceStart(t, GateConfig{Limits: Limits{MaxPending: 2, MaxPendingPerPeer: 1}})
	alice, bob := acceptanceCertificate(t, "alice"), acceptanceCertificate(t, "bob")
	a := p.connect(t, alice)
	record := acceptanceRecord("first")
	acceptanceStatus(t, acceptanceSend(t, a, record), StatusPending)
	acceptanceStatus(t, acceptanceSend(t, a, record), StatusDuplicate)
	acceptanceStatus(t, acceptanceSend(t, a, acceptanceRecord("second")), StatusRefused)
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, bob), record), StatusPending)
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, acceptanceCertificate(t, "charlie")), record), StatusRefused)
	total, perPeer, err := p.store.Pending(context.Background(), acceptanceFingerprint(t, alice))
	if err != nil || total != 2 || perPeer != 1 {
		t.Fatalf("pending counts = %d,%d,%v", total, perPeer, err)
	}
}

func TestAcceptanceConcurrentQuotaAdmission(t *testing.T) {
	p := acceptanceStart(t, GateConfig{Limits: Limits{MaxPending: 1, MaxPendingPerPeer: 1, MaxConns: 32}})
	cert := acceptanceCertificate(t, "alice")
	const count = 12
	connections := make([]*tls.Conn, count)
	for i := range connections {
		connections[i] = p.connect(t, cert)
	}
	start := make(chan struct{})
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i, c := range connections {
		wg.Add(1)
		go func(i int, c *tls.Conn) {
			defer wg.Done()
			<-start
			record := acceptanceRecord(strings.Repeat("q", i+1))
			if err := json.NewEncoder(c).Encode(record); err != nil {
				errors <- err
				return
			}
			var reply Reply
			if err := json.NewDecoder(c).Decode(&reply); err != nil {
				errors <- err
			}
		}(i, c)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	total, _, err := p.store.Pending(context.Background(), acceptanceFingerprint(t, cert))
	if err != nil || total != 1 {
		t.Fatalf("concurrent admission stored %d requests, error %v", total, err)
	}
}

func TestAcceptanceStrictEnvelopeRejectsExtraData(t *testing.T) {
	for _, suffix := range []string{`,"to":"operator"}`, `} {"v":1}`, `}}`} {
		t.Run(suffix, func(t *testing.T) {
			p := acceptanceStart(t, GateConfig{})
			cert := acceptanceCertificate(t, "alice")
			c := p.connect(t, cert)
			encoded, err := json.Marshal(acceptanceRecord("extra-data"))
			if err != nil {
				t.Fatal(err)
			}
			c.Write(append(append(encoded[:len(encoded)-1], []byte(suffix)...), '\n'))
			var reply Reply
			if err := json.NewDecoder(c).Decode(&reply); err == nil && reply.Status != StatusRefused {
				t.Fatalf("invalid record accepted: %+v", reply)
			}
			if _, ok, err := p.store.Get(context.Background(), acceptanceFingerprint(t, cert), "extra-data"); err != nil || ok {
				t.Fatalf("invalid record stored: %v, %v", ok, err)
			}
		})
	}
}

func TestAcceptanceOpaqueRelayCarriesContactRequest(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	c := p.connect(t, acceptanceCertificate(t, "alice"))
	record := acceptanceRecord("opaque-1")
	acceptanceStatus(t, acceptanceSend(t, c, record), "pending")
	if c.ConnectionState().Version != tls.VersionTLS13 {
		t.Fatal("expected TLS 1.3")
	}
	wire := p.wire.snapshot()
	if len(wire) == 0 {
		t.Fatal("relay capture was empty")
	}
	for _, marker := range []string{record.Note, "contact_request", record.RequestID} {
		if bytes.Contains(wire, []byte(marker)) {
			t.Fatalf("relay saw plaintext marker %q", marker)
		}
	}
}

func TestAcceptanceGateRechecksDiscoveryForEveryRequest(t *testing.T) {
	var visible atomic.Bool
	visible.Store(true)
	p := acceptanceStart(t, GateConfig{Discoverable: visible.Load})
	c := p.connect(t, acceptanceCertificate(t, "alice"))
	acceptanceStatus(t, acceptanceSend(t, c, acceptanceRecord("visible")), "pending")
	visible.Store(false)
	acceptanceStatus(t, acceptanceSend(t, c, acceptanceRecord("stale-cache")), "refused")
	visible.Store(true)
	acceptanceStatus(t, acceptanceSend(t, c, acceptanceRecord("visible-again")), "pending")
}

func TestAcceptanceDuplicateAndChangedContent(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	c := p.connect(t, acceptanceCertificate(t, "alice"))
	record := acceptanceRecord("same-id")
	acceptanceStatus(t, acceptanceSend(t, c, record), "pending")
	acceptanceStatus(t, acceptanceSend(t, c, record), "duplicate")
	record.Note = "changed under an existing request ID"
	acceptanceStatus(t, acceptanceSend(t, c, record), "refused")
}

func TestAcceptanceNamesCannotImpersonateCertificate(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	record := acceptanceRecord("same-name-same-id")
	for _, name := range []string{"alice-key", "other-key"} {
		c := p.connect(t, acceptanceCertificate(t, name))
		acceptanceStatus(t, acceptanceSend(t, c, record), "pending")
		c.Close()
	}
}

func TestAcceptanceRefusesPrivilegedAndFutureVerbs(t *testing.T) {
	for _, kind := range []string{"operator_send", "operator_ack", "msg", "hello", "grant", "revoke", "contact_accept", "contact_ignore"} {
		t.Run(kind, func(t *testing.T) {
			p := acceptanceStart(t, GateConfig{})
			c := p.connect(t, acceptanceCertificate(t, "alice"))
			record := acceptanceRecord("forbidden")
			record.Kind = Kind(kind)
			acceptanceStatus(t, acceptanceSend(t, c, record), "refused")
		})
	}
}

func TestAcceptanceRefusesInvalidMetadata(t *testing.T) {
	for _, name := range []string{"version", "empty-id", "long-id", "long-note"} {
		t.Run(name, func(t *testing.T) {
			p := acceptanceStart(t, GateConfig{})
			c := p.connect(t, acceptanceCertificate(t, "alice"))
			record := acceptanceRecord("metadata")
			switch name {
			case "version":
				record.Version++
			case "empty-id":
				record.RequestID = ""
			case "long-id":
				record.RequestID = strings.Repeat("x", 129)
			case "long-note":
				record.Note = strings.Repeat("x", MaxNoteBytes+1)
			}
			acceptanceStatus(t, acceptanceSend(t, c, record), "refused")
		})
	}
}

func TestAcceptanceFragmentedFrame(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	c := p.connect(t, acceptanceCertificate(t, "alice"))
	encoded, err := json.Marshal(acceptanceRecord("fragmented"))
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	for _, b := range encoded {
		if _, err := c.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	acceptanceStatus(t, reply, "pending")
}

func TestAcceptanceRejectsOversizedFrameAndRecovers(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	cert := acceptanceCertificate(t, "alice")
	c := p.connect(t, cert)
	c.Write([]byte(`{"v":1,"padding":"` + strings.Repeat("x", MaxRecordBytes) + "\"}\n"))
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err == nil && reply.Status != "refused" {
		t.Fatalf("oversized frame accepted: %+v", reply)
	}
	c.Close()
	acceptanceStatus(t, acceptanceSend(t, p.connect(t, cert), acceptanceRecord("after-oversize")), "pending")
}

func TestAcceptancePlaintextCannotBecomePeerRequest(t *testing.T) {
	p := acceptanceStart(t, GateConfig{})
	c, err := net.DialTimeout("tcp", p.addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(c).Encode(acceptanceRecord("plaintext"))
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err == nil && reply.Status == "pending" {
		t.Fatal("plaintext request admitted")
	}
}
