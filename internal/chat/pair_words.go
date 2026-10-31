package chat

import (
	"slices"
	"strings"
)

// The /pair words of a ⇄ tab offer what they act on: reviews the agents
// here, then ask, auto or off, then its flags; unshare the agents shared
// with that workspace; approve and decline its review requests waiting for
// this side. A pick that completes the line runs it on Enter; Tab only
// fills, to add more.

// reviewModes are what /pair reviews may let that workspace ask.
var reviewModes = []string{"ask", "auto", "off"}

// reviewFlags widen what a review grant lets it publish.
var reviewFlags = []string{"--publish", "--formal"}

// pairWordHelp says what each of these choices does, beside it.
var pairWordHelp = map[string]string{
	"ask":       "each request waits for your approval",
	"auto":      "requests go straight to the agent",
	"off":       "no reviews from this workspace",
	"--publish": "the verdict may be published to the forge",
	"--formal":  "an approval may count as a formal one",
}

// reviewsCompletions offer /pair reviews' next word: agents here, then a
// mode, then the flags not yet given.
func (m *Model) reviewsCompletions(l argLine) []string {
	if !m.conv.selected.isContact() {
		return nil
	}
	if len(l.words) == 1 {
		return m.shareCompletions(l)
	}
	if slices.ContainsFunc(l.words, isReviewMode) {
		return withPrefix(slices.DeleteFunc(slices.Clone(reviewFlags), l.typedWord), l.current)
	}
	return withPrefix(reviewModes, l.current)
}

func isReviewMode(w string) bool { return slices.Contains(reviewModes, w) }

// typedWord: the word is already on the line.
func (l argLine) typedWord(w string) bool { return slices.Contains(l.words, w) }

// unshareCompletions are the agents this side shares with the ⇄ tab's
// workspace and the line doesn't name yet.
func (m *Model) unshareCompletions(l argLine) []string {
	t := m.contacts.threads[m.conv.selected.contact]
	if t == nil || !t.loaded || afterPick(l) {
		return nil
	}
	shared := slices.DeleteFunc(slices.Clone(t.shared), func(id string) bool {
		return l.typedWord(id) || strings.ContainsFunc(id, isSpace)
	})
	slices.Sort(shared)
	return withPrefix(shared, l.current)
}

// awaitingRequests are the ⇄ tab workspace's review requests waiting for
// this side's approval, as their IDs.
func (m *Model) awaitingRequests(l argLine) []string {
	t := m.contacts.threads[m.conv.selected.contact]
	if t == nil || len(l.words) != 1 {
		return nil
	}
	var ids []string
	for _, v := range t.rows {
		if v.ReviewState == "awaiting" && v.Direction == "in" {
			ids = append(ids, v.ID)
		}
	}
	slices.Sort(ids)
	return withPrefix(ids, l.current)
}

// pairWordLabel is a row for one of these choices: the word and what it
// does, or the request it names.
func (m *Model) pairWordLabel(item string) (string, bool) {
	if help, ok := pairWordHelp[item]; ok {
		return "  " + safe(item) + "  " + mutedStyle.Render(help), true
	}
	return "", false
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// reviewerChoices are the agents the ⇄ tab's workspace shares, for its
// /pair review: theirs, never ours.
func (m *Model) reviewerChoices(l argLine) []string {
	t := m.contacts.threads[m.conv.selected.contact]
	if t == nil || !t.loaded || len(l.words) != 1 {
		return nil
	}
	ids := slices.DeleteFunc(slices.Clone(t.directory), func(id string) bool { return strings.ContainsFunc(id, isSpace) })
	slices.Sort(ids)
	return withPrefix(ids, l.current)
}

// nameChoices offer forgetting the name; any other is typed.
func (m *Model) nameChoices(l argLine) []string {
	if len(l.words) != 1 || !m.conv.selected.isContact() {
		return nil
	}
	return withPrefix([]string{clearName}, l.current)
}

// operationChoices are the listed redemptions' operation IDs, for the
// optional argument of /pair status and /pair settle: bare, they run as
// they are; Tab offers one.
func (m *Model) operationChoices(l argLine) []string {
	s := m.pairing.snapshot
	if s == nil || len(l.words) != 1 {
		return nil
	}
	ids := make([]string, 0, len(s.Joins))
	for _, j := range s.Joins {
		ids = append(ids, j.OperationID)
	}
	return withPrefix(ids, l.current)
}
