package chat_test

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPairReviewRetirementAcceptanceOldCommandNeverSendsAndShowsMigration(t *testing.T) {
	for _, command := range []string{
		"/pair review",
		"/pair review @reviewer pairtask/pairtask#235 base head",
		"/pair review arbitrary extra words and --publish",
	} {
		t.Run(command, func(t *testing.T) {
			h, _ := contactMessagesHarness(t)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v2Command(h, command)
			chatEqual(t, "migration in the selected workspace", h.m.State().Status,
				"/pair review is retired: use /review [--pin BASE..HEAD] [--publish] [--formal] TARGET @AGENT@remote")
			chatEqual(t, "old command never starts a request", h.m.State().InFlight, 0)
			chatEqual(t, "old command never writes to the broker", len(h.requests), 0)
		})
	}

	h, _ := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	v2Command(h, "/pair review @reviewer pairtask/pairtask#235 base head")
	chatEqual(t, "migration outside a workspace", h.m.State().Status,
		"/pair review is retired: use /review [--pin BASE..HEAD] [--publish] [--formal] TARGET @AGENT@WORKSPACE")
	chatEqual(t, "outside command never writes to the broker", len(h.requests), 0)
}

func TestPairReviewRetirementAcceptanceCompletionOffersPermissionOnly(t *testing.T) {
	h, _ := commandContactChoices(t)
	commandCompleteLine(h, "pair rev")
	picker := h.m.State().Picker
	if picker == nil {
		t.Fatal("pair command completion missing")
	}
	if slices.Contains(picker.Items, "review") || !slices.Contains(picker.Items, "reviews") {
		t.Fatalf("retired command offered or permission control missing: %v", picker.Items)
	}
	chatEqual(t, "completion sends nothing", h.m.State().InFlight, 0)
}
