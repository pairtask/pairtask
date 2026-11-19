package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type incrementalFixture struct {
	node    *Node
	dir     string
	contact string
	at      time.Time
}

func incrementalOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func incrementalStart(t *testing.T, dir string) *Node {
	t.Helper()
	n, err := Start(context.Background(), Config{Dir: dir, Name: "inbox-fixture"})
	incrementalOK(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func newIncrementalFixture(t *testing.T) *incrementalFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "pt-inbox-")
	incrementalOK(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &incrementalFixture{dir: filepath.Join(root, "node"), contact: "sha256:" + strings.Repeat("a", 64), at: time.Now()}
	f.node = incrementalStart(t, f.dir)
	st, ctx := f.node.own.st, context.Background()
	_, err = st.Commit(ctx, peer.PairingOutcome{OperationID: "inbox-pair", InvitationID: "inbox-invite", IdentityKey: f.contact,
		Name: "Inbox Contact", TranscriptID: make([]byte, 64), At: f.at})
	incrementalOK(t, err)
	_, err = st.Accept(ctx, f.contact)
	incrementalOK(t, err)
	_, err = st.Deliver(ctx, f.contact, peer.PairingAccepted, f.at)
	incrementalOK(t, err)
	return f
}

func (f *incrementalFixture) incoming(t *testing.T, seq int) {
	t.Helper()
	answer, err := f.node.own.st.PeerMessages().Accept(context.Background(), f.contact,
		messaging.Message{ID: fmt.Sprintf("in-%d", seq), N: uint64(seq), CreatedAt: f.at, Text: "incoming"}, f.at)
	incrementalOK(t, err)
	if answer.Refusal.Reason != "" {
		t.Fatalf("fixture message refused: %+v", answer)
	}
}

func (f *incrementalFixture) outgoing(t *testing.T, seq int) {
	t.Helper()
	_, err := f.node.own.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: f.contact, ID: fmt.Sprintf("out-%d", seq), Text: "outgoing", Now: f.at})
	incrementalOK(t, err)
}

// JSON keeps the packet executable against the pre-feature Args as well:
// old code ignores the new fields and fails the behavior assertions.
func incrementalRead(t *testing.T, n *Node, fields map[string]any) []Conversation {
	t.Helper()
	data, err := json.Marshal(fields)
	incrementalOK(t, err)
	var args InboxArgs
	incrementalOK(t, json.Unmarshal(data, &args))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	got, err := n.inbox(ctx, args)
	incrementalOK(t, err)
	return got
}

func incrementalIDs(t *testing.T, rows []Conversation, want ...string) {
	t.Helper()
	var got []string
	for _, conv := range rows {
		for _, msg := range conv.Messages {
			got = append(got, msg.ID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("message IDs = %v, want %v", got, want)
	}
}

func incrementalPosition(t *testing.T, row Conversation) (int64, int64) {
	t.Helper()
	data, err := json.Marshal(row)
	incrementalOK(t, err)
	var positions struct{ Next, Seen int64 }
	incrementalOK(t, json.Unmarshal(data, &positions))
	return positions.Next, positions.Seen
}

func incrementalPageEnd(t *testing.T, row Conversation) {
	t.Helper()
	next, seen := incrementalPosition(t, row)
	if next == 0 {
		t.Fatal("page has no continuation cursor")
	}
	if seen != next {
		t.Fatalf("seen = %d, want next %d", seen, next)
	}
	last := row.Messages[len(row.Messages)-1].Cursor
	if next != last {
		t.Fatalf("next = %d, want last returned cursor %d", next, last)
	}
}

func TestIncrementalInboxAcceptancePagesOldestIncomingWithoutSkipping(t *testing.T) {
	f := newIncrementalFixture(t)
	for i := 1; i <= 4; i++ {
		f.incoming(t, i)
		f.outgoing(t, i)
	}
	args := map[string]any{"new_only": true, "consumer": "worker-a", "limit": 2}
	first := incrementalRead(t, f.node, args)
	incrementalIDs(t, first, "in-1", "in-2")
	if len(first) != 1 || !first[0].More {
		t.Fatalf("first page must report more: %+v", first)
	}
	incrementalPageEnd(t, first[0])
	second := incrementalRead(t, f.node, args)
	incrementalIDs(t, second, "in-3", "in-4")
	if len(second) != 1 || second[0].More {
		t.Fatalf("last page must end: %+v", second)
	}
	empty := incrementalRead(t, f.node, args)
	if len(empty) != 0 {
		t.Fatalf("idle read must omit empty conversations: %+v", empty)
	}
}

func TestIncrementalInboxAcceptanceSeenSurvivesRestartWithoutReadReceipts(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	args := map[string]any{"new_only": true, "consumer": "persistent"}
	incrementalIDs(t, incrementalRead(t, f.node, args), "in-1")
	page, _, err := f.node.own.st.PeerMessages().CurrentPage(context.Background(), f.contact, 0, 50)
	incrementalOK(t, err)
	if len(page) != 1 || page[0].ReadAt != nil {
		t.Fatalf("peek changed stored read state: %+v", page)
	}
	reads, err := f.node.own.st.PeerMessages().PendingReads(context.Background(), f.contact, 50)
	incrementalOK(t, err)
	if len(reads) != 0 {
		t.Fatalf("peek created receipts: %v", reads)
	}
	incrementalOK(t, f.node.Close())
	f.node = incrementalStart(t, f.dir)
	incrementalIDs(t, incrementalRead(t, f.node, args))
}

func TestIncrementalInboxAcceptanceSharedCopiesKeepConsumersIndependent(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	other := incrementalStart(t, f.dir)
	a := map[string]any{"new_only": true, "consumer": "chat"}
	b := map[string]any{"new_only": true, "consumer": "cowork"}
	incrementalIDs(t, incrementalRead(t, f.node, a), "in-1")
	incrementalIDs(t, incrementalRead(t, other, b), "in-1")
	incrementalIDs(t, incrementalRead(t, other, a))
	incrementalIDs(t, incrementalRead(t, f.node, b))
}

func TestIncrementalInboxAcceptanceExplicitAfterIsPureAndIncludesOutgoing(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	f.outgoing(t, 1)
	f.incoming(t, 2)
	all := incrementalRead(t, f.node, map[string]any{})
	var firstCursor int64
	for _, message := range all[0].Messages {
		if message.ID == "in-1" {
			firstCursor = message.Cursor
		}
	}
	page := incrementalRead(t, f.node, map[string]any{"after": firstCursor, "limit": 1})
	incrementalIDs(t, page, "out-1")
	next, seen := incrementalPosition(t, page[0])
	if next != page[0].Messages[0].Cursor || seen != 0 {
		t.Fatalf("pure outgoing page next/seen = %d/%d", next, seen)
	}
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"after": next}), "in-2")
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"new_only": true}), "in-1", "in-2")
}

func TestIncrementalInboxAcceptanceInvalidSelectorsAreRefused(t *testing.T) {
	f := newIncrementalFixture(t)
	for _, raw := range []string{`{"after":-1}`, `{"after":1,"new_only":true}`, `{"consumer":"../escape"}`, `{"consumer":"UPPER"}`} {
		var args InboxArgs
		incrementalOK(t, json.Unmarshal([]byte(raw), &args))
		if _, err := f.node.inbox(context.Background(), args); err == nil {
			t.Errorf("invalid selectors accepted: %s", raw)
		}
	}
}

func TestIncrementalInboxAcceptanceIncomingFilterDoesNotMoveSeen(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	f.outgoing(t, 1)
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"incoming_only": true}), "in-1")
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"new_only": true}), "in-1")
}

func TestIncrementalInboxAcceptanceOldestUnreadBeyondNewestStoragePage(t *testing.T) {
	f := newIncrementalFixture(t)
	for i := 1; i <= 71; i++ {
		f.incoming(t, i)
	}
	incrementalIDs(t, incrementalRead(t, f.node, map[string]any{"new_only": true, "limit": 1}), "in-1")
}

func TestIncrementalInboxAcceptanceSealProjectionPreservesStoredBytes(t *testing.T) {
	f := newIncrementalFixture(t)
	// This is an opaque stored envelope fixture, not signature validation.
	seal := bytes.Repeat([]byte{42}, 1024)
	_, err := f.node.own.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{
		Contact: f.contact, ID: "sealed-out", Text: "sealed fixture", Now: f.at, Seal: seal})
	incrementalOK(t, err)
	without := incrementalRead(t, f.node, map[string]any{})
	incrementalIDs(t, without, "sealed-out")
	if without[0].Messages[0].Seal != "" {
		t.Fatal("default inbox included the large seal")
	}
	with := incrementalRead(t, f.node, map[string]any{"seals": true})
	if with[0].Messages[0].Seal != base64.StdEncoding.EncodeToString(seal) {
		t.Fatal("explicit seal projection did not preserve the stored envelope")
	}
	rows, _, err := f.node.own.st.PeerMessages().CurrentPage(context.Background(), f.contact, 0, 50)
	incrementalOK(t, err)
	if len(rows) != 1 || !bytes.Equal(rows[0].Seal, seal) {
		t.Fatal("response projection altered the stored seal")
	}
}
