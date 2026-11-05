package messaging_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"io"
	"testing"
	"time"
)

type consentDurableOutcomeStore interface {
	HeldBy(context.Context, string, messaging.Held, time.Time) error
	AcceptConsentOutcome(context.Context, string, messaging.ConsentOutcome, time.Time) error
}

func consentOutcomeDurableStore(t *testing.T, s messaging.Store) consentDurableOutcomeStore {
	t.Helper()
	cs, ok := s.(consentDurableOutcomeStore)
	if !ok {
		t.Fatal("durable consent outcome methods are not implemented")
	}
	return cs
}
func consentOutcomeTableCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	sessionOK(t, err)
	defer raw.Close()
	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	sessionOK(t, err)
	var names []string
	for rows.Next() {
		var name string
		sessionOK(t, rows.Scan(&name))
		names = append(names, name)
	}
	sessionOK(t, rows.Err())
	rows.Close()
	counts := map[string]int{}
	for _, name := range names {
		var n int
		sessionOK(t, raw.QueryRow(`SELECT COUNT(*) FROM "`+name+`"`).Scan(&n))
		counts[name] = n
	}
	return counts
}

func TestConsentOutcomeDurableAcceptanceLostAckReplaysAcrossRestart(t *testing.T) {
	db, path := sessionFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "request", Text: "body", Now: sessionAt})
	sessionOK(t, err)
	cs := consentOutcomeDurableStore(t, db.PeerMessages())
	sessionOK(t, cs.HeldBy(ctx, sessionContact, messaging.Held{ID: "request", HeldID: "hold"}, sessionAt))
	transcript := func() [][]byte {
		return append(consentOutcomePrefix(t, 8), consentRawFrame(24, "request", "declined", "denied"), routingSessionEnd(t))
	}
	lost := &scriptFrames{input: transcript(), failKind: 25}
	result, _, err := sessionSide(lost, db.PeerMessages()).OpenDial(ctx)
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	sessionEqual(t, result.Moved(), true)
	before := consentOutcomeTableCounts(t, path)
	sessionOK(t, db.Close())
	db = sessionDB(t, path)
	replay := &scriptFrames{input: transcript()}
	_, _, err = sessionSide(replay, db.PeerMessages()).OpenDial(ctx)
	sessionOK(t, err)
	acknowledged := false
	for _, frame := range replay.sent {
		kind, err := messaging.Kind(frame)
		sessionOK(t, err)
		if kind == 25 {
			ack, err := messaging.DecodeConsentOutcomeStored(frame)
			sessionOK(t, err)
			sessionEqual(t, ack.ID, "request")
			acknowledged = true
		}
	}
	if !acknowledged {
		t.Fatal("durably replayed outcome was not acknowledged")
	}
	after := consentOutcomeTableCounts(t, path)
	for table, n := range before {
		sessionEqual(t, after[table], n)
	}
}

func TestConsentOutcomeDurableAcceptanceUnheldRequestCannotReceiveOutcome(t *testing.T) {
	db, _ := sessionFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "never-held", Text: "body", Now: sessionAt})
	sessionOK(t, err)
	sessionOK(t, db.PeerMessages().Delivered(ctx, sessionContact, "never-held", sessionAt))
	cs := consentOutcomeDurableStore(t, db.PeerMessages())
	err = cs.AcceptConsentOutcome(ctx, sessionContact, messaging.ConsentOutcome{ID: "never-held", State: "allowed"}, sessionAt)
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("unsolicited consent outcome accepted: %v", err)
	}
}
