package chat

import (
	"testing"
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
