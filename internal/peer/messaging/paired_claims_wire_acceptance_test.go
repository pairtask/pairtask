package messaging_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func claimsWireNotice() messaging.ClaimNotice {
	return messaging.ClaimNotice{MessageID: "subject", Agent: "worker", Role: "owner", Task: "investigate", State: "active", Seq: 1, At: time.Date(2026, 11, 19, 14, 0, 0, 0, time.UTC)}
}

func TestPairedClaimsWireAcceptanceRoundTripAndFraming(t *testing.T) {
	for _, state := range []string{"active", "released", "done"} {
		n := claimsWireNotice()
		n.State = state
		wire, err := messaging.EncodeClaimNotice(n)
		if err != nil {
			t.Fatal(err)
		}
		got, err := messaging.DecodeClaimNotice(wire)
		if err != nil || !reflect.DeepEqual(got, n) {
			t.Fatalf("notice changed: %+v %v", got, err)
		}
		for length := 0; length < len(wire); length++ {
			if _, err := messaging.DecodeClaimNotice(wire[:length]); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("truncated notice accepted at %d: %v", length, err)
			}
		}
		if _, err := messaging.DecodeClaimNotice(append(wire, 0)); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatal("trailing data accepted")
		}
	}
}

func TestPairedClaimsWireAcceptanceInvalidValuesRefused(t *testing.T) {
	changes := map[string]func(*messaging.ClaimNotice){
		"empty-id":    func(n *messaging.ClaimNotice) { n.MessageID = "" },
		"long-id":     func(n *messaging.ClaimNotice) { n.MessageID = strings.Repeat("x", 129) },
		"empty-agent": func(n *messaging.ClaimNotice) { n.Agent = "" },
		"long-agent":  func(n *messaging.ClaimNotice) { n.Agent = strings.Repeat("x", 129) },
		"role":        func(n *messaging.ClaimNotice) { n.Role = "operator" },
		"task":        func(n *messaging.ClaimNotice) { n.Task = "bad task" },
		"state":       func(n *messaging.ClaimNotice) { n.State = "approved" },
		"sequence":    func(n *messaging.ClaimNotice) { n.Seq = 0 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			n := claimsWireNotice()
			change(&n)
			if _, err := messaging.EncodeClaimNotice(n); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("invalid notice encoded: %v", err)
			}
		})
	}
}

func TestPairedClaimsWireAcceptanceRepliesBindCompleteIdentity(t *testing.T) {
	for _, reason := range []string{"", "unknown_message", "not_recipient", "stale"} {
		r := messaging.ClaimReply{MessageID: "subject", Agent: "worker", Role: "reviewer", Task: "review", Seq: 9, Reason: reason}
		wire, err := messaging.EncodeClaimReply(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := messaging.DecodeClaimReply(wire)
		if err != nil || got != r {
			t.Fatalf("claim reply identity changed: %+v %v", got, err)
		}
	}
}
