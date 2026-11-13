package desktop

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestInboxNoticeAcceptanceCountsUnreadAcrossContactPages(t *testing.T) {
	readAt := time.Now()
	f := &unreadPagesFixture{t: t, pages: map[int64]*protocol.ContactMessagePage{
		0: {Messages: []protocol.ContactMessageView{{Direction: "in", Cursor: 5}, {Direction: "in", Cursor: 4, ReadAt: &readAt}}, More: true},
		4: {Messages: []protocol.ContactMessageView{{Direction: "in", Cursor: 3}, {Direction: "out", Cursor: 2}}, More: true},
		2: {Messages: []protocol.ContactMessageView{{Direction: "in", Cursor: 1}}},
	}}
	count, ok := countUnread(context.Background(), f.call, "alpha-key")
	if !ok || count != 3 {
		t.Fatalf("unread across pages = (%d, %v), want (3, true)", count, ok)
	}
	if !slices.Equal(f.cursors, []int64{0, 4, 2}) {
		t.Fatalf("contact page cursors = %v, want [0 4 2]", f.cursors)
	}
}

type unreadPagesFixture struct {
	t       *testing.T
	pages   map[int64]*protocol.ContactMessagePage
	cursors []int64
}

func (f *unreadPagesFixture) call(_ context.Context, req protocol.Message) protocol.Message {
	f.t.Helper()
	if req.Type != protocol.TypeOperatorContact {
		f.t.Fatalf("unexpected contact request type %q", req.Type)
	}
	if req.ContactOp != protocol.ContactOpList {
		f.t.Fatalf("unexpected contact operation %q", req.ContactOp)
	}
	if req.ContactKey != "alpha-key" {
		f.t.Fatalf("unexpected contact key %q", req.ContactKey)
	}
	f.cursors = append(f.cursors, req.ContactBefore)
	page := f.pages[req.ContactBefore]
	if page == nil {
		f.t.Fatalf("unexpected contact cursor %d", req.ContactBefore)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, ContactPage: page}
}

func TestInboxNoticeAcceptanceDiscardsPartialCountWhenOlderPageFails(t *testing.T) {
	call := func(_ context.Context, req protocol.Message) protocol.Message {
		if req.ContactBefore == 0 {
			return protocol.Message{Type: protocol.TypeOperatorContactResult,
				ContactPage: &protocol.ContactMessagePage{Messages: []protocol.ContactMessageView{{Direction: "in", Cursor: 2}}, More: true}}
		}
		return protocol.Message{Type: protocol.TypeError}
	}
	if count, ok := countUnread(context.Background(), call, "alpha-key"); ok || count != 0 {
		t.Fatalf("partial unread count after page failure = (%d, %v), want (0, false)", count, ok)
	}
}
