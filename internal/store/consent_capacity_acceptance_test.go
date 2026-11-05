package store_test

import (
	"context"
	"fmt"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
	"strings"
	"testing"
	"time"
)

func TestConsentCapacityAcceptancePerContactBoundDoesNotChargeRejectedRequest(t *testing.T) {
	agents := []string{"a", "b", "c", "d", "e"}
	f := consentFixture(t, agents...)
	for i := 0; i < store.ConsentPerContact; i++ {
		consentHeld(t, f, consentMessage(fmt.Sprintf("contact-%d", i), uint64(i+1), agents[i/16]), messageAt.Add(time.Duration(i)*time.Minute))
	}
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("contact-overflow", 65, "e"), messageAt.Add(65*time.Minute))
	if err == nil && a.Held {
		t.Fatal("contact count bound admitted 65th hold")
	}
	consentUnchanged(t, f, before)
}

func TestConsentCapacityAcceptancePerContactBytesUsesUTF8Bytes(t *testing.T) {
	f := consentFixture(t, "a", "b")
	for i := 0; i < 16; i++ {
		m := consentMessage(fmt.Sprintf("bytes-%d", i), uint64(i+1), "a")
		m.Text = strings.Repeat("é", messaging.TextBytes/2)
		consentHeld(t, f, m, messageAt.Add(time.Duration(i)*time.Minute))
	}
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("bytes-overflow", 17, "b"), messageAt.Add(17*time.Minute))
	if err == nil && a.Held {
		t.Fatal("contact byte bound counted runes instead of UTF-8 bytes")
	}
	consentUnchanged(t, f, before)
}

func TestConsentCapacityAcceptanceGlobalCountBoundAcrossContacts(t *testing.T) {
	f := consentFixture(t, "reserve")
	for contact := 0; contact < 4; contact++ {
		agents := []string{}
		for i := 0; i < 4; i++ {
			agents = append(agents, fmt.Sprintf("agent-%d-%d", contact, i))
		}
		current := consentAnotherContact(t, f, contact+2, agents...)
		for i := 0; i < 64; i++ {
			consentHeld(t, current, consentMessage(fmt.Sprintf("global-%d-%d", contact, i), uint64(i+1), agents[i/16]), messageAt.Add(time.Duration(i)*time.Minute))
		}
	}
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("global-overflow", 1, "reserve"), messageAt.Add(70*time.Minute))
	if err == nil && a.Held {
		t.Fatal("global count bound admitted 257th hold")
	}
	consentUnchanged(t, f, before)
}

func TestConsentCapacityAcceptanceGlobalBytesAcrossContacts(t *testing.T) {
	f := consentFixture(t, "reserve")
	for contact := 0; contact < 8; contact++ {
		agent := fmt.Sprintf("byte-agent-%d", contact)
		current := consentAnotherContact(t, f, contact+2, agent)
		for i := 0; i < 16; i++ {
			m := consentMessage(fmt.Sprintf("global-bytes-%d-%d", contact, i), uint64(i+1), agent)
			m.Text = strings.Repeat("x", messaging.TextBytes)
			consentHeld(t, current, m, messageAt.Add(time.Duration(i)*time.Minute))
		}
	}
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, consentMessage("global-byte-overflow", 1, "reserve"), messageAt.Add(20*time.Minute))
	if err == nil && a.Held {
		t.Fatal("global byte bound admitted content past 2MiB")
	}
	consentUnchanged(t, f, before)
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 10)
	pairOK(t, err)
	pairEqual(t, page.Total, 0)
}
