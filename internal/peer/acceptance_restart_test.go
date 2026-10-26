package peer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// acceptanceJournal is an independent test adapter for inbound request
// persistence. It syncs each request before reporting success and reconstructs
// a fresh MemStore on reopen. It does not persist decisions or outbound requests
// and is not the production broker store or a proposed database implementation.
type acceptanceJournal struct {
	*MemStore
	mu   sync.Mutex
	file *os.File
}

func acceptanceOpenJournal(t *testing.T, path string) *acceptanceJournal {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s := &acceptanceJournal{MemStore: NewMemStore(), file: f}
	decoder := json.NewDecoder(f)
	for {
		var req Request
		err := decoder.Decode(&req)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.MemStore.SaveRequest(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (s *acceptanceJournal) SaveRequest(ctx context.Context, req Request) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, err := s.MemStore.SaveRequest(ctx, req)
	if err != nil || out.Existing {
		return out, err
	}
	if err := json.NewEncoder(s.file).Encode(req); err != nil {
		return Outcome{}, err
	}
	if err := s.file.Sync(); err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func TestAcceptanceRestartAcrossPersistenceAndReplyFaults(t *testing.T) {
	for _, point := range []string{"before-persist", "after-persist", "before-reply", "after-reply"} {
		t.Run(point, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requests.jsonl")
			store := acceptanceOpenJournal(t, path)
			var hit atomic.Bool
			fault := func(Record) error { hit.Store(true); return errors.New("injected disconnect") }
			faults := &Faults{}
			switch point {
			case "before-persist":
				faults.BeforePersist = fault
			case "after-persist":
				faults.AfterPersist = fault
			case "before-reply":
				faults.BeforeReply = fault
			case "after-reply":
				faults.AfterReply = fault
			}
			server, client := acceptanceCertificate(t, "gate"), acceptanceCertificate(t, "alice")
			p := acceptanceStart(t, GateConfig{Cert: server, Store: store, Faults: faults})
			c := p.connect(t, client)
			record := acceptanceRecord("survives-restart")
			if err := json.NewEncoder(c).Encode(record); err != nil {
				t.Fatal(err)
			}
			var reply Reply
			err := json.NewDecoder(c).Decode(&reply)
			if point != "after-reply" && err == nil {
				t.Fatalf("request acknowledged across %s fault: %+v", point, reply)
			}
			c.Close()
			p.stop()
			if !hit.Load() {
				t.Fatal("fault hook was not reached")
			}
			if err := store.file.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := acceptanceOpenJournal(t, path)
			fp := acceptanceFingerprint(t, client)
			_, exists, err := reopened.Get(context.Background(), fp, record.RequestID)
			if err != nil {
				t.Fatal(err)
			}
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
