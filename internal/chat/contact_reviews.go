package chat

import (
	"charm.land/lipgloss/v2"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
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
var reviewPairWords = map[string]int{pairReviews: 3, pairApprove: 2, pairDecline: 2}

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
func reviewHead(ws workspaceLabel, v protocol.ContactMessageView, l dotLook) string {
	a := v.ReviewAsk
	if a == nil {
		return outcomeHead(ws, v.ReviewOutcome, l) // the request's record is gone; where it stands is not
	}
	head := " · review " + safe(a.Repository) + "#" + safe(a.Change) + " pinned " + safe(shortSHA(a.Base)) + ".." + safe(shortSHA(a.Head))
	switch {
	case refusedHere(v):
		head += " · refused: " + refusalWords(v.ReviewReason)
	case v.ReviewState != "":
		head += " · " + safe(v.ReviewState)
	}
	if v.ReviewState == "awaiting" {
		head += ": /pair approve " + safe(v.ID) + " or decline"
	}
	return head + outcomeHead(ws, v.ReviewOutcome, l)
}

// outcomeHead is where a request this side sent stands, as the other
// workspace gave it, escaped.
// A request under way blinks with the footer's dot, a failed one stays.
func outcomeHead(ws workspaceLabel, o *protocol.ReviewOutcomeView, l dotLook) string {
	if o == nil {
		return ""
	}
	head := " · " + theirState(o.State, o.Reviewer, ws)
	if d := askDot(o.State); d != dotNone {
		head = " · " + d.mark(lipgloss.NewStyle(), dotLook{off: d.blinks() && l.off, mono: l.mono}) + " " + theirState(o.State, o.Reviewer, ws)
	}
	if o.Verdict != "" {
		head += " " + safe(o.Verdict)
	}
	if o.URL != "" {
		head += " " + safe(o.URL)
	}
	for _, words := range outcomeLinks(o).words() {
		head += " · " + words
	}
	return head
}

// askLinks are where a request's change moved, as the reviewer reported
// it, and the requests it replaces or that replaced it (#259).
type askLinks struct {
	askID                    string
	movedTo                  *protocol.Revisions
	supersedes, supersededBy string
	pruned                   bool
}

func outcomeLinks(o *protocol.ReviewOutcomeView) askLinks {
	return askLinks{o.AskID, o.MovedTo, o.Supersedes, o.SupersededBy, o.SupersedesPruned}
}

func askedLinks(it protocol.ReviewAskedItem) askLinks {
	return askLinks{it.AskID, it.MovedTo, it.Supersedes, it.SupersededBy, it.SupersedesPruned}
}

// words say each link, escaped; none for a request without any.
func (l askLinks) words() []string {
	var words []string
	if l.movedTo != nil {
		words = append(words, l.movedWords())
	}
	if l.supersedes != "" {
		words = append(words, l.replacesWords())
	}
	if l.supersededBy != "" {
		words = append(words, "replaced by "+safe(l.supersededBy))
	}
	return words
}

// movedWords say where the change moved and, until it is, how to ask again.
func (l askLinks) movedWords() string {
	words := "moved to " + safe(shortSHA(l.movedTo.Base)) + ".." + safe(shortSHA(l.movedTo.Head)) + " (as reported)"
	if l.supersededBy == "" {
		words += ": ask again with /review --replaces " + safe(l.askID)
	}
	return words
}

// replacesWords name the request this one replaces, and whether its record
// is gone.
func (l askLinks) replacesWords() string {
	if l.pruned {
		return "replaces " + safe(l.supersedes) + " (its record was pruned)"
	}
	return "replaces " + safe(l.supersedes)
}

// reviewIDRows name a review request on both sides, escaped: for one sent
// from here, the review the other side made of it; for one the contact
// sent, their ask ID and the review it became here, once it is one.
func reviewIDRows(v protocol.ContactMessageView) []string {
	switch {
	case v.ReviewOutcome != nil:
		return []string{theirReviewID(v.ReviewOutcome.ReviewID)}
	case v.ReviewAsk == nil || v.Direction != "in":
		return nil
	}
	rows := []string{"requester's ask ID " + safe(v.ID)}
	if v.ReviewID != "" {
		rows = append(rows, "local review ID "+safe(v.ReviewID))
	}
	return rows
}

// theirReviewID names the review the other workspace made of a request,
// escaped; unknown until one of its outcomes reports it, which says
// nothing of why.
func theirReviewID(id string) string {
	if id == "" {
		return "reviewer ID not reported"
	}
	return "their review ID " + safe(id)
}

func shortSHA(sha string) string { return sha[:min(len(sha), 12)] }

// pairReviewRetired answers /pair review, retired in favour of /review: it
// names the /review command that asks the same of the open tab's workspace.
func (m *Model) pairReviewRetired(pairWords) tea.Cmd {
	workspace := "WORKSPACE"
	if key := m.conv.selected; key.isContact() {
		workspace = string(routeToken(m.pairedContacts(), identityKey(key.contact)))
	}
	m.status = "/pair review is retired: use /review [--pin BASE..HEAD] [--publish] [--formal] TARGET @AGENT@" + safe(workspace)
	return nil
}
