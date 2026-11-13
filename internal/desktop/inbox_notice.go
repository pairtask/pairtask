package desktop

import (
	"context"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A Desktop chat acts only when its person writes, so a message that
// arrived meanwhile would wait unseen: every tool but check_inbox and
// mark_read says how many received messages are still unread, and from
// whom, so Claude can mention them on its next turn.

// InboxNotice is the received messages not yet marked read, across the
// paired workspaces; empty when there are none.
type InboxNotice struct {
	Unread     int    `json:"unread,omitempty"`
	UnreadFrom string `json:"unread_from,omitempty"` // display names, sorted, comma-separated
}

// unreadNote ends the descriptions of the tools that carry the notice.
const unreadNote = " The unread field counts received messages not yet marked read, and unread_from names the workspaces they came from; call check_inbox to read them."

// notice counts each paired workspace's unread received messages across
// its history. A workspace whose pages can't be read is left out: the
// note never fails the tool it rides on.
func (n *Node) notice(ctx context.Context, contacts []protocol.PairingContactView) InboxNotice {
	var note InboxNotice
	var from []string
	for _, c := range contacts {
		if k := n.unreadWith(ctx, c); k > 0 {
			note.Unread += k
			from = append(from, contactName(c))
		}
	}
	slices.Sort(from)
	note.UnreadFrom = strings.Join(from, ", ")
	return note
}

// unreadWith is c's unread received messages, 0 when they can't be read or
// it isn't a paired workspace that carries messages.
func (n *Node) unreadWith(ctx context.Context, c protocol.PairingContactView) int {
	if !c.Confirmed || c.UnpairedAt != nil {
		return 0
	}
	count, _ := countUnread(ctx, n.Call, c.IdentityKey)
	return count
}

// unreadPages bounds the pages countUnread reads for one workspace.
const unreadPages = 100

// countUnread is a workspace's unread received messages across every page
// of its history, and whether all of it could be read. It stops at the
// last page, or at one that adds nothing older; a history longer than
// unreadPages pages isn't counted, so no notice claims a partial total.
func countUnread(ctx context.Context, call func(context.Context, protocol.Message) protocol.Message, contact string) (int, bool) {
	total, before := 0, int64(0)
	for range unreadPages {
		out := call(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: contact, ContactBefore: before})
		page := out.ContactPage
		if page == nil {
			return 0, false
		}
		total += unread(page.Messages)
		next, ok := olderThan(page, before)
		if !ok {
			return total, true
		}
		before = next
	}
	return 0, false
}

// olderThan is the cursor that pages past page, when there is more and it
// moves older than before.
func olderThan(page *protocol.ContactMessagePage, before int64) (int64, bool) {
	if !page.More || len(page.Messages) == 0 {
		return 0, false
	}
	next := page.Messages[0].Cursor
	for _, m := range page.Messages {
		next = min(next, m.Cursor)
	}
	return next, before == 0 || next < before
}

// freshNotice is the notice from a new pairing snapshot, empty when even
// that can't be read.
func (n *Node) freshNotice(ctx context.Context) InboxNotice {
	snap, err := n.pairing(ctx)
	if err != nil {
		return InboxNotice{}
	}
	return n.notice(ctx, snap.Contacts)
}
