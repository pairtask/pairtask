package chat

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// Review requests in chat (#201): ask an agent to review a change, follow
// the reviews and cancel one. The broker holds them; chat shows them in a
// panel and never in the transcript.

const (
	kindReview      = "review"
	kindReviewQuiet = "review quiet"
	panelReviews    = "reviews"
	reviewUsage     = "usage: /review [--no-publish] [--no-formal] TARGET AGENT [SCOPE] | /review status ID | /review cancel ID [REASON]"
)

// reviewState is what chat knows of reviews: the last list, the selected
// review, and an operator request whose answer was lost.
type reviewState struct {
	loaded       bool // reviews were listed or requested this session: changes refresh them
	list         []protocol.ReviewView
	total        int
	partial      bool
	selected     string
	stale        bool                 // reviews changed while a quiet read was in flight
	followed     string               // the review this session requested last: its progress shows in the status line
	detail       *protocol.ReviewView // the selected review as last read, kept when a partial list leaves it out
	refreshAfter bool                 // reconnected while a request was resent: read the reviews once it is answered
	intent       *protocol.Message    // a request in flight, resent unchanged after a reconnect if its answer was lost
	unknown      bool                 // the intent's answer was lost
}

func reviewArg(a argument) error {
	fields := strings.Fields(string(a))
	if len(fields) == 0 {
		return errors.New(reviewUsage)
	}
	switch fields[0] {
	case "status":
		return wantFields(fields, 2, 2)
	case "cancel":
		return wantFields(fields, 2, -1)
	}
	_, _, err := parseReviewRequest(fields)
	return err
}

func wantFields(fields []string, least, most int) error {
	if len(fields) < least || tooMany(fields, most) {
		return errors.New(reviewUsage)
	}
	return nil
}

// tooMany: more fields than most, where a negative most means no bound.
func tooMany(fields []string, most int) bool {
	return most >= 0 && len(fields) > most
}

// parseReviewRequest reads [--no-publish] [--no-formal] TARGET AGENT [SCOPE].
func parseReviewRequest(fields []string) (protocol.ReviewSpec, string, error) {
	var policy *protocol.ReviewPolicy
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		policy = reviewFlag(policy, fields[0])
		if policy == nil {
			return protocol.ReviewSpec{}, "", fmt.Errorf("unknown flag %s; %s", safe(fields[0]), reviewUsage)
		}
		fields = fields[1:]
	}
	if len(fields) < 2 {
		return protocol.ReviewSpec{}, "", errors.New(reviewUsage)
	}
	target, err := review.ParseTarget(fields[0])
	if err != nil {
		return protocol.ReviewSpec{}, "", err
	}
	spec := protocol.ReviewSpec{ReviewTarget: target, Reviewer: fields[1], Scope: strings.Join(fields[2:], " "), Policy: policy}
	return spec, fields[1], protocol.ValidateReviewSpec(spec)
}

// reviewFlag applies one policy flag; an unknown flag is nil.
func reviewFlag(policy *protocol.ReviewPolicy, flag string) *protocol.ReviewPolicy {
	if policy == nil {
		policy = &protocol.ReviewPolicy{Publish: true, Formal: true}
	}
	switch flag {
	case "--no-publish":
		policy.Publish, policy.Formal = false, false
	case "--no-formal":
		policy.Formal = false
	default:
		return nil
	}
	return policy
}

// reviewCommand is /review: request, status or cancel.
func (m *Model) reviewCommand(arg argument) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityReviews) {
		m.status = "this broker doesn't support review requests; upgrade it to use /review"
		return nil
	}
	if !m.ready() {
		m.status = "not connected; /review again once reconnected"
		return nil
	}
	fields := strings.Fields(string(arg))
	switch fields[0] {
	case "status":
		return m.reviewStatus(fields[1])
	case "cancel":
		return m.reviewCancel(fields[1], strings.Join(fields[2:], " "))
	}
	if in := m.reviews.intent; in != nil {
		// One request at a time, so a lost answer is never lost with it.
		m.status = "review request " + safe(in.MessageID) + " is not settled yet; /review again once it is answered"
		return nil
	}
	spec, reviewer, _ := parseReviewRequest(fields)
	msg := protocol.Message{Type: protocol.TypeOperatorReview, MessageID: uuid.NewString(), ReviewOp: protocol.ReviewOpRequest, ReviewSpec: &spec}
	m.reviews.intent, m.reviews.unknown, m.reviews.loaded = &msg, false, true
	m.status = "requesting a review from " + safe(reviewer) + "…"
	return m.request(kindReview, msg, protocol.ReviewOpRequest, msg.MessageID)
}

// reviewsCommand is /reviews: list them in the panel.
func (m *Model) reviewsCommand(argument) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityReviews) {
		m.status = "this broker doesn't support review requests; upgrade it to use /reviews"
		return nil
	}
	m.reviews.loaded, m.reviews.selected, m.reviews.detail = true, "", nil
	m.panel = &panel{kind: panelReviews}
	return m.request(kindReview, protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpList}, protocol.ReviewOpList)
}

func (m *Model) reviewStatus(ref string) tea.Cmd {
	id, problem := m.reviewID(ref)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.reviews.loaded, m.reviews.selected, m.reviews.detail = true, id, nil
	m.panel = &panel{kind: panelReviews}
	return m.request(kindReview, protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpStatus, ReviewID: id}, protocol.ReviewOpStatus)
}

func (m *Model) reviewCancel(ref, reason string) tea.Cmd {
	id, problem := m.reviewID(ref)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "cancelling review " + safe(id) + "…"
	return m.request(kindReview, protocol.Message{Type: protocol.TypeOperatorReview, MessageID: uuid.NewString(), ReviewOp: protocol.ReviewOpCancel,
		ReviewID: id, ReviewReason: reason}, protocol.ReviewOpCancel)
}

// reviewID resolves a full ID, or a prefix of exactly one listed review; an
// exact ID wins over reviews it is a prefix of.
func (m *Model) reviewID(ref string) (string, string) {
	var matches []string
	for _, r := range m.reviews.list {
		if r.ID == ref {
			return ref, ""
		}
		if strings.HasPrefix(r.ID, ref) {
			matches = append(matches, r.ID)
		}
	}
	switch {
	case len(matches) == 1:
		return matches[0], ""
	case len(matches) > 1:
		return "", safe(ref) + " matches several reviews; type more of the ID"
	case protocol.ValidateReviewID(ref) == nil && !m.reviews.loaded:
		return ref, "" // not listed yet: the broker decides
	}
	return "", "no listed review matches " + safe(ref) + "; /reviews lists them"
}

// reviewResult answers /review and /reviews: one review or the list.
func (m *Model) reviewResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := req.ids[0]
	var follow tea.Cmd
	if op == protocol.ReviewOpRequest && m.settles(req) {
		m.reviews.intent, m.reviews.unknown = nil, false
		follow = m.refreshAfterResend()
	}
	if resp.Type != protocol.TypeOperatorReviewResult {
		m.status = "review " + op + " refused: " + brokerReason(resp)
		return follow
	}
	m.applyReviews(resp)
	if resp.Review != nil {
		m.status = reviewStatusLine(op, *resp.Review)
		if op == protocol.ReviewOpRequest {
			m.reviews.followed = resp.Review.ID
		}
	}
	return follow
}

// refreshAfterResend reads the reviews a reconnect deferred behind a resend.
func (m *Model) refreshAfterResend() tea.Cmd {
	if !m.reviews.refreshAfter {
		return nil
	}
	m.reviews.refreshAfter = false
	return m.reviewsChanged()
}

// settles: this answer is to the request in flight, not to an earlier one.
func (m *Model) settles(req pendingRequest) bool {
	in := m.reviews.intent
	return in != nil && len(req.ids) > 1 && req.ids[1] == in.MessageID
}

// quietReviewResult keeps a refreshed list without touching the panel,
// the selection or the draft; one more read follows if changes kept coming.
func (m *Model) quietReviewResult(_ pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type == protocol.TypeOperatorReviewResult {
		m.applyReviews(resp)
		m.reviewOutcome()
	}
	if !m.reviews.stale {
		return nil
	}
	m.reviews.stale = false
	return m.refreshReviews()
}

// reviewsChanged follows the broker's reviews_changed, once reviews matter
// here.
func (m *Model) reviewsChanged() tea.Cmd {
	if !m.reviews.loaded {
		return nil
	}
	if m.requested(kindReviewQuiet, protocol.ReviewOpList) {
		m.reviews.stale = true
		return nil
	}
	return m.refreshReviews()
}

func (m *Model) refreshReviews() tea.Cmd {
	return m.request(kindReviewQuiet, protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpList}, protocol.ReviewOpList)
}

// applyReviews merges an answer into what chat knows: a list replaces it,
// one review updates its row.
func (m *Model) applyReviews(resp protocol.Message) {
	if p := resp.ReviewPage; p != nil {
		m.reviews.list, m.reviews.total, m.reviews.partial = p.Reviews, p.Total, p.Truncated
		for _, r := range p.Reviews {
			m.keepDetail(r)
		}
	}
	if r := resp.Review; r != nil {
		m.upsertReview(*r)
		m.keepDetail(*r)
	}
}

// keepDetail updates the selected review's own record when r is it.
func (m *Model) keepDetail(r protocol.ReviewView) {
	if r.ID == m.reviews.selected {
		m.reviews.detail = &r
	}
}

// reviewsReconnected resends a request whose answer was lost, then reads
// the reviews again if this session follows them: changes made while
// offline were never announced here.
func (m *Model) reviewsReconnected() tea.Cmd {
	if resend := m.resendReview(); resend != nil {
		// The resend goes first; the reviews are read once it is answered.
		m.reviews.refreshAfter = m.reviews.loaded
		return resend
	}
	if m.reviews.loaded && m.session.HasCapability(protocol.CapabilityReviews) {
		return m.reviewsChanged()
	}
	return nil
}

func (m *Model) upsertReview(r protocol.ReviewView) {
	for i := range m.reviews.list {
		if m.reviews.list[i].ID == r.ID {
			m.reviews.list[i] = r
			return
		}
	}
	m.reviews.list = append([]protocol.ReviewView{r}, m.reviews.list...)
	m.reviews.total++
}

// reviewOutcome shows the requested review's progress in the status line,
// so its result is seen with the panel closed too.
func (m *Model) reviewOutcome() {
	for _, r := range m.reviews.list {
		if r.ID == m.reviews.followed {
			m.status = reviewStatusLine("", r)
			return
		}
	}
}

func reviewStatusLine(op string, r protocol.ReviewView) string {
	line := fmt.Sprintf("review %s for %s: %s", safe(r.ID), safe(r.Spec.Reviewer), safe(r.State))
	if op == protocol.ReviewOpRequest && r.State == protocol.ReviewRequested {
		line = fmt.Sprintf("review requested: %s for %s; /reviews follows it", safe(r.ID), safe(r.Spec.Reviewer))
	}
	if r.Verdict != nil {
		line += ", verdict " + safe(r.Verdict.Outcome) + ", publication " + safe(r.Publication.State)
	}
	return line
}

// reviewsDisconnected leaves a request in flight unknown: its answer may be
// lost, so it is resent unchanged after the reconnect.
func (m *Model) reviewsDisconnected() {
	if m.reviews.intent != nil {
		m.reviews.unknown = true
	}
}

// resendReview resends a request whose answer was lost: the same message ID
// and specification, so the broker returns the review it already made.
func (m *Model) resendReview() tea.Cmd {
	if m.reviews.intent == nil || !m.reviews.unknown || !m.session.HasCapability(protocol.CapabilityReviews) {
		return nil
	}
	m.reviews.unknown = false
	m.status = "review request outcome unknown; resending " + safe(m.reviews.intent.MessageID)
	return m.request(kindReview, *m.reviews.intent, protocol.ReviewOpRequest, m.reviews.intent.MessageID)
}
