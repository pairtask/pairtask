package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

func refsHubWithFiles(t *testing.T, target any, files []map[string]any) {
	t.Helper()
	data, err := json.Marshal(target)
	messageWorkerOK(t, err)
	var fields map[string]json.RawMessage
	messageWorkerOK(t, json.Unmarshal(data, &fields))
	fields["files"], err = json.Marshal(files)
	messageWorkerOK(t, err)
	data, err = json.Marshal(fields)
	messageWorkerOK(t, err)
	messageWorkerOK(t, json.Unmarshal(data, target))
}

func refsHubSignedMessage(t *testing.T, signer contactSendSigner, files []map[string]any) protocol.Message {
	t.Helper()
	msg := signer.message("refs-contact-send", "opaque reference metadata")
	msg.ProtocolVersion = 31
	refsHubWithFiles(t, &msg, files)
	link := protocol.PeerLink{Receiver: signer.contact}
	refsHubWithFiles(t, &link, files)
	seal, err := protocol.DecodeSeal(msg.Seal)
	messageWorkerOK(t, err)
	seal.Header.Recipients = link.Recipients()
	msg.Seal = protocol.EncodeSeal(signing.Sign(signer.device, seal.Chain, seal.RootKey, seal.Header))
	return msg
}

func TestFileRefsOperatorSealAcceptanceContactSendRequiresExactSignedReferences(t *testing.T) {
	files := []map[string]any{{"path": "opaque.bin", "size": 7, "sha256": strings.Repeat("a", 64)}}
	cases := []struct {
		name             string
		signed, received []map[string]any
		accepted         bool
	}{
		{"matched", files, files, true}, {"legacy", nil, nil, true},
		{"added-after-signing", nil, files, false}, {"removed-after-signing", files, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, contact, person, device := signedContactSendFixture(t)
			msg := refsHubSignedMessage(t, contactSendSigner{h, contact, person, device}, tc.signed)
			refsHubWithFiles(t, &msg, tc.received)
			out := h.operatorContact(context.Background(), nil, operatorRequest{uid: 0, msg: msg})
			if tc.accepted {
				assertContactSendResult(t, out, msg.Seal)
				assertQueuedContactSeal(t, h, contact, msg.Seal)
				return
			}
			if out.Type != protocol.TypeError || out.Code != protocol.ErrInvalidSeal {
				t.Fatalf("reference metadata did not match the operator seal: type=%s code=%s", out.Type, out.Code)
			}
			rows, _, err := h.store.PeerMessages().Page(context.Background(), contact, 0, 10)
			messageWorkerOK(t, err)
			if len(rows) != 0 {
				t.Fatalf("invalid sealed references were queued: %+v", rows)
			}
		})
	}
}
