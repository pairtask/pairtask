package chat

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// /pair while typing: its words, the requests waiting for a decision after
// accept or reject, and the usage of the word being used, so nothing is
// guessed mid-line. Nothing is offered after redeem: its code is secret.

const pendingAcceptance = "pending_acceptance"

var pairVerbs = []string{"open", "status", "cancel", "accept", "reject", "redeem", "settle"}

// contactPairVerbs are offered too in a contact's tab, where they act.
var contactPairVerbs = []string{pairShare, pairUnshare, pairReviews, pairReview, pairApprove, pairDecline, pairName}

// pairVerbsHere are the /pair words that act in the open tab.
func (m *Model) pairVerbsHere() []string {
	if m.conv.selected.isContact() && m.routingAvailable() {
		return append(slices.Clone(pairVerbs), contactPairVerbs...)
	}
	return pairVerbs
}

// pairUsages say what each /pair word takes, in the input box's label.
var pairUsages = map[string]string{
	"open":    "open: show a one-time code for the other workspace",
	"status":  "status [OPERATION_ID]",
	"cancel":  "cancel: close the open invitation",
	"accept":  "accept KEY: the start of the request's key; Tab lists requests",
	"reject":  "reject KEY: the start of the request's key; Tab lists requests",
	"redeem":  "redeem HOST:PORT CODE [NAME]: NAME is how this workspace shows on theirs",
	"settle":  "settle [OPERATION_ID]: finish a redemption the other side accepted",
	"share":   "share AGENT...|*: in a ⇄ tab, that workspace may message these agents",
	"unshare": "unshare AGENT...|*: in a ⇄ tab, it may message them no more",
	"reviews": "reviews AGENT... ask|auto|off [--publish] [--formal]: in a ⇄ tab, what that workspace may ask of these agents",
	"review":  "review AGENT TARGET BASE HEAD [SCOPE] [--publish] [--formal]: in a ⇄ tab, ask one of its agents for a review",
	"approve": "approve ID: in a ⇄ tab, let one of its review requests through",
	"decline": "decline ID: in a ⇄ tab, refuse one of its review requests",
	"name":    "name NAME|--clear: in a ⇄ tab, what this side calls that workspace",
}

func decidesRequest(word string) bool {
	return word == "accept" || word == "reject"
}

// pairCompletions offers the words, then the short keys of requests waiting
// for a decision; a typed "sha256:" is ignored, as every key has it.
func (m *Model) pairCompletions(l argLine) []string {
	if len(l.words) == 0 {
		return withPrefix(m.pairVerbsHere(), l.current)
	}
	if words, ok := pairWordChoices[l.words[0]]; ok {
		return words(m, l)
	}
	return nil
}

// requestKeyChoices are the keys of the requests waiting for a decision,
// for accept and reject.
func (m *Model) requestKeyChoices(l argLine) []string {
	if len(l.words) != 1 {
		return nil
	}
	return withPrefix(m.pendingRequestKeys(), protocol.KeyDigest(l.current))
}

// invitationChoices are the open invitation, for cancel.
func (m *Model) invitationChoices(l argLine) []string {
	if len(l.words) != 1 {
		return nil
	}
	return withPrefix(m.openInvitation(), l.current)
}

// pairWordChoices offer the next word of each /pair word.
var pairWordChoices = map[string]func(*Model, argLine) []string{
	"accept":                 (*Model).requestKeyChoices,
	"reject":                 (*Model).requestKeyChoices,
	protocol.PairingOpCancel: (*Model).invitationChoices,
	pairShare:                (*Model).shareCompletions,
	pairReviews:              (*Model).reviewsCompletions,
	pairUnshare:              (*Model).unshareCompletions,
	pairApprove:              (*Model).awaitingRequests,
	pairDecline:              (*Model).awaitingRequests,
	pairReview:               (*Model).reviewerChoices,
	pairName:                 (*Model).nameChoices,
	protocol.PairingOpStatus: (*Model).operationChoices,
	protocol.PairingOpSettle: (*Model).operationChoices,
}

// pendingRequestKeys are the keys of the listed requests that wait for a
// decision, in listing order: the short key when it tells them apart from
// every listed request (ResolveKey matches decided ones too), more of the
// digest when one shares it, and the whole digest when the listing is
// partial, since only a full key resolves then.
func (m *Model) pendingRequestKeys() []string {
	s := m.pairing.snapshot
	if s == nil {
		return nil
	}
	all := make([]string, 0, len(s.Requests))
	var pending []string
	for _, r := range s.Requests {
		digest := protocol.KeyDigest(r.IdentityKey)
		all = append(all, digest)
		if r.State == pendingAcceptance {
			pending = append(pending, digest)
		}
	}
	if s.RequestsTruncated {
		return pending
	}
	keys := make([]string, len(pending))
	for i, d := range pending {
		keys[i] = uniquePrefix(d, all)
	}
	return keys
}

// uniquePrefix is the shortest prefix of digest, at least a short key long,
// that no other digest starts with.
func uniquePrefix(digest string, all []string) string {
	for n := len(protocol.ShortKey(digest)); n < len(digest); n++ {
		prefix := digest[:n]
		if !slices.ContainsFunc(all, func(o string) bool { return o != digest && strings.HasPrefix(o, prefix) }) {
			return prefix
		}
	}
	return digest
}

// pendingRequestLabel is a picker row for an offered key: the key, the
// name the other workspace gave, when it asked, and its address, which a
// request doesn't carry.
func (m *Model) pendingRequestLabel(key string) (string, bool) {
	s := m.pairing.snapshot
	if s == nil {
		return "", false
	}
	i := slices.IndexFunc(s.Requests, func(r protocol.PairingRequestView) bool {
		return r.State == pendingAcceptance && strings.HasPrefix(protocol.KeyDigest(r.IdentityKey), key)
	})
	if i < 0 {
		return "", false
	}
	r := s.Requests[i]
	name := shownName(r.Name)
	if name == "" {
		name = "no name given"
	}
	detail := safe(name) + " · asked " + r.FirstPairedAt.Local().Format("Jan 2 15:04") + " · address unknown"
	return fmt.Sprintf("  %s  %s", safe(key), mutedStyle.Render(detail)), true
}

// argumentLabel is an argument picker row.
func (m *Model) argumentLabel(item string) string {
	if label, ok := m.pairLabel(item); ok {
		return label
	}
	if label, ok := m.ackPickLabel(item); ok {
		return label
	}
	if label, ok := m.replyPickLabel(item); ok {
		return label
	}
	return "  " + safe(item)
}

// commandHint is the input box's label in command mode: the usage of the
// command being typed, and of its /pair word, never the typed arguments.
func (m *Model) commandHint() string {
	const keys = "Enter runs · Tab completes · Esc back to your draft"
	name, rest, hasArgs := strings.Cut(m.compose.cmd.input.Value(), " ")
	c, _ := lookupCommand(argument(name))
	if !hasArgs || !c.hasUsage() {
		return "command · " + keys
	}
	usage := c.usage
	if c.name == "pair" {
		usage = pairWordUsage(rest, usage)
	}
	return "/" + c.name + " " + usage + " · " + keys
}

// pairWordUsage narrows /pair's usage to the word being used, once it is
// typed or unambiguous.
func pairWordUsage(rest, all string) string {
	word, _, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if word == "" {
		return all
	}
	matches := withPrefix(pairVerbs, word)
	if usage, ok := pairUsages[word]; ok {
		return usage
	}
	if len(matches) == 1 {
		return pairUsages[matches[0]]
	}
	return all
}

// invitationLabel is the open invitation's row: when it expires and the
// attempts it has left; its code is never shown here.
func (m *Model) invitationLabel(id string) (string, bool) {
	v := m.invitation()
	if v == nil || v.ID != id {
		return "", false
	}
	detail := fmt.Sprintf("invitation · expires %s · %d attempts left", v.ExpiresAt.Local().Format("15:04"), v.AttemptsLeft)
	return fmt.Sprintf("  %s  %s", safe(id), mutedStyle.Render(detail)), true
}
