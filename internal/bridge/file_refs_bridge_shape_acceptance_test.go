package bridge

import (
	"encoding/json"
	"testing"

	"github.com/pairtask/pairtask/internal/signing"
)

func TestFileRefsBridgeShapeAcceptanceEmptyListMeansNoReferences(t *testing.T) {
	f := refsBridgeSigned(t, false, []map[string]any{})
	requireBridgeState(t, f.deliver(t), signing.Verified)
}

func TestFileRefsBridgeShapeAcceptanceMalformedMetadataIsInvalid(t *testing.T) {
	for name, field := range map[string]string{
		"string":           `"not-a-list"`,
		"object":           `{}`,
		"bad-size-type":    `[{"path":"first.bin","size":"seven","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`,
		"nonintegral-size": `[{"path":"first.bin","size":7.5,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := refsBridgeSigned(t, false, nil)
			f.message.Payload = json.RawMessage(`{"text":"hello from a paired agent","files":` + field + `}`)
			requireBridgeState(t, f.deliver(t), signing.Invalid)
		})
	}
}
