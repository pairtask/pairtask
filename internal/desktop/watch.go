package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The owning copy watches for messages from paired workspaces and tells
// the person, since a Desktop chat only looks when they write. It follows
// the broker's operator events in process; a notice says how many are
// unread and from which workspace, never what they say.

// notifyWindow: at most one notice per workspace this often; a rise inside
// it is noticed when it ends.
const notifyWindow = 30 * time.Second

// noticeTitle heads every notice.
const noticeTitle = "pairtask"

// watcher is what the notices remember: per workspace, its unread count
// when last noticed or checked, when its last notice went out, and
// whether a rise waits for its window to end.
type watcher struct {
	call     func(context.Context, protocol.Message) protocol.Message
	notify   Notifier
	log      *slog.Logger
	now      func() time.Time
	seen     map[string]int
	last     map[string]time.Time
	held     map[string]bool
	baseline bool
	unknown  map[string]bool // at start, but its page couldn't be read then
	quiet    bool            // a notice failed once; later failures aren't logged
}

func newWatcher(call func(context.Context, protocol.Message) protocol.Message, notify Notifier, log *slog.Logger) *watcher {
	return &watcher{call: call, notify: notify, log: log, now: time.Now,
		seen: map[string]int{}, last: map[string]time.Time{}, held: map[string]bool{}, unknown: map[string]bool{}}
}

// watch follows the broker's events for as long as the owner runs. The
// node opens no other operator session, so another owning it is logged.
func (o *owner) watch(notify Notifier) {
	defer o.wg.Done()
	sess := hub.NewOperatorSession(os.Getuid())
	open := protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: uuid.NewString(), ProtocolVersion: protocol.Version}
	if resp := o.hub.OpenOperatorSession(o.ctx, open, sess); resp.Type != protocol.TypeOperatorSession {
		o.log.Warn("notifications off: the operator session is unavailable", "code", resp.Code)
		return
	}
	defer o.hub.CloseOperatorSession(sess)
	w := newWatcher(o.request, notify, o.log)
	w.run(o.ctx, o.hub, sess)
}

// request is one operator request from the watcher, stamped as Call
// stamps a tool's.
func (o *owner) request(ctx context.Context, msg protocol.Message) protocol.Message {
	msg.ProtocolVersion, msg.MessageID = protocol.Version, uuid.NewString()
	return o.call(ctx, msg)
}

// run asks for a baseline, then handles the session's frames and lets
// held rises out as their windows end.
func (w *watcher) run(ctx context.Context, h *hub.Hub, sess *hub.OperatorSession) {
	frames := make(chan protocol.Message)
	go func() {
		defer close(frames)
		for msg, ok := sess.Next(ctx); ok; msg, ok = sess.Next(ctx) {
			sess.Handed(msg) // releases its request ID, as the socket's writer does
			frames <- msg
		}
	}()
	w.resync(ctx, h, sess)
	tick := time.NewTicker(notifyWindow / 6)
	defer tick.Stop()
	for {
		select {
		case msg, ok := <-frames:
			if !ok {
				return
			}
			w.frame(ctx, h, sess, msg)
		case <-tick.C:
			w.release(ctx)
		}
	}
}

// resync asks for a new baseline; events flow only after one.
func (w *watcher) resync(ctx context.Context, h *hub.Hub, sess *hub.OperatorSession) {
	id := uuid.NewString()
	if sess.Begin(id) == nil {
		h.SessionRequest(ctx, sess, protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: id, ProtocolVersion: protocol.Version})
	}
}

// frame handles one session frame: a baseline, a workspace's messages
// changing, or events lost.
func (w *watcher) frame(ctx context.Context, h *hub.Hub, sess *hub.OperatorSession, msg protocol.Message) {
	switch {
	case msg.Type == protocol.TypeOperatorSnapshot:
		w.checkAll(ctx)
	case msg.Event == nil:
	case msg.Event.Kind == protocol.EventContactMessagesChanged:
		w.check(ctx, msg.Event.ContactKey)
	case msg.Event.Kind == protocol.EventResyncRequired:
		w.resync(ctx, h, sess)
	}
}

// checkAll checks every workspace; the first time, it only records them,
// so nothing already unread at start is announced.
func (w *watcher) checkAll(ctx context.Context) {
	out := w.call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.PairingResult == nil {
		return
	}
	for _, c := range out.PairingResult.Contacts {
		if !carriesMessages(c) {
			continue
		}
		if w.baseline {
			w.check(ctx, c.IdentityKey)
		} else if count, ok := w.unread(ctx, c.IdentityKey); ok {
			w.seen[c.IdentityKey] = count
		} else {
			w.unknown[c.IdentityKey] = true
		}
	}
	w.baseline = true
}

// carriesMessages: a workspace paired on both sides and not unpaired here,
// the ones whose messages are counted.
func carriesMessages(c protocol.PairingContactView) bool {
	return c.Confirmed && c.UnpairedAt == nil
}

// check notices a workspace whose unread count rose, unless its window is
// still open; a fall is only remembered. A page that can't be read changes
// nothing, so its messages aren't announced again once it can; for a
// workspace there at start whose page couldn't be read then, the first
// page that can is its baseline. A workspace paired later starts at none,
// so its first message is announced.
func (w *watcher) check(ctx context.Context, contact string) {
	count, ok := w.unread(ctx, contact)
	switch {
	case !ok:
	case w.unknown[contact]:
		delete(w.unknown, contact)
		w.seen[contact] = count
	case count <= w.seen[contact]:
		w.seen[contact] = count
	case w.now().Sub(w.last[contact]) < notifyWindow:
		w.held[contact] = true
	default:
		w.announce(ctx, contact, count)
	}
}

// release checks the workspaces whose held rise's window has ended.
func (w *watcher) release(ctx context.Context) {
	for contact := range w.held {
		if w.now().Sub(w.last[contact]) >= notifyWindow {
			delete(w.held, contact)
			w.check(ctx, contact)
		}
	}
}

// announce tells the person and remembers it.
func (w *watcher) announce(ctx context.Context, contact string, count int) {
	w.seen[contact], w.last[contact] = count, w.now()
	text := fmt.Sprintf("%d unread from %s", count, w.name(ctx, contact))
	if err := w.notify(ctx, noticeTitle, text); err != nil && !w.quiet {
		w.quiet = true
		w.log.Debug("notification not shown; later failures aren't logged", "error", err)
	}
}

// unread is a workspace's unread received messages, and whether its page
// could be read.
func (w *watcher) unread(ctx context.Context, contact string) (int, bool) {
	out := w.call(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: contact})
	if out.ContactPage == nil {
		return 0, false
	}
	return unread(out.ContactPage.Messages), true
}

// name is what the person calls a workspace, at most 64 runes, else its
// short key.
func (w *watcher) name(ctx context.Context, contact string) string {
	out := w.call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.PairingResult != nil {
		for _, c := range out.PairingResult.Contacts {
			if c.IdentityKey == contact {
				return clip(contactName(c), 64)
			}
		}
	}
	return protocol.ShortKey(contact)
}

// clip is s cut to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
