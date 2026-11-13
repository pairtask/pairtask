package chat

import "testing"

func TestContactKeySelectionRejectsExtraExplicitKeys(t *testing.T) {
	const pending = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	selected, problem := chosenKey([]string{pending}, []string{"different", "extra"})
	if selected != "" {
		t.Fatalf("malformed explicit selection approved %q", selected)
	}
	if problem != "usage: /pair accept-key [KEY]" {
		t.Fatalf("malformed selection explanation = %q", problem)
	}
}
