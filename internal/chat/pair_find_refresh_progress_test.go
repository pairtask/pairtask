package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestFindProgressRefreshUpdatesEmptyScanStatus(t *testing.T) {
	for _, initial := range []string{protocol.ScanPending, protocol.ScanHelperOff} {
		t.Run(initial, func(t *testing.T) {
			now := time.Now()
			first := &protocol.ContactFind{ScanState: initial}
			if initial == protocol.ScanPending {
				first.HelperSeenAt = &now
			}
			m := &Model{}
			m.discoveryResult(pendingRequest{}, protocol.Message{
				Type: protocol.TypeOperatorContactResult, ContactFind: first,
			})
			m.discoveryRefreshed(pendingRequest{}, protocol.Message{
				Type:        protocol.TypeOperatorContactResult,
				ContactFind: &protocol.ContactFind{ScanState: protocol.ScanScanning, HelperSeenAt: &now},
			})
			if !strings.Contains(m.status, "nothing heard yet") {
				t.Fatalf("%s -> scanning left a stale footer: %q", initial, m.status)
			}
		})
	}
}
