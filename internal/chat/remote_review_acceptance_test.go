package chat

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteReviewChatAcceptanceFlagsCannotReplaceArguments(t *testing.T) {
	for _, fields := range []pairWords{
		{"reviews", "--publish", "--formal"},
		{"approve", "--publish"},
		{"decline", "--formal"},
	} {
		t.Run(fields[0], func(t *testing.T) {
			// These commands satisfy the outer raw arity guard, but policy flags
			// must never become positional operands or cause an index panic.
			handled, valid := fields.reviewArity()
			if !handled || !valid {
				t.Fatal("fixture no longer reaches command parser")
			}
			_, problem := reviewRequestFor(fields, "contact")
			if problem == "" {
				t.Fatal("missing positional operands accepted")
			}
		})
	}
}

func TestRemoteReviewChatAcceptanceAliasPinsAndPolicy(t *testing.T) {
	for _, reviewer := range []string{"agent", "@agent", "@@literal"} {
		msg, problem := reviewRequestFor(pairWords{"review", reviewer, "pairtask/pairtask#235", "base", "head", "scope", "@other", "--publish"}, "contact")
		if problem != "" {
			t.Fatal(problem)
		}
		want := "agent"
		if reviewer == "@@literal" {
			want = "@literal"
		}
		if len(msg.ContactTo) != 1 || msg.ContactTo[0] != want {
			t.Fatalf("reviewer changed: %v", msg.ContactTo)
		}
		if msg.ContactOp != protocol.ContactOpReviewAsk || msg.ReviewAsk == nil {
			t.Fatal("missing ask")
		}
		if msg.ReviewAsk.Base != "base" || msg.ReviewAsk.Head != "head" || msg.ReviewAsk.Scope != "scope @other" {
			t.Fatalf("changed review scope/pins: %+v", msg.ReviewAsk)
		}
		if !msg.ReviewAsk.Publish || msg.ReviewAsk.Formal {
			t.Fatal("publication policy changed")
		}
	}
}
