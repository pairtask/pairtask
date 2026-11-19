package desktop

import (
	"cmp"
	"context"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Incremental inbox reads. A session that polls shouldn't pay for the whole
// history on every call, nor have to remember what it already reported:
//
//   - after: only the messages newer than a cursor, oldest first, both
//     ways. A pure read.
//   - new_only: the received messages this consumer hasn't been shown,
//     oldest first, and its seen mark moves to the last one returned, so a
//     cut page leaves the rest for the next call. Nothing is marked read.
//   - incoming_only and seals shape what is returned and change nothing.
//
// A consumer is one reader of this node's inbox. Several sessions that
// share a node each name themselves, so one never hides messages from
// another.

// defaultConsumer reads for a session that doesn't name itself.
const defaultConsumer = "default"

var consumerName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// inboxSelection is a checked InboxArgs.
type inboxSelection struct {
	limit        int
	after        int64
	newOnly      bool
	incomingOnly bool
	seals        bool
	consumer     string
}

// selection checks args: a cursor is 0 or more, after and new_only don't
// combine (a high after would skip unseen messages), and a consumer is a
// short lower-case name.
func selection(args InboxArgs) (inboxSelection, error) {
	consumer, err := consumerOf(args.Consumer)
	if err != nil {
		return inboxSelection{}, err
	}
	if args.After < 0 {
		return inboxSelection{}, errors.New("after is a cursor, 0 or more")
	}
	if args.After > 0 && args.NewOnly {
		return inboxSelection{}, errors.New("after and new_only don't combine: new_only continues from this consumer's seen mark")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = inboxDefault
	}
	return inboxSelection{limit: min(limit, inboxMax), after: args.After, newOnly: args.NewOnly,
		incomingOnly: args.IncomingOnly || args.NewOnly, seals: args.Seals, consumer: consumer}, nil
}

// consumerOf is the consumer named, or the default one.
func consumerOf(name string) (string, error) {
	if name == "" {
		return defaultConsumer, nil
	}
	if !consumerName.MatchString(name) {
		return "", errors.New("consumer is 1 to 64 of a-z, 0-9 and -")
	}
	return name, nil
}

// incremental: the read continues from a cursor, oldest first.
func (s inboxSelection) incremental() bool { return s.after > 0 || s.newOnly }

// wants reports whether m is of a direction the selection returns.
func (s inboxSelection) wants(m protocol.ContactMessageView) bool {
	return !s.incomingOnly || m.Direction == "in"
}

// contactPage is one page of the history with contact, the newest when
// before is 0.
func (n *Node) contactPage(ctx context.Context, contact string, before int64) (*protocol.ContactMessagePage, error) {
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList,
		ContactKey: contact, ContactBefore: before}, protocol.TypeOperatorContactResult)
	if err != nil || out.ContactPage == nil {
		return nil, errors.Join(err, missing(out.ContactPage == nil))
	}
	return out.ContactPage, nil
}

// newerPages bounds the pages one incremental read walks back.
const newerPages = 100

var errTooFarBack = errors.New("too many messages are newer than this cursor to page from it; continue from a later cursor")

// newerThan is every message with contact the selection wants and whose
// cursor is above after, oldest first, with the newest page it came from.
// The history is paged newest first, so it walks back until a page reaches
// the cursor or the history ends: ordering is the stored row order, never
// a timestamp.
func (n *Node) newerThan(ctx context.Context, contact string, after int64, sel inboxSelection) ([]protocol.ContactMessageView, *protocol.ContactMessagePage, error) {
	walk := newerWalk{after: after, sel: sel}
	before := int64(0)
	for range newerPages {
		page, err := n.contactPage(ctx, contact, before)
		if err != nil {
			return nil, nil, err
		}
		reached := walk.take(page)
		next, older := olderThan(page, before)
		if reached || !older {
			return walk.oldestFirst(), walk.newest, nil
		}
		before = next
	}
	return nil, nil, errTooFarBack
}

// newerWalk gathers, page by page, the wanted messages above a cursor.
type newerWalk struct {
	after  int64
	sel    inboxSelection
	rows   []protocol.ContactMessageView
	newest *protocol.ContactMessagePage
}

// take keeps page's wanted messages above the cursor, and says whether
// the page reached the cursor: nothing older is above it.
func (w *newerWalk) take(page *protocol.ContactMessagePage) bool {
	if w.newest == nil {
		w.newest = page
	}
	reached := false
	for _, m := range page.Messages {
		reached = reached || m.Cursor <= w.after
		if m.Cursor > w.after && w.sel.wants(m) {
			w.rows = append(w.rows, m)
		}
	}
	return reached
}

func (w *newerWalk) oldestFirst() []protocol.ContactMessageView {
	slices.SortFunc(w.rows, func(a, b protocol.ContactMessageView) int { return cmp.Compare(a.Cursor, b.Cursor) })
	return w.rows
}

// continued is the conversation with c from a cursor on: the oldest
// messages above it, at most the limit, and the cursor to continue from.
func (n *Node) continued(ctx context.Context, c protocol.PairingContactView, after int64, sel inboxSelection) (Conversation, protocol.ContactSyncView, error) {
	rows, page, err := n.newerThan(ctx, c.IdentityKey, after, sel)
	if err != nil {
		return Conversation{}, protocol.ContactSyncView{}, err
	}
	conv := Conversation{Contact: c.IdentityKey, Name: contactName(c), Directory: page.Directory, Unread: unread(page.Messages),
		Messages: rows, Next: after}
	if len(conv.Messages) > sel.limit {
		conv.Messages, conv.More = conv.Messages[:sel.limit], true
	}
	if k := len(conv.Messages); k > 0 {
		conv.Next = conv.Messages[k-1].Cursor
	}
	return conv, page.Sync, nil
}

// shown finishes one conversation for its consumer: a new_only read moves
// the seen mark to the last message it returns, seals are left out unless
// asked for, and a sync failure already reported isn't repeated. It says
// whether the conversation is worth returning: a new_only read with
// nothing new and nothing to report is not.
func (n *Node) shown(conv *Conversation, sel inboxSelection, marks map[string]int64) (bool, error) {
	conv.Seen = marks[conv.Contact]
	if sel.newOnly && len(conv.Messages) > 0 {
		at, err := n.marks().advance(sel.consumer, conv.Contact, conv.Next)
		if err != nil {
			return false, err
		}
		conv.Seen = at
	}
	if !sel.seals {
		conv.Messages = withoutSeals(conv.Messages)
	}
	conv.SyncFailure = n.told.changed(sel.consumer, conv.Contact, conv.SyncFailure)
	return !sel.newOnly || len(conv.Messages) > 0 || conv.SyncFailure != "", nil
}

// withoutSeals is a copy of messages without their seals: about a
// kilobyte each that a reader of the text has no use for.
func withoutSeals(messages []protocol.ContactMessageView) []protocol.ContactMessageView {
	out := slices.Clone(messages)
	for i := range out {
		out[i].Seal = ""
	}
	return out
}

func (n *Node) marks() seenMarks { return seenMarks{dir: n.cfg.Dir} }

// syncNotices remembers, per consumer and workspace, the sync failure last
// reported by this copy, so an idle workspace says it once.
type syncNotices struct {
	mu   sync.Mutex
	last map[string]string
}

// changed is failure when it differs from the one last reported to
// consumer for contact, else "". A read that was fresh clears it, so the
// next failure is reported again.
func (s *syncNotices) changed(consumer, contact, failure string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := consumer + "\x00" + contact
	if s.last[key] == failure {
		return ""
	}
	if s.last == nil {
		s.last = map[string]string{}
	}
	s.last[key] = failure
	return failure
}

// WaitArgs are wait_inbox's arguments.
type WaitArgs struct {
	From           string `json:"from,omitempty" jsonschema:"One paired workspace, by name or identity key; every one if empty"`
	Consumer       string `json:"consumer,omitempty" jsonschema:"The reader whose seen mark decides what is new; the same name it gives check_inbox"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"How long to wait, 1 to 300 seconds; default 60"`
}

// WaitResult is wait_inbox's answer.
type WaitResult struct {
	Ready    bool     `json:"ready"`
	Contacts []string `json:"contacts"` // the workspaces with received messages this consumer hasn't seen
}

const (
	waitDefault = 60 * time.Second
	waitMax     = 300 * time.Second
	// waitPoll is how soon a waiting call notices a message; waitSync how
	// often it asks the workspaces for a session meanwhile, since nothing
	// arrives between this node's own dials.
	waitPoll = 2 * time.Second
	waitSync = 30 * time.Second
)

// waitInbox returns once a received message the consumer hasn't seen
// exists, or when the time is up. It is bounded polling of this node's own
// store: no seen mark moves, nothing is marked read, and a cancelled call
// ends at once.
func (n *Node) waitInbox(ctx context.Context, args WaitArgs) (WaitResult, error) {
	consumer, err := consumerOf(args.Consumer)
	if err != nil {
		return WaitResult{}, err
	}
	timeout, err := waitTimeout(args.TimeoutSeconds)
	if err != nil {
		return WaitResult{}, err
	}
	contacts, err := n.inboxContacts(ctx, args.From)
	if err != nil {
		return WaitResult{}, err
	}
	w := inboxWait{n: n, consumer: consumer, contacts: contacts, deadline: time.Now().Add(timeout)}
	return w.run(ctx)
}

func waitTimeout(seconds int) (time.Duration, error) {
	if seconds == 0 {
		return waitDefault, nil
	}
	if d := time.Duration(seconds) * time.Second; d > 0 && d <= waitMax {
		return d, nil
	}
	return 0, errors.New("timeout_seconds is 1 to 300")
}

// inboxWait is one waiting call.
type inboxWait struct {
	n        *Node
	consumer string
	contacts []protocol.PairingContactView
	deadline time.Time
	synced   time.Time
}

func (w *inboxWait) run(ctx context.Context) (WaitResult, error) {
	for {
		w.sync(ctx)
		unseen, err := w.unseen(ctx)
		if err != nil {
			return WaitResult{}, err
		}
		left := time.Until(w.deadline)
		if len(unseen) > 0 || left <= 0 {
			return WaitResult{Ready: len(unseen) > 0, Contacts: unseen}, nil
		}
		if err := sleep(ctx, min(waitPoll, left)); err != nil {
			return WaitResult{}, err
		}
	}
}

// sync asks each workspace for a session, at most every waitSync.
func (w *inboxWait) sync(ctx context.Context) {
	if time.Since(w.synced) < waitSync {
		return
	}
	w.synced = time.Now()
	for _, c := range w.contacts {
		w.n.requestSync(ctx, c.IdentityKey)
	}
}

// unseen is the workspaces with a received message above the consumer's
// seen mark, never nil.
func (w *inboxWait) unseen(ctx context.Context) ([]string, error) {
	marks, err := w.n.marks().of(w.consumer)
	if err != nil {
		return nil, err
	}
	found := []string{}
	for _, c := range w.contacts {
		rows, _, err := w.n.newerThan(ctx, c.IdentityKey, marks[c.IdentityKey], inboxSelection{incomingOnly: true})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			found = append(found, c.IdentityKey)
		}
	}
	return found, nil
}
