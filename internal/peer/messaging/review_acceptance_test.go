package messaging_test

import (
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func reviewWireFixture() messaging.Message {
	m := routedFixture()
	m.To, m.ReplyTo = []string{"reviewer/@\nname"}, messaging.ReplyRef{}
	m.Review = &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "235", Scope: "security", Base: "base", Head: "head", Publish: true, Formal: true}
	return m
}

func TestReviewWireAcceptanceRoundTripAndTruncation(t *testing.T) {
	m := reviewWireFixture()
	wire, err := messaging.EncodeReviewAsk(m)
	sessionOK(t, err)
	decoded, err := messaging.DecodeReviewAsk(wire)
	sessionOK(t, err)
	if !reflect.DeepEqual(decoded, m) {
		t.Fatalf("changed request: %#v", decoded)
	}
	for n := 0; n < len(wire); n++ {
		if _, err := messaging.DecodeReviewAsk(wire[:n]); err == nil {
			t.Fatalf("accepted truncation at %d", n)
		}
	}
	if _, err := messaging.DecodeReviewAsk(append(wire, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
}

func TestReviewWireAcceptanceHashBindsEntireAsk(t *testing.T) {
	base := reviewWireFixture()
	for field, change := range map[string]func(*messaging.ReviewAsk){
		"forge":   func(a *messaging.ReviewAsk) { a.Forge = "other" },
		"repo":    func(a *messaging.ReviewAsk) { a.Repository = "other/repo" },
		"change":  func(a *messaging.ReviewAsk) { a.Change = "236" },
		"scope":   func(a *messaging.ReviewAsk) { a.Scope = "other" },
		"base":    func(a *messaging.ReviewAsk) { a.Base = "other" },
		"head":    func(a *messaging.ReviewAsk) { a.Head = "other" },
		"publish": func(a *messaging.ReviewAsk) { a.Publish = false },
		"formal":  func(a *messaging.ReviewAsk) { a.Formal = false },
	} {
		t.Run(field, func(t *testing.T) {
			m, ask := base, *base.Review
			m.Review = &ask
			change(&ask)
			if m.Hash() == base.Hash() {
				t.Fatal("ask mutation did not change dedup identity")
			}
		})
	}
}

func TestReviewWireAcceptanceRequiresPinsAndSingleReviewer(t *testing.T) {
	for _, change := range []func(*messaging.Message){
		func(m *messaging.Message) { m.Review.Base = "" },
		func(m *messaging.Message) { m.Review.Head = "" },
		func(m *messaging.Message) { m.To = []string{"one", "two"} },
		func(m *messaging.Message) {
			m.ReplyTo = messaging.ReplyRef{ID: "parent", Author: messaging.ReplyBySender}
		},
	} {
		m := reviewWireFixture()
		change(&m)
		if messaging.ValidReviewAsk(m) {
			t.Fatal("accepted unpinned or ambiguous review ask")
		}
	}
}
