//go:build linux

package server

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedClaimsEmptyReadAcceptanceHasNoVisibilitySummaryWithoutClaim(t *testing.T) {
	f := pairedClaimSetup(t)
	result := pairedClaimCall(f.worker, pairedClaimAction{protocol.TypeClaimGet, f.localID, "", ""})
	operatorEqual(t, "claim read accepted", result.text("type"), protocol.TypeClaimResult)
	for _, field := range []string{"remote_visible", "remote_reason"} {
		if _, present := result[field]; present {
			t.Fatalf("empty claim read carries %s", field)
		}
	}
}
