package chat

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// Review requests with a paired workspace (#235), in its ⇄ tab:
//
//	/pair reviews AGENT... ask|auto|off [--publish] [--formal]
//	/pair review AGENT TARGET BASE HEAD [SCOPE] [--publish] [--formal]
//	/pair approve ID, /pair decline ID
//
// The first sets what that workspace may ask of agents here; the second
// asks one of its agents; the last decide one of its requests that waits.

const (
	kindContactAsk = "contact review ask"
	pairReviews    = "reviews"
	pairReview     = "review"
	pairApprove    = "approve"
	pairDecline    = "decline"
)

// reviewPairWords are the review words of /pair and the least words each
// takes, itself included; approve and decline take exactly two. Policy
// flags count here; reviewRequestFor checks the operands without them.
var reviewPairWords = map[string]int{pairReviews: 3, pairReview: 5, pairApprove: 2, pairDecline: 2}

// reviewsAvailable: the broker serves review requests with workspaces.
func (m *Model) reviewsAvailable() bool {
	return m.session.HasCapability(protocol.CapabilityContactReviews)
}

// reviewArity reports a review word, and whether it has enough words.
func (f pairWords) reviewArity() (bool, bool) {
	least, ok := reviewPairWords[f[0]]
	if !ok {
		return false, false
	}
	if f[0] == pairApprove || f[0] == pairDecline {
		return true, len(f) == least
	}
	return true, len(f) >= least
}

// splitPolicy takes --publish and --formal out of the words.
func splitPolicy(words []string) ([]string, bool, bool) {
	publish, formal := slices.Contains(words, "--publish"), slices.Contains(words, "--formal")
	rest := slices.DeleteFunc(slices.Clone(words), func(w string) bool { return w == "--publish" || w == "--formal" })
	return rest, publish, formal
}

// reviewCommand is a review word of /pair in a contact's tab.
func (m *Model) contactReviewCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	switch {
	case !m.reviewsAvailable():
		m.status = "this broker can't take review requests with a workspace; upgrade it"
		return nil
	case !key.isContact():
		m.status = "open the workspace's ⇄ tab, then /pair " + fields[0]
		return nil
	case !m.confirmedContact(key.contact):
		m.status = contactConfirming
		return nil
	}
	msg, problem := reviewRequestFor(fields, key.contact)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "review " + fields[0] + "…"
	if msg.ContactOp == protocol.ContactOpReviewAsk {
		return m.request(kindContactAsk, msg, key.contact, msg.MessageID)
	}
	return m.request(kindContactShare, msg, key.contact, fields[0])
}

// contactAskResult shows a review request queued for the workspace, as a
// send's message, or why it was refused.
func (m *Model) contactAskResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact := req.ids[0]
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactMessage == nil {
		m.status = "review request refused: " + refusalReason(resp)
		return nil
	}
	t := m.contacts.thread(contact)
	t.merge([]protocol.ContactMessageView{*resp.ContactMessage})
	t.offset = 0
	m.status = "review request queued for the workspace"
	return nil
}

// reviewRequestFor is the operator_contact request a review word makes, or
// why it can't be made.
func reviewRequestFor(fields pairWords, contact string) (protocol.Message, string) {
	words, publish, formal := splitPolicy(fields[1:])
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactKey: contact, MessageID: uuid.NewString()}
	switch fields[0] {
	case pairReviews:
		return reviewGrantFor(msg, words, publish, formal)
	case pairReview:
		return reviewAskFor(msg, words, publish, formal && publish)
	}
	policy := publish || formal
	if len(words) != 1 || policy {
		return msg, "usage: /pair " + fields[0] + " ID"
	}
	approve := fields[0] == pairApprove
	msg.ContactOp, msg.ContactRead, msg.ContactApprove = protocol.ContactOpReviewDecide, words, &approve
	return msg, validReviewWords(msg)
}

func reviewGrantFor(msg protocol.Message, words []string, publish, formal bool) (protocol.Message, string) {
	if len(words) < 2 {
		return msg, "usage: /pair reviews AGENT... ask|auto|off [--publish] [--formal]"
	}
	msg.ContactOp, msg.ContactAgents = protocol.ContactOpReviewGrant, words[:len(words)-1]
	msg.ContactReviewGrant = &protocol.ContactReviewGrant{Mode: words[len(words)-1], Publish: publish, Formal: formal}
	return msg, validReviewWords(msg)
}

func reviewAskFor(msg protocol.Message, words []string, publish, formal bool) (protocol.Message, string) {
	if len(words) < 4 {
		return msg, "usage: /pair review AGENT TARGET BASE HEAD [SCOPE] [--publish] [--formal]"
	}
	target, err := review.ParseTarget(words[1])
	if err != nil {
		return msg, err.Error()
	}
	msg.ContactOp, msg.ContactTo = protocol.ContactOpReviewAsk, []string{strings.TrimPrefix(words[0], "@")}
	msg.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: target, Base: words[2], Head: words[3], Scope: strings.Join(words[4:], " "),
		Publish: publish, Formal: formal}
	return msg, validReviewWords(msg)
}

// validReviewWords is why a request the broker would refuse can't be sent,
// or "": its own fields, for the open tab's contact.
func validReviewWords(msg protocol.Message) string {
	msg.ContactKey = "sha256:" + strings.Repeat("0", 64) // the tab's, checked when it opened
	if err := protocol.ValidateContactRequest(msg); err != nil {
		return err.Error()
	}
	return ""
}

// reviewGrantsLine is what the contact may ask of agents here, for its card.
func reviewGrantsLine(t *contactThread) string {
	if t == nil || len(t.grants) == 0 {
		return ""
	}
	var parts []string
	for _, g := range t.grants {
		part := safe(g.Agent) + " " + g.Mode
		if g.Formal {
			part += "+formal"
		} else if g.Publish {
			part += "+publish"
		}
		parts = append(parts, part)
	}
	return "they may ask for reviews from " + strings.Join(parts, ", ")
}

// reviewHead is a review request's line: the change, its pins, and for one
// the contact sent here, where it is.
func reviewHead(v protocol.ContactMessageView) string {
	a := v.ReviewAsk
	if a == nil {
		return ""
	}
	head := " · review " + safe(a.Repository) + "#" + safe(a.Change) + " pinned " + safe(shortSHA(a.Base)) + ".." + safe(shortSHA(a.Head))
	if v.ReviewState != "" {
		head += " · " + safe(v.ReviewState)
	}
	if v.ReviewState == "awaiting" {
		head += ": /pair approve " + safe(v.ID) + " or decline"
	}
	return head + outcomeHead(v.ReviewOutcome)
}

// outcomeHead is where a request this side sent stands, as the other
// workspace gave it, escaped.
func outcomeHead(o *protocol.ReviewOutcomeView) string {
	if o == nil {
		return ""
	}
	head := " · " + safe(o.State)
	if o.Verdict != "" {
		head += " " + safe(o.Verdict)
	}
	if o.URL != "" {
		head += " " + safe(o.URL)
	}
	return head
}

func shortSHA(sha string) string { return sha[:min(len(sha), 12)] }
