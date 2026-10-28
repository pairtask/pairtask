package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPeerMessagesAcceptanceFailedInsertRollsBackSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	s := db.PeerMessages()
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	defer raw.Close()
	_, err = raw.Exec("CREATE TRIGGER fail_message BEFORE INSERT ON peer_messages BEGIN SELECT RAISE(ABORT, 'injected message failure'); END")
	pairOK(t, err)
	_, err = s.Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "failed", Text: "text", Now: messageAt})
	if err == nil {
		t.Fatal("queue succeeded without durable insertion")
	}
	answer, err := s.Accept(context.Background(), contact, messageWire("failed-in", 1), messageAt)
	if err == nil || answer.Stored {
		t.Fatal("Stored returned for failed durable insertion")
	}
	_, err = raw.Exec("DROP TRIGGER fail_message")
	pairOK(t, err)
	next, err := s.Queue(context.Background(), messaging.Outgoing{Contact: contact, ID: "first", Text: "text", Now: messageAt})
	pairOK(t, err)
	pairEqual(t, next.N, uint64(1))
	pairEqual(t, len(messagePage(t, s, contact)), 1)
}

func TestPeerMessagesAcceptanceConcurrentQueueEnforcesQuota(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	for i := 0; i < 63; i++ {
		_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: fmt.Sprintf("seed-%d", i), Text: "text", Now: messageAt})
		pairOK(t, err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: fmt.Sprintf("race-%d", n), Text: "text", Now: messageAt})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	pairEqual(t, messageQueueSuccesses(t, results), 1)
	rows := messagePage(t, s, contact)
	pairEqual(t, len(rows), 64)
	messageUniqueSequences(t, rows)
}

func messageQueueSuccesses(t *testing.T, results <-chan error) int {
	t.Helper()
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, messaging.ErrOutboxFull) {
			t.Fatal(err)
		}
	}
	return successes
}

func messageUniqueSequences(t *testing.T, rows []messaging.Row) {
	t.Helper()
	seen := map[uint64]bool{}
	for _, row := range rows {
		if seen[row.N] {
			t.Fatalf("sequence reused: %d", row.N)
		}
		seen[row.N] = true
	}
}

func TestPeerMessagesAcceptanceExpiryUsesChronologicalSubseconds(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "old", Text: "text", Now: messageAt})
	pairOK(t, err)
	// The cutoff is half a second after creation. RFC3339Nano strings are
	// not lexically ordered when one timestamp omits fractional seconds.
	rows, err := s.Offer(ctx, contact, messageAt.Add(messaging.QueuedExpiry+500*time.Millisecond), messaging.Limit{Count: 16, Bytes: 512 << 10})
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	pairEqual(t, messagePage(t, s, contact)[0].State, messaging.StateExpired)
}

func TestPeerMessagesAcceptanceOfferClampsAndPreservesOrder(t *testing.T) {
	db, contact := messageFixture(t)
	s := db.PeerMessages()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: fmt.Sprintf("out-%d", i), Text: "1234", Now: messageAt})
		pairOK(t, err)
	}
	rows, err := s.Offer(ctx, contact, messageAt, messaging.Limit{Count: 1000, Bytes: 1 << 30})
	pairOK(t, err)
	pairEqual(t, len(rows), messaging.SessionMessages)
	for i, row := range rows {
		pairEqual(t, row.N, uint64(i+1))
	}
	rows, err = s.Offer(ctx, contact, messageAt, messaging.Limit{Count: 16, Bytes: 7})
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	rows, err = s.Offer(ctx, contact, messageAt, messaging.Limit{Count: -1, Bytes: 100})
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	rows, err = s.Offer(ctx, contact, messageAt, messaging.Limit{Count: 16, Bytes: -1})
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
}
