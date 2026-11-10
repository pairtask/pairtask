package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/forge"
)

// A refused /review lookup is this side's, made with this side's GitHub
// token: the chat names that token and which refusal, never the reviewer's
// access, and keeps the --pin way around it.
func lookupRefused(t *testing.T, err error) (status, card string) {
	t.Helper()
	h, lookup := remoteHarness(t)
	v2Command(h, "/review pairtask/pairtask#295 reviewer@Office")
	remoteCall(t, lookup)
	lookup.results <- remoteResolution{err: err}
	h.await(func() bool { return strings.Contains(h.m.State().Status, "can't look up") })
	chatEqual(t, "refused lookup never sends", h.m.State().InFlight, 0)
	cards := h.m.State().Cards
	chatEqual(t, "refused lookup leaves one card", len(cards), 1)
	chatEqual(t, "refused lookup card is finished", cards[0].Finished, true)
	return h.m.State().Status, ansi.Strip(strings.Join(cards[0].Steps, "\n"))
}

func lookupWordingClean(t *testing.T, status, card string) {
	t.Helper()
	for _, text := range []string{status, card} {
		for _, banned := range []string{"SECRET", "no access", "GitHub's own words"} {
			if strings.Contains(text, banned) {
				t.Fatalf("lookup failure shows %q: %q", banned, text)
			}
		}
	}
	v3Contains(t, status, "add --pin BASE..HEAD")
}

func TestReviewLookupWordingAcceptanceEachRefusalSaysWhichAndWhose(t *testing.T) {
	for _, tc := range []struct {
		reason forge.Reason
		words  string
	}{
		{forge.ReasonNoToken, "no GitHub token on this side: set GH_TOKEN or run gh auth login"},
		{forge.ReasonTokenRejected, "GitHub rejected your token (401): expired or revoked; run gh auth login"},
		{forge.ReasonRepository, "your GitHub token was refused for this repository (403): it can't read it, or the head is in a fork it can't read"},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			status, card := lookupRefused(t, forge.Refusedf(tc.reason, "SECRET GitHub's own words"))
			v3Contains(t, status, "pairtask/pairtask#295: "+tc.words+" · add --pin BASE..HEAD")
			v3Contains(t, card, "Not sent: "+tc.words+" · add --pin BASE..HEAD")
			lookupWordingClean(t, status, card)
		})
	}
}

func TestReviewLookupWordingAcceptanceForbiddenWithoutReasonNamesThisSidesToken(t *testing.T) {
	status, card := lookupRefused(t, forge.Errorf(forge.ClassForbidden, "SECRET raw credential detail"))
	words := "your GitHub token was refused for this repository"
	v3Contains(t, status, "pairtask/pairtask#295: "+words+" · add --pin BASE..HEAD")
	v3Contains(t, card, "Not sent: "+words+" · add --pin BASE..HEAD")
	for _, guess := range []string{"(401)", "(403)", "no GitHub token"} {
		if strings.Contains(status, guess) || strings.Contains(card, guess) {
			t.Fatalf("a refusal without a reason guesses %q: %q / %q", guess, status, card)
		}
	}
	lookupWordingClean(t, status, card)
}

func TestReviewLookupWordingAcceptanceOtherClassesKeepTheirWords(t *testing.T) {
	for class, words := range map[string]string{
		forge.ClassNotFound:    "change not found",
		forge.ClassRateLimited: "the code host's rate limit",
		forge.ClassInvalid:     "unexpected answer",
	} {
		t.Run(class, func(t *testing.T) {
			status, card := lookupRefused(t, forge.Errorf(class, "SECRET raw detail"))
			v3Contains(t, status, words)
			v3Contains(t, card, "Not sent: "+words+" · add --pin BASE..HEAD")
			if strings.Contains(status, "GitHub token") {
				t.Fatalf("a %s failure blames the token: %q", class, status)
			}
			lookupWordingClean(t, status, card)
		})
	}
}
