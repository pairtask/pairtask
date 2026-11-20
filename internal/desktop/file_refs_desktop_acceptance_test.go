package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// File references through the Desktop connector (#419): what the caller
// states travels unchanged, nothing is opened or hashed here, bad metadata
// never reaches the broker, and received references come back as data.

type fileRefsDesktopBroker struct {
	mu       sync.Mutex
	sends    []map[string]any
	received []map[string]any // files on the one message the inbox page holds
}

func fileRefsDesktopNode(t *testing.T, b *fileRefsDesktopBroker) *Node {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var raw map[string]any
			if dec.Decode(&raw) != nil {
				return
			}
			if enc.Encode(b.answer(raw)) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = client.Close(); <-done })
	return &Node{cfg: Config{Dir: t.TempDir()}, fwd: &forwarder{conn: client, in: bufio.NewScanner(client)}}
}

// answer speaks raw JSON, so the fixture works whatever the request and
// page types are called in Go.
func (b *fileRefsDesktopBroker) answer(req map[string]any) map[string]any {
	contact := map[string]any{"identity_key": "key-a", "name": "Work", "confirmed": true}
	switch {
	case req["type"] == protocol.TypeOperatorPairing:
		return map[string]any{"type": protocol.TypeOperatorPairingResult, "pairing_result": map[string]any{"contacts": []any{contact}}}
	case req["contact_op"] == protocol.ContactOpSend:
		b.mu.Lock()
		b.sends = append(b.sends, req)
		b.mu.Unlock()
		return map[string]any{"type": protocol.TypeOperatorContactResult, "contact_message": map[string]any{"id": "sent", "text": req["contact_text"]}}
	case req["contact_op"] == protocol.ContactOpSync:
		return map[string]any{"type": protocol.TypeError, "detail": "offline fixture"}
	}
	message := map[string]any{"id": "in-1", "contact": "key-a", "direction": "in", "text": "see the attached reference", "cursor": 1, "files": b.received}
	return map[string]any{"type": protocol.TypeOperatorContactResult, "contact_page": map[string]any{"contact": "key-a", "messages": []any{message}}}
}

func fileRefsDesktopSample() []map[string]any {
	return []map[string]any{
		{"path": "/does-not-exist/build/report.pdf", "size": float64(1048576), "sha256": strings.Repeat("a", 64)},
		{"path": "notes.txt", "size": float64(0), "sha256": strings.Repeat("b", 64)},
	}
}

// fileRefsDesktopFind reports whether any value under v equals want: the
// test doesn't pin which request field carries the references.
func fileRefsDesktopFind(v any, want []map[string]any) bool {
	plain, _ := json.Marshal(want)
	var target any
	_ = json.Unmarshal(plain, &target)
	return fileRefsDesktopHolds(v, target)
}

// fileRefsDesktopHolds: v is target, or holds it at any depth.
func fileRefsDesktopHolds(v, target any) bool {
	if reflect.DeepEqual(v, target) {
		return true
	}
	for _, child := range fileRefsDesktopChildren(v) {
		if fileRefsDesktopHolds(child, target) {
			return true
		}
	}
	return false
}

// fileRefsDesktopChildren are a JSON object's values or a list's items.
func fileRefsDesktopChildren(v any) []any {
	switch node := v.(type) {
	case map[string]any:
		out := make([]any, 0, len(node))
		for _, child := range node {
			out = append(out, child)
		}
		return out
	case []any:
		return node
	}
	return nil
}

func fileRefsDesktopSend(t *testing.T, n *Node, files []map[string]any) error {
	t.Helper()
	data, err := json.Marshal(map[string]any{"to": "Work", "text": "handing over", "files": files})
	if err != nil {
		t.Fatal(err)
	}
	var args SendArgs
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = n.send(ctx, args)
	return err
}

func (b *fileRefsDesktopBroker) sent(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]map[string]any(nil), b.sends...)
}

func TestFileRefsDesktopAcceptanceSendCarriesCallerMetadataUnchanged(t *testing.T) {
	b := &fileRefsDesktopBroker{}
	files := fileRefsDesktopSample()
	if err := fileRefsDesktopSend(t, fileRefsDesktopNode(t, b), files); err != nil {
		t.Fatalf("a send with references to a path that doesn't exist here must not open it: %v", err)
	}
	sends := b.sent(t)
	if len(sends) != 1 {
		t.Fatalf("sends = %d", len(sends))
	}
	if !fileRefsDesktopFind(sends[0], files) {
		t.Fatalf("references lost or rewritten on the way to the broker: %v", sends[0])
	}
}

func TestFileRefsDesktopAcceptanceInvalidMetadataNeverReachesBroker(t *testing.T) {
	nine := make([]map[string]any, 9)
	for i := range nine {
		nine[i] = map[string]any{"path": "f", "size": float64(1), "sha256": strings.Repeat("a", 64)}
	}
	one := func(path string, size float64, digest string) []map[string]any {
		return []map[string]any{{"path": path, "size": size, "sha256": digest}}
	}
	good := strings.Repeat("a", 64)
	for name, files := range map[string][]map[string]any{
		"nine references":   nine,
		"empty path":        one("", 1, good),
		"control in path":   one("a\x1b[2Jb", 1, good),
		"path over 512":     one(strings.Repeat("p", 513), 1, good),
		"negative size":     one("f", -1, good),
		"size over 2^53-1":  one("f", 9007199254740992, good),
		"short digest":      one("f", 1, strings.Repeat("a", 63)),
		"upper-case digest": one("f", 1, strings.Repeat("A", 64)),
		"non-hex digest":    one("f", 1, strings.Repeat("g", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			b := &fileRefsDesktopBroker{}
			if err := fileRefsDesktopSend(t, fileRefsDesktopNode(t, b), files); err == nil {
				t.Fatal("invalid references accepted")
			}
			if len(b.sent(t)) != 0 {
				t.Fatal("invalid references reached the broker")
			}
		})
	}
}

func TestFileRefsDesktopAcceptanceWithoutReferencesSendIsUnchanged(t *testing.T) {
	b := &fileRefsDesktopBroker{}
	if err := fileRefsDesktopSend(t, fileRefsDesktopNode(t, b), nil); err != nil {
		t.Fatal(err)
	}
	sends := b.sent(t)
	if len(sends) != 1 {
		t.Fatalf("sends = %d", len(sends))
	}
	for key := range sends[0] {
		if strings.Contains(key, "file") {
			t.Fatalf("a send without references carries a references field %q", key)
		}
	}
}

func TestFileRefsDesktopAcceptanceInboxReturnsReceivedReferencesAsData(t *testing.T) {
	files := fileRefsDesktopSample()
	b := &fileRefsDesktopBroker{received: files}
	n := fileRefsDesktopNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	convs, err := n.inbox(ctx, InboxArgs{From: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(convs)
	if err != nil {
		t.Fatal(err)
	}
	var out []struct {
		Messages []struct {
			ID    string           `json:"id"`
			Files []map[string]any `json:"files"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Messages) != 1 {
		t.Fatalf("unexpected inbox: %s", data)
	}
	if !reflect.DeepEqual(out[0].Messages[0].Files, files) {
		t.Fatalf("received references lost or rewritten: got %v want %v", out[0].Messages[0].Files, files)
	}
}
