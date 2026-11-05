package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
	"strings"
	"testing"
	"time"
)

func consentFillBytes(t *testing.T, f workspaceStoreFixture, agent string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		m := consentMessage(fmt.Sprintf("byte-fill-%d", i), uint64(i+1), agent)
		m.Text = strings.Repeat("x", messaging.TextBytes)
		consentHeld(t, f, m, messageAt.Add(time.Duration(i)*time.Minute))
	}
}
func consentAskByteFixture(t *testing.T, global bool) workspaceStoreFixture {
	t.Helper()
	f := consentFixture(t, "last-agent", "reviewer")
	if global {
		for i := 0; i < 7; i++ {
			agent := fmt.Sprintf("full-agent-%d", i)
			other := consentAnotherContact(t, f, i+2, agent)
			consentFillBytes(t, other, agent, 16)
		}
	}
	consentFillBytes(t, f, "last-agent", 15)
	_, err := f.db.PeerMessages().SetReviewGrant(context.Background(), f.key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAsk, Now: messageAt})
	pairOK(t, err)
	return f
}
func consentSizedAsk(t *testing.T, over int) messaging.Message {
	t.Helper()
	m := remoteAsk("legacy-ask-at-byte-bound")
	m.N = 30
	m.Review.Publish = false
	m.Review.Formal = false
	encoded, err := json.Marshal(m.Review)
	pairOK(t, err)
	m.Text = strings.Repeat("a", messaging.TextBytes-len(encoded)+over)
	pairEqual(t, messaging.ValidText(m.Text), true)
	return m
}
