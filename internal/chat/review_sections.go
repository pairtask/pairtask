package chat

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// /reviews in sections (#286): requests waiting for the operator first,
// then reviews running here, this side's requests to paired workspaces,
// and every review here. The first two lists come from the broker a page
// at a time; each item keeps one short ref for the session, so a ref
// offered once never names another request. Nothing here decides, claims
// or acknowledges anything: /reviews open only shows.

const kindReviewPending = "review pending"

// pendingSection is one paged list: its items, how many there are, and the
// cursor of the next page. gen fences its reads: a first page starts a new
// generation, and an answer from an older one is dropped.
type pendingSection[T any] struct {
	items   []T
	total   int
	more    bool
	next    string
	gen     uint64
	firstIn bool // a first page is being read
	again   bool // a change arrived meanwhile: read the first page once more
}

// add replaces the list with a first page, or appends a later one, each
// item once.
func (s *pendingSection[T]) add(items []T, first bool, key func(T) string) {
	if first {
		s.items = nil
	}
	held := map[string]bool{}
	for _, it := range s.items {
		held[key(it)] = true
	}
	for _, it := range items {
		if !held[key(it)] {
			s.items, held[key(it)] = append(s.items, it), true
		}
	}
}

// reviewPending is the panel's paged sections and their refs.
type reviewPending struct {
	needs pendingSection[protocol.ReviewAwaitingItem]
	asked pendingSection[protocol.ReviewAskedItem]
	refs  map[string]string // identity → ref, for the session
	byRef map[string]string // ref → identity
	count map[byte]int      // refs given per prefix
	nav   *navTarget        // a request being looked for in its ⇄ tab
}

func needKey(it protocol.ReviewAwaitingItem) string { return "n" + it.Contact + "\x00" + it.MessageID }
func askedKey(it protocol.ReviewAskedItem) string   { return "a" + it.Contact + "\x00" + it.AskID }

// ref is an identity's ref, given on first sight and kept.
func (p *reviewPending) ref(identity string) string {
	if p.refs == nil {
		p.refs, p.byRef, p.count = map[string]string{}, map[string]string{}, map[byte]int{}
	}
	if r, ok := p.refs[identity]; ok {
		return r
	}
	p.count[identity[0]]++
	r := fmt.Sprintf("%c%d", identity[0], p.count[identity[0]])
	p.refs[identity], p.byRef[r] = r, identity
	return r
}

// pendingAvailable: the broker pages pending reviews, as its baseline's
// counts showed.
func (m *Model) pendingAvailable() bool {
	return m.reviewSig.counts != nil
}

// readSection asks for one section's page: the first, coalesced, or the
// next one of the current generation.
func (m *Model) readSection(op string, first bool) tea.Cmd {
	f := m.sectionFence(op)
	msg := protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: op}
	page := "more"
	switch {
	case first && *f.firstIn:
		*f.again = true
		return nil
	case first:
		*f.gen++
		*f.firstIn, page = true, "first"
	default:
		msg.ReviewBefore = m.pendingNext(op)
	}
	return m.request(kindReviewPending, msg, op, page, strconv.FormatUint(*f.gen, 10))
}

// fence is a section's generation and its first-page flags.
type fence struct {
	gen            *uint64
	firstIn, again *bool
}

func (m *Model) sectionFence(op string) fence {
	if op == protocol.ReviewOpAwaiting {
		s := &m.sections.needs
		return fence{&s.gen, &s.firstIn, &s.again}
	}
	s := &m.sections.asked
	return fence{&s.gen, &s.firstIn, &s.again}
}

// currentPage settles a page's fence: false for an older generation's page,
// which is dropped, and a re-read when a change arrived during a first.
func (m *Model) currentPage(req pendingRequest) (bool, tea.Cmd) {
	f := m.sectionFence(req.ids[0])
	if req.ids[2] != strconv.FormatUint(*f.gen, 10) {
		return false, nil
	}
	if req.ids[1] != "first" {
		return true, nil
	}
	*f.firstIn = false
	if *f.again {
		*f.again = false
		return false, m.readSection(req.ids[0], true) // this page may predate the change
	}
	return true, nil
}

func (m *Model) pendingNext(op string) string {
	if op == protocol.ReviewOpAwaiting {
		return m.sections.needs.next
	}
	return m.sections.asked.next
}

// refreshPendingSections reads both first pages again while /reviews is
// open: a change never leaves an actionable request behind a stale cursor.
func (m *Model) refreshPendingSections() tea.Cmd {
	if !m.reviewsPanelOpen() {
		return nil
	}
	return tea.Batch(m.readSection(protocol.ReviewOpAwaiting, true), m.readSection(protocol.ReviewOpAsked, true))
}

// reviewPendingResult keeps one section's page; a refusal leaves the
// section as it was.
func (m *Model) reviewPendingResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	current, reread := m.currentPage(req)
	if !current {
		return reread
	}
	first := req.ids[1] == "first"
	switch {
	case resp.ReviewAwaiting != nil:
		page := resp.ReviewAwaiting
		for _, it := range page.Items {
			m.sections.ref(needKey(it)) // a ref is given on arrival, never by drawing order
		}
		m.sections.needs.add(page.Items, first, needKey)
		m.sections.needs.total, m.sections.needs.more, m.sections.needs.next = page.Total, page.More, page.Next
		if first && m.reviewsPanelOpen() {
			m.reviewsShown(page.Token) // the set this page showed, and only while it is on screen
		}
	case resp.ReviewAsked != nil:
		page := resp.ReviewAsked
		for _, it := range page.Items {
			m.sections.ref(askedKey(it))
		}
		m.sections.asked.add(page.Items, first, askedKey)
		m.sections.asked.total, m.sections.asked.more, m.sections.asked.next = page.Total, page.More, page.Next
	default:
		m.status = "pending reviews not read: " + brokerReason(resp)
	}
	return nil
}

// reviewsArg accepts nothing, "more needs", "more asked" or "open REF".
func reviewsArg(a argument) error {
	fields := strings.Fields(string(a))
	switch {
	case len(fields) == 0:
		return nil
	case len(fields) == 2 && fields[0] == "more" && (fields[1] == "needs" || fields[1] == "asked"):
		return nil
	case len(fields) == 2 && fields[0] == "open":
		return nil
	}
	return errors.New("usage: /reviews | /reviews more needs|asked | /reviews open REF")
}

// pendingRef is a section ref: n or a, then a number.
var pendingRef = regexp.MustCompile(`^[na][0-9]+$`)

// reviewsSubcommand runs /reviews more and /reviews open.
func (m *Model) reviewsSubcommand(fields []string) tea.Cmd {
	if fields[0] == "more" {
		op := map[string]string{"needs": protocol.ReviewOpAwaiting, "asked": protocol.ReviewOpAsked}[fields[1]]
		if m.pendingNext(op) == "" {
			m.status = "no more in that section"
			return nil
		}
		return m.readSection(op, false)
	}
	if !pendingRef.MatchString(fields[1]) {
		return m.reviewStatus(fields[1]) // a review here: its detail, as /review status
	}
	return m.openPending(fields[1])
}

// navBatch bounds the older pages one /reviews open reads before it
// asks to be run again.
const navBatch = 20

// navTarget is a request being looked for in its ⇄ tab: its message and
// how many older pages this run has read.
type navTarget struct {
	ref, contact, direction string
	cursor                  int64
	pages                   int
}

// openPending shows a section item: its ⇄ tab, scrolled to its message,
// reading from the newest page back, in batches. It decides nothing.
func (m *Model) openPending(ref string) tea.Cmd {
	target, ok := m.navTargetFor(ref)
	if !ok {
		m.status = ref + " is no longer pending"
		return nil
	}
	if !m.selectView(contactKey(target.contact)) {
		m.status = "can't open that workspace's tab now"
		return nil
	}
	m.panel = nil
	if target.cursor == 0 {
		m.status = prunedStatus
		return nil
	}
	m.sections.nav = &target
	return m.navStep()
}

// prunedStatus: the request's message is gone from this side's history.
const prunedStatus = "that request's message is no longer kept here (history pruned)"

// navTargetFor is where a ref's item is, when a section still holds it.
func (m *Model) navTargetFor(ref string) (navTarget, bool) {
	contact, cursor, ok := m.pendingTarget(m.sections.byRef[ref])
	direction := "in"
	if strings.HasPrefix(ref, "a") {
		direction = "out"
	}
	return navTarget{ref: ref, contact: contact, direction: direction, cursor: cursor}, ok
}

// navStep moves the search on: shows the message once loaded, else reads
// the next older page, until the batch ends or the history does.
func (m *Model) navStep() tea.Cmd {
	n := m.sections.nav
	if n == nil {
		return nil
	}
	t := m.contacts.thread(n.contact)
	switch {
	case m.showNavRow(t, n):
		m.sections.nav = nil
		return nil
	case !t.loaded:
		return nil // the tab's newest page is on its way
	case !t.more || (t.oldest != 0 && t.oldest <= n.cursor):
		m.status, m.sections.nav = prunedStatus, nil
		return nil
	case n.pages >= navBatch:
		m.status, n.pages = n.ref+" is further back: /reviews open "+n.ref+" continues", 0
		m.sections.nav = nil
		return nil
	}
	n.pages++
	return m.refreshContact(n.contact, t.oldest)
}

// showNavRow scrolls the tab to the target's message, when loaded.
func (m *Model) showNavRow(t *contactThread, n *navTarget) bool {
	for _, v := range t.rows {
		if v.Cursor == n.cursor && v.Direction == n.direction {
			m.scrollToRow(t, n)
			m.status = "showing " + n.ref + " in ⇄ " + safe(m.contactTitle(contactKey(n.contact)))
			return true
		}
	}
	return false
}

// scrollToRow puts the target message's lines in the tab's window: the
// one with its cursor and direction, never another with the same ID.
func (m *Model) scrollToRow(t *contactThread, n *navTarget) {
	l := m.contactLines(n.contact)
	room := max(1, m.layout().transcript-len(l.header))
	for _, s := range l.spans {
		if s.cursor == n.cursor && s.direction == n.direction {
			end := min(len(l.lines), s.upto+room/2)
			t.offset = max(0, len(l.lines)-end)
			return
		}
	}
}

// navAfterPage moves the search on after a page of its contact arrived;
// a failed page stops it.
func (m *Model) navAfterPage(contact string, ok bool) tea.Cmd {
	n := m.sections.nav
	if n == nil || n.contact != contact {
		return nil
	}
	if !ok {
		m.status, m.sections.nav = "stopped looking for "+n.ref+": "+m.status, nil
		return nil
	}
	return m.navStep()
}

// leaveNav stops a search when the operator selects another tab.
func (m *Model) leaveNav(key viewKey) {
	if n := m.sections.nav; n != nil && key != contactKey(n.contact) {
		m.stopNav()
	}
}

// stopNav ends a search the operator left: another tab, or Esc.
func (m *Model) stopNav() bool {
	n := m.sections.nav
	if n == nil {
		return false
	}
	m.status, m.sections.nav = "stopped looking for "+n.ref, nil
	return true
}

// reviewsPanelOpen: /reviews shows its sections now.
func (m *Model) reviewsPanelOpen() bool {
	return m.panel != nil && m.panel.kind == panelReviews && m.pendingAvailable()
}

// pendingTarget is where an identity's item is, if a section still holds it.
func (m *Model) pendingTarget(identity string) (string, int64, bool) {
	if it, ok := findItem(m.sections.needs.items, needKey, identity); ok {
		return it.Contact, it.Cursor, true
	}
	if it, ok := findItem(m.sections.asked.items, askedKey, identity); ok {
		return it.Contact, it.Cursor, true
	}
	return "", 0, false
}

// findItem is the item with this identity.
func findItem[T any](items []T, key func(T) string, identity string) (T, bool) {
	for _, it := range items {
		if key(it) == identity {
			return it, true
		}
	}
	var none T
	return none, false
}

// sectionRows are the panel's four sections, the ones needing the operator
// first.
func (m *Model) sectionRows() []string {
	rows := append([]string{needStyle.Render("Needs you")}, m.needRows()...)
	rows = append(rows, titleStyle.Render("Running here"))
	rows = append(rows, m.runningRows()...)
	rows = append(rows, titleStyle.Render("Asked by you"))
	rows = append(rows, m.askedRows()...)
	rows = append(rows, titleStyle.Render("All reviews here"))
	return append(rows, m.listRows()...)
}

func (m *Model) needRows() []string {
	s := &m.sections.needs
	if len(s.items) == 0 {
		return []string{mutedStyle.Render("  none")}
	}
	var rows []string
	for _, it := range s.items {
		rows = append(rows, fmt.Sprintf("  %s  %s  from %s  %s", m.sections.ref(needKey(it)), askLabel(it.Ask),
			safe(it.Sender)+"@"+safe(m.contactTitle(contactKey(it.Contact))), mutedStyle.Render(age(it.ReceivedAt))))
	}
	return append(rows, moreRow(s.more, "needs")...)
}

func (m *Model) askedRows() []string {
	s := &m.sections.asked
	if len(s.items) == 0 {
		return []string{mutedStyle.Render("  none")}
	}
	var rows []string
	for _, it := range s.items {
		row := fmt.Sprintf("  %s  %s  %s", m.sections.ref(askedKey(it)), safe(it.Reviewer)+"@"+safe(m.contactTitle(contactKey(it.Contact))), safe(it.State))
		if it.Verdict != "" {
			row += " " + safe(it.Verdict)
		}
		rows = append(rows, row)
	}
	return append(rows, moreRow(s.more, "asked")...)
}

// runningRows are the reviews here still waiting for or with their reviewer.
func (m *Model) runningRows() []string {
	var rows []string
	for _, r := range m.reviews.list {
		if openReview(r.State) {
			rows = append(rows, reviewRow(r))
		}
	}
	if len(rows) == 0 {
		return []string{mutedStyle.Render("  none")}
	}
	return rows
}

func openReview(state string) bool {
	return state == protocol.ReviewRequested || state == protocol.ReviewReady || state == protocol.ReviewInReview
}

// moreRow says how to read a section's next page, when it has one.
func moreRow(more bool, section string) []string {
	if !more {
		return nil
	}
	return []string{mutedStyle.Render("  more… /reviews more " + section)}
}

// askLabel names a requested change, escaped.
func askLabel(a *protocol.ReviewAsk) string {
	if a == nil {
		return "(request no longer kept)"
	}
	return safe(a.Repository) + "#" + safe(a.Change) + " " + safe(shortSHA(a.Base)) + ".." + safe(shortSHA(a.Head))
}

// reviewsPanelCompletions offers open and more, then the refs to open, those
// needing the operator first, or the sections to page.
func (m *Model) reviewsPanelCompletions(l argLine) []string {
	switch {
	case len(l.words) == 0:
		return withPrefix([]string{"open", "more"}, l.current)
	case len(l.words) == 1 && l.words[0] == "more":
		return withPrefix([]string{"needs", "asked"}, l.current)
	case len(l.words) == 1 && l.words[0] == "open":
		return withPrefix(m.openableRefs(), l.current)
	}
	return nil
}

// openableRefs are the shown items' refs: needing the operator, asked,
// then the reviews here.
func (m *Model) openableRefs() []string {
	var refs []string
	for _, it := range m.sections.needs.items {
		refs = append(refs, m.sections.ref(needKey(it)))
	}
	for _, it := range m.sections.asked.items {
		refs = append(refs, m.sections.ref(askedKey(it)))
	}
	for _, r := range m.reviews.list {
		refs = append(refs, reviewShort(r.ID))
	}
	return refs
}

// reviewCard sets a review request apart in a ⇄ tab: who asked whom, and
// whether it waits for this side's operator, apart from where the review
// stands. Only what the message carries is shown, escaped.
func reviewCard(s contactSides, v protocol.ContactMessageView) []string {
	if v.ReviewAsk == nil {
		return nil
	}
	head := mutedStyle.Render("◇ review request")
	if v.ReviewState == "awaiting" {
		head = needStyle.Render("◆ review request · awaiting you")
	}
	rows := []string{"  " + head, "  " + mutedStyle.Render(reviewParties(s, v))}
	if a := v.ReviewAsk; a.Scope != "" {
		rows = append(rows, "  "+mutedStyle.Render("scope: "+safe(a.Scope)))
	}
	return rows
}

// reviewParties names who asked and who is asked.
func reviewParties(s contactSides, v protocol.ContactMessageView) string {
	if v.Direction == "in" {
		return "asked by " + s.sender(v) + " · of " + safe(strings.Join(v.To, ", ")) + " here"
	}
	return "asked of " + theirAgents(v.To, s.ws)
}
