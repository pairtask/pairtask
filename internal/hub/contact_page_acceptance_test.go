package hub

import (
	"encoding/json"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestContactPageAcceptanceEscapedSessionEnvelope(t *testing.T) {
	page := &protocol.ContactMessagePage{Messages: []protocol.ContactMessageView{}}
	out := protocol.Message{Type: protocol.TypeOperatorContactResult, ContactOp: protocol.ContactOpList, ContactPage: page}
	for i := 0; i < 10; i++ {
		page.Messages = append(page.Messages, protocol.ContactMessageView{Cursor: int64(11 - i), Text: strings.Repeat("\x00", protocol.ContactTextBytes)})
	}
	page.Messages = append(page.Messages, protocol.ContactMessageView{Cursor: 1})
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// A valid final message puts this page just below the current 256-byte
	// reserve; an accepted 128-byte correlation ID needs more after escaping.
	remaining := protocol.MaxFrameBytes - 300 - len(encoded)
	text := strings.Repeat("\x00", remaining/6) + strings.Repeat("x", remaining%6)
	if len(text) > protocol.ContactTextBytes || remaining <= 0 {
		t.Fatal("invalid boundary fixture")
	}
	page.Messages[10].Text = text
	requestID := strings.Repeat("\x00", 128)
	if err := fitContactPage(&out, requestID); err != nil {
		t.Fatal(err)
	}
	out.RequestID = requestID
	encoded, err = json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded)+1 > protocol.MaxFrameBytes {
		t.Fatalf("encoded response %d exceeds frame %d", len(encoded)+1, protocol.MaxFrameBytes)
	}
	if len(page.Messages) != 10 || !page.More {
		t.Fatalf("expected whole-row trimming with More: %d %v", len(page.Messages), page.More)
	}
}
