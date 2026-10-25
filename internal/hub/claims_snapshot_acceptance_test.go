package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestTaskClaimsAcceptanceSnapshotFitsEscapedValidFields(t *testing.T) {
	snap := protocol.OperatorSnapshot{ClaimsTotal: 32}
	for i := 0; i < 32; i++ {
		parent := strings.Repeat("<", 125) + fmt.Sprintf("%03d", i)
		set := protocol.ClaimSet{OperatorMessageID: parent, ClaimPage: protocol.ClaimPage{Total: 16}}
		for j := 0; j < 16; j++ {
			set.Claims = append(set.Claims, protocol.Claim{
				OperatorMessageID: parent, Task: strings.Repeat("a", 61) + fmt.Sprintf("%03d", j),
				Topic: strings.Repeat("&", 120), Role: "owner", AgentID: strings.Repeat("<", 125) + fmt.Sprintf("%03d", j),
				State: "active", ClaimedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
			})
		}
		snap.Claims = append(snap.Claims, set)
	}
	bounded, err := fitSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: strings.Repeat("r", 128), Snapshot: &bounded})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame)+1 > protocol.MaxFrameBytes {
		t.Fatalf("snapshot with bounded valid claims exceeds wire limit: %d > %d", len(frame)+1, protocol.MaxFrameBytes)
	}
	if bounded.ClaimsTotal != 32 || !bounded.ClaimsTruncated || len(bounded.Claims) >= 32 {
		t.Fatal("claim truncation lost its original total or was not reported")
	}
}
