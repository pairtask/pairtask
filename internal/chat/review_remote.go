package chat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// A review asked of a paired workspace's agent from /review (#297): name
// it AGENT@WORKSPACE and give the change. This side looks up the change's
// base and head with its own access, pins them and sends the request to
// that workspace; --pin BASE..HEAD pins them instead. The workspace is
// fixed when the command runs, never by a name that changes meanwhile, and
// a lookup the operator left behind never sends anything.

const kindRemoteReview = "remote review ask"

// lookupTimeout bounds one change lookup.
const lookupTimeout = 20 * time.Second

// askFlags are a request's leading flags.
type askFlags struct {
	publish, noPublish, formal, noFormal bool
	pin                                  *protocol.Revisions
}

// reviewFields splits a /review line into words, a word in double quotes
// kept whole without them. A quote never closed stays in its word;
// unclosedQuote refuses such a line first.
func reviewFields(line string) []string {
	var words []string
	rest := strings.TrimSpace(line)
	for rest != "" {
		word, tail := nextReviewWord(rest)
		words, rest = append(words, word), strings.TrimLeft(tail, " \t")
	}
	return words
}

// unclosedQuote: a word opens a quote it never closes.
func unclosedQuote(line string) bool {
	rest := strings.TrimSpace(line)
	for rest != "" {
		if rest[0] == '"' && !strings.Contains(rest[1:], `"`) {
			return true
		}
		_, tail := nextReviewWord(rest)
		rest = strings.TrimLeft(tail, " \t")
	}
	return false
}

func nextReviewWord(s string) (string, string) {
	if quoted, tail, ok := strings.Cut(s[1:], `"`); s[0] == '"' && ok {
		return quoted, tail
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// readReviewFlags takes the leading flags off a request's words.
func readReviewFlags(words []string) (askFlags, []string, error) {
	var f askFlags
	for len(words) > 0 && strings.HasPrefix(words[0], "--") {
		var err error
		if words, err = f.take(words); err != nil {
			return f, nil, err
		}
	}
	return f, words, f.conflict()
}

// take reads the first flag, and --pin's value, off words.
func (f *askFlags) take(words []string) ([]string, error) {
	switches := map[string]*bool{"--publish": &f.publish, "--no-publish": &f.noPublish, "--formal": &f.formal, "--no-formal": &f.noFormal}
	if b, ok := switches[words[0]]; ok {
		*b = true
		return words[1:], nil
	}
	if words[0] != "--pin" {
		return nil, fmt.Errorf("unknown flag %s; %s", safe(words[0]), reviewUsage)
	}
	if len(words) < 2 {
		return nil, errPin
	}
	pin, err := parsePin(words[1])
	f.pin = pin
	return words[2:], err
}

var errPin = errors.New("--pin needs full commit IDs: BASE..HEAD, 40 or 64 lowercase hex each")

// parsePin is BASE..HEAD as full commit IDs.
func parsePin(word string) (*protocol.Revisions, error) {
	base, head, ok := strings.Cut(word, "..")
	pin := &protocol.Revisions{Base: base, Head: head}
	if !ok || protocol.ValidateRevisions(pin) != nil {
		return nil, errPin
	}
	return pin, nil
}

// conflict is two flags that can't both hold.
func (f askFlags) conflict() error {
	switch {
	case f.publish && f.noPublish:
		return errors.New("--publish and --no-publish conflict")
	case f.formal && f.noFormal:
		return errors.New("--formal and --no-formal conflict")
	case f.formal && f.noPublish:
		return errors.New("--formal and --no-publish conflict")
	}
	return nil
}

// localPolicy is a review here's policy, as before: both unless a flag
// says otherwise, nil without flags.
func (f askFlags) localPolicy() *protocol.ReviewPolicy {
	if !slices.Contains([]bool{f.publish, f.noPublish, f.formal, f.noFormal}, true) {
		return nil
	}
	publish := !f.noPublish
	return &protocol.ReviewPolicy{Publish: publish, Formal: publish && !f.noFormal}
}

// remotePolicy is what a paired workspace's agent is asked to publish:
// nothing unless asked, a formal review only with publishing.
func (f askFlags) remotePolicy() (publish, formal bool) {
	return f.publish || f.formal, f.formal
}

// pairedReviewer splits AGENT@WORKSPACE at its last @, after the one
// leading @ a mention drops; a word without one is a reviewer here.
func pairedReviewer(word string) (agent, workspace string, ok bool) {
	name := strings.TrimPrefix(word, "@")
	i := strings.LastIndex(name, "@")
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// remoteAsk is a review request for a paired workspace's agent, its
// workspace fixed by key.
type remoteAsk struct {
	key, agent, workspace string
	target                protocol.ReviewTarget
	scope                 string
	publish, formal       bool
}

// remoteLookup is the change lookup in flight, if any.
type remoteLookup struct {
	gen    uint64
	ask    *remoteAsk
	cancel context.CancelFunc
}

// remoteLookupMsg is a lookup's answer, for its generation.
type remoteLookupMsg struct {
	gen  uint64
	pins protocol.Revisions
	err  error
}

// isRemoteReview: the request names a paired workspace's agent.
func isRemoteReview(words []string) bool {
	_, rest, err := readReviewFlags(words)
	if err != nil || len(rest) < 2 {
		return false
	}
	_, _, ok := pairedReviewer(rest[1])
	return ok
}

// checkRemoteReview is why a request for a paired workspace's agent can't
// be made from its words alone, or nil.
func checkRemoteReview(words []string) error {
	_, rest, err := readReviewFlags(words)
	if err != nil {
		return err
	}
	_, err = review.ParseTarget(rest[0])
	return err
}

// requestRemoteReview fixes the workspace, then pins the change: as given,
// or by looking it up.
func (m *Model) requestRemoteReview(words []string) tea.Cmd {
	m.cancelLookup()
	ask, pin, problem := m.remoteAskFor(words)
	switch {
	case problem != "":
		m.status = problem
		return nil
	case pin != nil:
		return m.sendRemoteReview(ask, *pin)
	case m.opts.Bounds.ResolveChange == nil:
		m.status = "can't look up changes here: add --pin BASE..HEAD"
		return nil
	}
	return m.startLookup(ask)
}

// remoteAskFor is the request the words make, its pins when given, or why
// it can't be made.
func (m *Model) remoteAskFor(words []string) (remoteAsk, *protocol.Revisions, string) {
	f, rest, err := readReviewFlags(words)
	if err != nil {
		return remoteAsk{}, nil, err.Error()
	}
	target, err := review.ParseTarget(rest[0])
	if err != nil {
		return remoteAsk{}, nil, err.Error()
	}
	agent, workspace, _ := pairedReviewer(rest[1])
	key, problem := m.pairedWorkspace(workspace)
	if problem != "" {
		return remoteAsk{}, nil, problem
	}
	publish, formal := f.remotePolicy()
	return remoteAsk{key: key, agent: agent, workspace: workspace, target: target, scope: strings.Join(rest[2:], " "),
		publish: publish, formal: formal}, f.pin, ""
}

// startLookup looks the change up off the update loop, bounded, and
// cancellable by anything that leaves the request behind.
func (m *Model) startLookup(ask remoteAsk) tea.Cmd {
	ctx, cancel := context.WithTimeout(m.ctx, lookupTimeout)
	m.lookup.gen++
	m.lookup.ask, m.lookup.cancel = &ask, cancel
	gen, resolve, target := m.lookup.gen, m.opts.Bounds.ResolveChange, ask.target
	m.status = "looking up " + targetName(target) + "…"
	return func() tea.Msg {
		pins, err := resolve(ctx, target)
		return remoteLookupMsg{gen: gen, pins: pins, err: err}
	}
}

// cancelLookup drops a lookup in flight: its answer, whenever it comes,
// sends nothing and says nothing. It reports whether there was one.
func (m *Model) cancelLookup() bool {
	l := &m.lookup
	if l.ask == nil {
		return false
	}
	l.cancel()
	l.gen++
	l.ask, l.cancel = nil, nil
	return true
}

// lookupDone sends the request its lookup pinned, or says why it can't.
func (m *Model) lookupDone(msg remoteLookupMsg) tea.Cmd {
	l := &m.lookup
	if msg.gen != l.gen || l.ask == nil {
		return nil // left behind: nothing to send or say
	}
	ask := *l.ask
	l.cancel()
	l.ask, l.cancel = nil, nil
	err := msg.err
	if err == nil && protocol.ValidateRevisions(&msg.pins) != nil {
		err = forge.Errorf(forge.ClassInvalid, "not full commit IDs") // the code host's answer, not pins
	}
	if err != nil {
		m.status = "can't look up " + targetName(ask.target) + ": " + lookupFailure(err) + " · add --pin BASE..HEAD to pin it yourself"
		return nil
	}
	return m.sendRemoteReview(ask, msg.pins)
}

// lookupFailures name what went wrong, never in the code host's words.
var lookupFailures = map[string]string{
	forge.ClassForbidden:   "no access to this repository",
	forge.ClassNotFound:    "change not found",
	forge.ClassRateLimited: "the code host's rate limit",
	forge.ClassInvalid:     "unexpected answer",
	forge.ClassUnsupported: "this code host can't be looked up here",
}

func lookupFailure(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	if words, ok := lookupFailures[forge.ClassOf(err)]; ok {
		return words
	}
	return "network error"
}

// sendRemoteReview sends the pinned request to the workspace fixed when the
// command ran, if it can still take one.
func (m *Model) sendRemoteReview(ask remoteAsk, pins protocol.Revisions) tea.Cmd {
	if !m.contactTakesRequests(ask.key) {
		m.status = safe(ask.workspace) + " can't take requests now (not confirmed, paused or removed): not sent"
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewAsk, ContactKey: ask.key,
		ContactTo: []string{ask.agent}, MessageID: uuid.NewString(),
		ReviewAsk: &protocol.ReviewAsk{ReviewTarget: ask.target, Base: pins.Base, Head: pins.Head, Scope: ask.scope,
			Publish: ask.publish, Formal: ask.formal}}
	if problem := validReviewWords(msg); problem != "" {
		m.status = problem
		return nil
	}
	return m.request(kindRemoteReview, msg, ask.key, msg.MessageID, m.sentRemoteStatus(ask, pins))
}

// contactTakesRequests: the contact is known, confirmed and not paused.
func (m *Model) contactTakesRequests(key string) bool {
	c, ok := m.contact(key)
	return ok && c.Confirmed && c.PausedAt == nil
}

// sentRemoteStatus says what was asked: of whom, which change at which
// commits, what to publish, and when the agent isn't listed as shared.
func (m *Model) sentRemoteStatus(ask remoteAsk, pins protocol.Revisions) string {
	status := fmt.Sprintf("asked %s@%s to review %s at %s (base %s) · %s", safe(ask.agent), safe(ask.workspace), targetName(ask.target),
		shortSHA7(pins.Head), shortSHA7(pins.Base), policyWords(ask.publish, ask.formal))
	if !slices.Contains(m.theirDirectory(ask.key), ask.agent) {
		status += " · " + safe(ask.agent) + " isn't listed as shared by " + safe(ask.workspace) +
			" as last synced (that list can be out of date); they decide"
	}
	return status
}

func policyWords(publish, formal bool) string {
	switch {
	case formal:
		return "formal"
	case publish:
		return "publish"
	}
	return "no publish"
}

func shortSHA7(sha string) string { return sha[:min(len(sha), 7)] }

func targetName(t protocol.ReviewTarget) string { return safe(t.Repository) + "#" + safe(t.Change) }

// theirDirectory is what the contact last said it shares.
func (m *Model) theirDirectory(key string) []string {
	if t := m.contacts.threads[key]; t != nil {
		return t.directory
	}
	return nil
}

// remoteReviewResult shows the request queued for the workspace, or why it
// was refused.
func (m *Model) remoteReviewResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactMessage == nil {
		m.status = "review request refused: " + refusalReason(resp)
		return nil
	}
	t := m.contacts.thread(req.ids[0])
	t.merge([]protocol.ContactMessageView{*resp.ContactMessage})
	m.status = req.ids[2]
	return nil
}
