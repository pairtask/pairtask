package messaging_test

import (
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPairedClaimsSequenceAcceptanceFitsDurableInteger(t *testing.T) {
	n := claimsWireNotice()
	n.Seq = 1<<63 - 1
	if _, err := messaging.EncodeClaimNotice(n); err != nil {
		t.Fatal(err)
	}
	n.Seq++
	if _, err := messaging.EncodeClaimNotice(n); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("unstoreable notice sequence accepted: %v", err)
	}
	r := messaging.ClaimReply{MessageID: n.MessageID, Agent: n.Agent, Role: n.Role, Task: n.Task, Seq: n.Seq, Reason: "full"}
	if _, err := messaging.EncodeClaimReply(r); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("unstoreable reply sequence accepted: %v", err)
	}
	r.Seq = 1
	wire, err := messaging.EncodeClaimReply(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := messaging.DecodeClaimReply(wire)
	if err != nil || got != r {
		t.Fatalf("full reply changed: %+v %v", got, err)
	}
}
