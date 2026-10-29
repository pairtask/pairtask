package hub

import (
	"encoding/json"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func contactEncoded(t *testing.T, out protocol.Message) []byte {
	t.Helper()
	encoded, err := json.Marshal(out)
	messageWorkerOK(t, err)
	return encoded
}
func contactBoundaryPage(t *testing.T) protocol.Message {
	t.Helper()
	page := &protocol.ContactMessagePage{Messages: []protocol.ContactMessageView{}}
	out := protocol.Message{Type: protocol.TypeOperatorContactResult, ContactOp: protocol.ContactOpList, ContactPage: page}
	for i := 0; i < 10; i++ {
		page.Messages = append(page.Messages, protocol.ContactMessageView{Cursor: int64(11 - i), Text: strings.Repeat("\x00", protocol.ContactTextBytes)})
	}
	page.Messages = append(page.Messages, protocol.ContactMessageView{Cursor: 1})
	// Valid text puts the page below the former 256-byte reserve; the
	// accepted correlation ID requires more room after JSON escaping.
	remaining := protocol.MaxFrameBytes - 300 - len(contactEncoded(t, out))
	messageSyncEqual(t, remaining > 0, true)
	text := strings.Repeat("\x00", remaining/6) + strings.Repeat("x", remaining%6)
	messageSyncEqual(t, len(text) <= protocol.ContactTextBytes, true)
	page.Messages[10].Text = text
	return out
}
func TestContactPageAcceptanceEscapedSessionEnvelope(t *testing.T) {
	out := contactBoundaryPage(t)
	requestID := strings.Repeat("\x00", 128)
	messageWorkerOK(t, fitContactPage(&out, requestID))
	out.RequestID = requestID
	messageSyncEqual(t, len(contactEncoded(t, out))+1 <= protocol.MaxFrameBytes, true)
	messageSyncEqual(t, len(out.ContactPage.Messages), 10)
	messageSyncEqual(t, out.ContactPage.More, true)
}
