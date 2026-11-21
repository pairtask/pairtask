package bridge

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Independently calculated from the documented domain and binary reference list.
const refsBridgeCommitment = "files-sha512:6e1404dde32cc5a690f86f0a7706561bff30f78207427c25c4fa954a7b4854ae287f96bda57f3700777e9e67bc5db92477f50c278f6f07d554772766e2be11e8"

func refsBridgeMetadata() []map[string]any {
	return []map[string]any{
		{"path": "first.bin", "size": 7, "sha256": strings.Repeat("a", 64)},
		{"path": "second.bin", "size": 9, "sha256": strings.Repeat("b", 64)},
	}
}

func refsBridgeSigned(t *testing.T, signedFiles bool, delivered []map[string]any) bridgeSignedFixture {
	t.Helper()
	f := remoteAgentBridgeFixture(t)
	seal, err := protocol.DecodeSeal(f.message.Seal)
	if err != nil {
		t.Fatal(err)
	}
	if signedFiles {
		seal.Header.Recipients = append(seal.Header.Recipients, refsBridgeCommitment)
	}
	root := bridgeIdentityKey("remote-agent-workspace").Public().(ed25519.PublicKey)
	f.message.Seal = protocol.EncodeSeal(signing.Sign(bridgeIdentityKey("remote-agent-broker"), seal.Chain, root, seal.Header))
	f.message.Payload, err = json.Marshal(map[string]any{"text": "hello from a paired agent", "files": delivered})
	if err != nil {
		t.Fatal(err)
	}
	// A relay's claim is never the recipient's verdict.
	f.message.Verification = &protocol.Verification{State: signing.Verified}
	return f
}

func TestFileRefsBridgeSealAcceptanceMatchingAndLegacyControls(t *testing.T) {
	for _, signed := range []bool{false, true} {
		var files []map[string]any
		if signed {
			files = refsBridgeMetadata()
		}
		f := refsBridgeSigned(t, signed, files)
		requireBridgeState(t, f.deliver(t), signing.Verified)
	}
}

func TestFileRefsBridgeSealAcceptanceRelayCannotChangeReferences(t *testing.T) {
	for _, change := range []string{"removed", "path", "size", "digest", "reordered", "added-to-legacy"} {
		t.Run(change, func(t *testing.T) {
			files := refsBridgeMetadata()
			switch change {
			case "removed":
				files = nil
			case "path":
				files[0]["path"] = "elsewhere.bin"
			case "size":
				files[0]["size"] = 8
			case "digest":
				files[0]["sha256"] = strings.Repeat("c", 64)
			case "reordered":
				files[0], files[1] = files[1], files[0]
			}
			f := refsBridgeSigned(t, change != "added-to-legacy", files)
			requireBridgeState(t, f.deliver(t), signing.Invalid)
		})
	}
}
