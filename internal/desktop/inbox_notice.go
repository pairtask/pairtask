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

// notice reads each paired workspace's newest page and counts its unread
// received messages. A workspace whose page can't be read is left out:
// the note never fails the tool it rides on.
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

// unreadWith is c's unread received messages, 0 when it can't be read or
// isn't a paired workspace that carries messages.
func (n *Node) unreadWith(ctx context.Context, c protocol.PairingContactView) int {
	if !c.Confirmed || c.UnpairedAt != nil {
		return 0
	}
	out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: c.IdentityKey},
		protocol.TypeOperatorContactResult)
	if err != nil || out.ContactPage == nil {
		return 0
	}
	return unread(out.ContactPage.Messages)
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
