package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorContactAcceptanceSendPreservesStableIDAndText(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	requests := make(chan protocol.Message, 1)
	path := statusSocket(t, func(c net.Conn) {
		var req protocol.Message
		if json.NewDecoder(c).Decode(&req) != nil {
			return
		}
		requests <- req
		json.NewEncoder(c).Encode(protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: req.MessageID,
			ContactMessage: &protocol.ContactMessageView{ID: req.MessageID, Contact: key, State: "queued", Direction: "out"}})
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "contact", "send", key, "line one\nline two", "--message-id", "stable-contact-id", "--socket", path, "--json"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("contact send failed: %s", stderr.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid JSON result: %s", out.String())
	}
	select {
	case req := <-requests:
		assertStatusEqual(t, "operator-only frame", req.Type, protocol.TypeOperatorContact)
		assertStatusEqual(t, "contact operation", req.ContactOp, protocol.ContactOpSend)
		assertStatusEqual(t, "full identity", req.ContactKey, key)
		assertStatusEqual(t, "stable message ID", req.MessageID, "stable-contact-id")
		assertStatusEqual(t, "exact text", req.ContactText, "line one\nline two")
	case <-time.After(time.Second):
		t.Fatal("missing contact request")
	}
}

func TestOperatorContactAcceptanceRemoteTextCannotControlTerminal(t *testing.T) {
	var out bytes.Buffer
	writeContact(&out, protocol.Message{ContactPage: &protocol.ContactMessagePage{Messages: []protocol.ContactMessageView{{
		ID: "remote", Direction: "in", State: "received", Text: "hello\x1b[2J\rforged\x07\nworld", CreatedAt: time.Now(),
	}}}})
	for _, control := range []rune{'\x1b', '\r', '\x07'} {
		if strings.ContainsRune(out.String(), control) {
			t.Fatalf("remote control byte %U reached terminal: %q", control, out.String())
		}
	}
	if !strings.Contains(out.String(), "hello") || !strings.Contains(out.String(), "world") {
		t.Fatalf("text lost: %q", out.String())
	}
}
