package messaging_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"testing"
	"time"
)

func consentOrderingSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	sessionOK(t, err)
	defer raw.Close()
	counts := consentOutcomeTableCounts(t, path)
	out := map[string]string{}
	for name := range counts {
		rows, err := raw.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
		sessionOK(t, err)
		columns, err := rows.Columns()
		sessionOK(t, err)
		var data [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			sessionOK(t, rows.Scan(pointers...))
			data = append(data, values)
		}
		sessionOK(t, rows.Err())
		rows.Close()
		encoded, err := json.Marshal(data)
		sessionOK(t, err)
		out[name] = string(encoded)
	}
	return out
}

func TestConsentOrderingAcceptanceTerminalSurvivesReconnectAndOldHeld(t *testing.T) {
	for _, state := range []string{"allowed", "declined", "expired", "withheld"} {
		t.Run(state, func(t *testing.T) {
			db, path := sessionFixture(t)
			ctx := context.Background()
			_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "immutable-request", Text: "body", Now: sessionAt})
			sessionOK(t, err)
			cs := consentOutcomeDurableStore(t, db.PeerMessages())
			held := messaging.Held{ID: "immutable-request", HeldID: "original-hold"}
			sessionOK(t, cs.HeldBy(ctx, sessionContact, held, sessionAt))
			reason := ""
			switch state {
			case "declined":
				reason = "denied"
			case "expired":
				reason = "expired"
			case "withheld":
				reason = "session_ended"
			}
			outcome := messaging.ConsentOutcome{ID: held.ID, State: state, Reason: reason}
			sessionOK(t, cs.AcceptConsentOutcome(ctx, sessionContact, outcome, sessionAt))
			before := consentOrderingSnapshot(t, path)
			sessionOK(t, db.Close())
			db = sessionDB(t, path)
			cs = consentOutcomeDurableStore(t, db.PeerMessages())
			err = cs.HeldBy(ctx, sessionContact, held, sessionAt.Add(time.Hour))
			if err != nil && !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("old Held failed unexpectedly: %v", err)
			}
			sessionOK(t, cs.AcceptConsentOutcome(ctx, sessionContact, outcome, sessionAt.Add(time.Hour)))
			after := consentOrderingSnapshot(t, path)
			for table, want := range before {
				sessionEqual(t, after[table], want)
			}
			conflicting := outcome
			if state == "allowed" {
				conflicting.State = "declined"
				conflicting.Reason = "denied"
			} else {
				conflicting.State = "allowed"
				conflicting.Reason = ""
			}
			err = cs.AcceptConsentOutcome(ctx, sessionContact, conflicting, sessionAt.Add(2*time.Hour))
			if !errors.Is(err, messaging.ErrConsentTerminalConflict) {
				t.Fatalf("conflicting terminal outcome not rejected: %v", err)
			}
			after = consentOrderingSnapshot(t, path)
			for table, want := range before {
				sessionEqual(t, after[table], want)
			}
			offers, err := db.PeerMessages().Offer(ctx, sessionContact, sessionAt.Add(2*time.Hour), messaging.SessionLimit)
			sessionOK(t, err)
			sessionEqual(t, len(offers), 0)
		})
	}
}
