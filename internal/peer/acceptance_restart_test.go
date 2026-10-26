package peer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// acceptanceJournal is an independent test adapter for the gate's persistence
// contract. It syncs successful mutations and reconstructs a fresh MemStore on
// reopen. It is not the production store or a proposed database implementation:
// these tests do not simulate torn journal writes or storage-device failure.
type acceptanceJournal struct {
	*MemStore
	mu   sync.Mutex
	file *os.File
}

type acceptanceJournalEntry struct {
	Operation string
	Request   Request
	Contact   ContactRef
	Decision  Kind
}

func acceptanceOpenJournal(t *testing.T, path string) *acceptanceJournal {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	acceptanceNoError(t, err)
	t.Cleanup(func() { f.Close() })
	s := &acceptanceJournal{MemStore: NewMemStore(), file: f}
	decoder := json.NewDecoder(f)
	for {
		var entry acceptanceJournalEntry
		err := decoder.Decode(&entry)
		if errors.Is(err, io.EOF) {
			break
		}
		acceptanceNoError(t, err)
		if _, err := s.applyEntry(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *acceptanceJournal) SaveRequest(ctx context.Context, req Request) (Outcome, error) {
	return s.mutate(ctx, acceptanceJournalEntry{Operation: "request", Request: req})
}

func (s *acceptanceJournal) Accept(ctx context.Context, ref ContactRef) (Outcome, error) {
	return s.mutate(ctx, acceptanceJournalEntry{Operation: "accept", Contact: ref})
}
func (s *acceptanceJournal) Ignore(ctx context.Context, ref ContactRef) (Outcome, error) {
	return s.mutate(ctx, acceptanceJournalEntry{Operation: "ignore", Contact: ref})
}
func (s *acceptanceJournal) SaveOutbound(ctx context.Context, ref ContactRef) (Outcome, error) {
	return s.mutate(ctx, acceptanceJournalEntry{Operation: "outbound", Contact: ref})
}
func (s *acceptanceJournal) Settle(ctx context.Context, ref ContactRef, decision Kind) (Outcome, error) {
	return s.mutate(ctx, acceptanceJournalEntry{Operation: "settle", Contact: ref, Decision: decision})
}

func (s *acceptanceJournal) applyEntry(ctx context.Context, entry acceptanceJournalEntry) (Outcome, error) {
	switch entry.Operation {
	case "request":
		return s.MemStore.SaveRequest(ctx, entry.Request)
	case "accept":
		return s.MemStore.Accept(ctx, entry.Contact)
	case "ignore":
		return s.MemStore.Ignore(ctx, entry.Contact)
	case "outbound":
		return s.MemStore.SaveOutbound(ctx, entry.Contact)
	case "settle":
		return s.MemStore.Settle(ctx, entry.Contact, entry.Decision)
	default:
		return Outcome{}, errors.New("unknown fixture journal operation")
	}
}

func (s *acceptanceJournal) mutate(ctx context.Context, entry acceptanceJournalEntry) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.applyEntry(ctx, entry)
	if err != nil || out.Existing {
		return out, err
	}
	if err := json.NewEncoder(s.file).Encode(entry); err != nil {
		return Outcome{}, err
	}
	if err := s.file.Sync(); err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func TestAcceptanceLocalDecisionSurvivesRestart(t *testing.T) {
	for _, decision := range []string{StatusAccepted, StatusIgnored} {
		t.Run(decision, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "decisions.jsonl")
			store := acceptanceOpenJournal(t, path)
			p := acceptanceStart(t, GateConfig{Store: store})
			cert := acceptanceCertificate(t, "alice")
			record := acceptanceRecord("local-decision-restart")
			acceptanceStatus(t, acceptanceSend(t, p.connect(t, cert), record), StatusPending)
			fp := acceptanceFingerprint(t, cert)
			var err error
			if decision == StatusAccepted {
				_, err = store.Accept(context.Background(), ContactRef{Fingerprint: fp, RequestID: record.RequestID})
			} else {
				_, err = store.Ignore(context.Background(), ContactRef{Fingerprint: fp, RequestID: record.RequestID})
			}
			acceptanceNoError(t, err)
			p.stop()
			store.file.Close()
			reopened := acceptanceOpenJournal(t, path)
			state, ok, err := reopened.Get(context.Background(), ContactRef{Fingerprint: fp, RequestID: record.RequestID})
			acceptanceNoError(t, err)
			acceptanceEqual(t, ok, true)
			acceptanceEqual(t, state.Status, decision)
			resumed := acceptanceStart(t, GateConfig{Cert: p.serverCert, Store: reopened})
			reply := acceptanceSend(t, resumed.connect(t, cert), record)
			acceptanceStatus(t, reply, StatusDuplicate)
			if reply.Reason != decision {
				t.Fatalf("retry did not report persisted decision: %+v", reply)
			}
			if len(reopened.Snapshot()) != 1 {
				t.Fatal("decision retry created an extra contact record")
			}
		})
	}
}

func TestAcceptanceOutboundSettlementSurvivesFaultAndRestart(t *testing.T) {
	for _, point := range []string{"before-persist", "after-persist", "before-reply", "after-reply"} {
		t.Run(point, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outbound.jsonl")
			store := acceptanceOpenJournal(t, path)
			cert := acceptanceCertificate(t, "alice")
			fp := acceptanceFingerprint(t, cert)
			if _, err := store.SaveOutbound(context.Background(), ContactRef{Fingerprint: fp, RequestID: "outbound-restart"}); err != nil {
				t.Fatal(err)
			}
			hooks, hit := acceptanceFaultAt(t, point)
			p := acceptanceStart(t, GateConfig{Store: store, Faults: hooks})
			record := acceptanceRecord("outbound-restart")
			record.Kind = KindAccept
			acceptanceFaultRequest{Client: cert, Record: record, Point: point}.disconnect(t, p)
			store.file.Close()
			if !hit.Load() {
				t.Fatal("settlement hook not reached")
			}
			reopened := acceptanceOpenJournal(t, path)
			want := StatusAccepted
			if point == "before-persist" {
				want = StatusPending
			}
			if got := reopened.Snapshot()["out/"+fp+"/outbound-restart"]; got != want {
				t.Fatalf("persisted settlement=%s,want %s", got, want)
			}
			resumed := acceptanceStart(t, GateConfig{Cert: p.serverCert, Store: reopened, Discoverable: func() bool { return false }})
			want = StatusDuplicate
			if point == "before-persist" {
				want = StatusAccepted
			}
			acceptanceStatus(t, acceptanceSend(t, resumed.connect(t, cert), record), want)
			if state := reopened.Snapshot(); len(state) != 1 || state["out/"+fp+"/outbound-restart"] != StatusAccepted {
				t.Fatalf("settlement did not converge: %+v", state)
			}
		})
	}
}

func TestAcceptanceRestartAcrossPersistenceAndReplyFaults(t *testing.T) {
	for _, point := range []string{"before-persist", "after-persist", "before-reply", "after-reply"} {
		t.Run(point, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requests.jsonl")
			store := acceptanceOpenJournal(t, path)
			faults, hit := acceptanceFaultAt(t, point)
			server, client := acceptanceCertificate(t, "gate"), acceptanceCertificate(t, "alice")
			p := acceptanceStart(t, GateConfig{Cert: server, Store: store, Faults: faults})
			record := acceptanceRecord("survives-restart")
			acceptanceFaultRequest{Client: client, Record: record, Point: point}.disconnect(t, p)
			if !hit.Load() {
				t.Fatal("fault hook was not reached")
			}
			if err := store.file.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := acceptanceOpenJournal(t, path)
			fp := acceptanceFingerprint(t, client)
			_, exists, err := reopened.Get(context.Background(), ContactRef{Fingerprint: fp, RequestID: record.RequestID})
			acceptanceNoError(t, err)
			wantPersisted := point != "before-persist"
			if exists != wantPersisted {
				t.Fatalf("durable request exists=%v, want %v at %s", exists, wantPersisted, point)
			}
			resumed := acceptanceStart(t, GateConfig{Cert: server, Store: reopened})
			want := StatusPending
			if wantPersisted {
				want = StatusDuplicate
			}
			acceptanceStatus(t, acceptanceSend(t, resumed.connect(t, client), record), want)
			if count := len(reopened.Snapshot()); count != 1 {
				t.Fatalf("restart/retry produced %d records", count)
			}
			record.Note = "changed after restart"
			acceptanceStatus(t, acceptanceSend(t, resumed.connect(t, client), record), StatusRefused)
		})
	}
}

func acceptanceFaultAt(t *testing.T, point string) (*Faults, *atomic.Bool) {
	t.Helper()
	hit := &atomic.Bool{}
	fault := func(Record) error { hit.Store(true); return errors.New("injected disconnect") }
	hooks := &Faults{}
	switch point {
	case "before-persist":
		hooks.BeforePersist = fault
	case "after-persist":
		hooks.AfterPersist = fault
	case "before-reply":
		hooks.BeforeReply = fault
	case "after-reply":
		hooks.AfterReply = fault
	default:
		t.Fatalf("unknown fault point %s", point)
	}
	return hooks, hit
}

type acceptanceFaultRequest struct {
	Client tls.Certificate
	Record Record
	Point  string
}

func (r acceptanceFaultRequest) disconnect(t *testing.T, p *acceptanceProbe) {
	t.Helper()
	c := p.connect(t, r.Client)
	acceptanceNoError(t, json.NewEncoder(c).Encode(r.Record))
	var reply Reply
	err := json.NewDecoder(c).Decode(&reply)
	if r.Point != "after-reply" && err == nil {
		t.Fatalf("request acknowledged across %s fault: %+v", r.Point, reply)
	}
	c.Close()
	p.stop()
}
