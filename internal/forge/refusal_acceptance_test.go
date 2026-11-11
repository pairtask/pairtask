package forge_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
)

// A refused lookup says which refusal, because the fix differs: no token
// here, a token the code host rejected, or a token that may not read this
// repository.
func TestRefusalAcceptanceRefusedfCarriesClassAndReason(t *testing.T) {
	for _, reason := range []forge.Reason{forge.ReasonNoToken, forge.ReasonTokenRejected, forge.ReasonRepository} {
		t.Run(string(reason), func(t *testing.T) {
			err := forge.Refusedf(reason, "refused %s", "detail")
			if err == nil {
				t.Fatal("Refusedf returned nil")
			}
			if got := forge.ClassOf(err); got != forge.ClassForbidden {
				t.Fatalf("class = %q; a refusal is forbidden", got)
			}
			if got := forge.ReasonOf(err); got != reason {
				t.Fatalf("reason = %q; want %q", got, reason)
			}
			if !strings.Contains(err.Error(), "refused detail") {
				t.Fatalf("message lost its formatted text: %q", err.Error())
			}
		})
	}
}

func TestRefusalAcceptanceReasonsAreDistinct(t *testing.T) {
	seen := map[forge.Reason]bool{}
	for _, reason := range []forge.Reason{forge.ReasonNoToken, forge.ReasonTokenRejected, forge.ReasonRepository} {
		if reason == "" {
			t.Fatal("a refusal reason is empty, so it reads as no reason")
		}
		if seen[reason] {
			t.Fatalf("reason %q is used twice", reason)
		}
		seen[reason] = true
	}
}

func TestRefusalAcceptanceReasonSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("resolve: %w", forge.Refusedf(forge.ReasonTokenRejected, "rejected"))
	if got := forge.ReasonOf(err); got != forge.ReasonTokenRejected {
		t.Fatalf("wrapped reason = %q; want %q", got, forge.ReasonTokenRejected)
	}
	if got := forge.ClassOf(err); got != forge.ClassForbidden {
		t.Fatalf("wrapped class = %q; want forbidden", got)
	}
}

func TestRefusalAcceptanceErrorsWithoutReasonReadEmpty(t *testing.T) {
	for name, err := range map[string]error{
		"plain forbidden":   forge.Errorf(forge.ClassForbidden, "forbidden"),
		"other class":       forge.Errorf(forge.ClassNotFound, "missing"),
		"not a forge error": errors.New("plain"),
		"nil":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			if got := forge.ReasonOf(err); got != "" {
				t.Fatalf("reason = %q; want none", got)
			}
		})
	}
	if got := forge.ClassOf(forge.Errorf(forge.ClassForbidden, "forbidden")); got != forge.ClassForbidden {
		t.Fatalf("Errorf class changed: %q", got)
	}
}
