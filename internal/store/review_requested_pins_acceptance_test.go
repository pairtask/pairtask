package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestRequestedPinsAcceptanceViewKeepsOriginalAskInEveryState(t *testing.T) {
	base, head := strings.Repeat("1", 40), strings.Repeat("2", 40)
	for _, state := range []string{"asked", "requested", "accepted", "declined", "completed"} {
		t.Run(state, func(t *testing.T) {
			ask := AskOut{AskID: "original", State: state, SupersededBy: "replacement", Ask: messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "419", Base: base, Head: head}}
			before, _ := json.Marshal(ask)
			encoded, err := json.Marshal(ask.View())
			if err != nil {
				t.Fatal(err)
			}
			var view struct {
				Forge     string                      `json:"forge"`
				Requested struct{ Base, Head string } `json:"requested"`
			}
			if err := json.Unmarshal(encoded, &view); err != nil {
				t.Fatal(err)
			}
			requestedField(t, "forge", view.Forge, "github")
			requestedField(t, "base", view.Requested.Base, base)
			requestedField(t, "head", view.Requested.Head, head)
			after, _ := json.Marshal(ask)
			requestedField(t, "stored ask", string(after), string(before))
		})
	}
}

func requestedField(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %q want %q", name, got, want)
	}
}
