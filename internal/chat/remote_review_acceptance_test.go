package chat

import (
	"reflect"
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
	for reviewer, want := range map[string]string{"agent": "agent", "@agent": "agent", "@@literal": "@literal"} {
		msg, problem := reviewRequestFor(pairWords{"review", reviewer, "pairtask/pairtask#235", "base", "head", "scope", "@other", "--publish"}, "contact")
		if problem != "" {
			t.Fatal(problem)
		}
		if !reflect.DeepEqual(msg.ContactTo, []string{want}) {
			t.Fatalf("reviewer changed: %v", msg.ContactTo)
		}
		if msg.ContactOp != protocol.ContactOpReviewAsk {
			t.Fatal("missing ask operation")
		}
		assertRemoteChatAsk(t, msg.ReviewAsk)
	}
}

func assertRemoteChatAsk(t *testing.T, ask *protocol.ReviewAsk) {
	t.Helper()
	if ask == nil {
		t.Fatal("missing ask")
	}
	got := []string{ask.Base, ask.Head, ask.Scope}
	if !reflect.DeepEqual(got, []string{"base", "head", "scope @other"}) {
		t.Fatalf("changed scope/pins: %v", got)
	}
	if ask.Publish != true {
		t.Fatal("publication permission lost")
	}
	if ask.Formal != false {
		t.Fatal("formal permission added")
	}
}
