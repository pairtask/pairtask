package desktop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestInboxNoticeAcceptanceSkipsUnreadableContactPage(t *testing.T) {
	root, err := os.MkdirTemp("", "pt-unread-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	n, err := Start(context.Background(), Config{Dir: filepath.Join(root, "node"), Name: "Desktop"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The broker refuses a page for an unknown key. The notice leaves that
	// contact out and does not disrupt the node's pairing tool.
	bad := protocol.PairingContactView{IdentityKey: "unknown-contact", Name: "Unreadable", Confirmed: true}
	if got := n.notice(ctx, []protocol.PairingContactView{bad}); got.Unread != 0 || got.UnreadFrom != "" {
		t.Fatalf("unreadable contact contributed to notice: %#v", got)
	}
	if _, err := n.pairing(ctx); err != nil {
		t.Fatalf("pairing after unreadable contact page: %v", err)
	}
}
