//go:build linux

package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestMobilityAcceptanceFreshWorkspaceDoesNotAdvertise(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	lease := mobilityLease(t, f)
	operatorEqual(t, "no stranger advertising", lease["advertise"], any(false))
	operatorEqual(t, "no unrelated browsing", lease["browse"], any(false))
	expiry, err := time.Parse(time.RFC3339Nano, lease["expires_at"].(string))
	operatorNoError(t, err)
	if !expiry.After(time.Now()) || expiry.After(time.Now().Add(91*time.Second)) {
		t.Fatal("lease is absent, expired, or beyond90seconds")
	}
}

func TestMobilityAcceptanceInvitedOnlyListenerAdvertisesWithoutFailures(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	lease := mobilityLease(t, f)
	operatorEqual(t, "listener advertises", lease["advertise"], any(true))
	operatorEqual(t, "listener never needs outbound failure", lease["browse"], any(false))
	encoded, err := json.Marshal(lease)
	operatorNoError(t, err)
	operatorEqual(t, "lease reveals no contact pin", strings.Contains(string(encoded), id.Fingerprint()), false)
	pauseServerContact(t, f, id.Fingerprint(), true)
	operatorEqual(t, "pause withdraws advertising", mobilityLease(t, f)["advertise"], any(false))
}

func TestMobilityAcceptanceOneShotOpsPreserveOperatorSession(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	mobilityLease(t, f)
	result := mobilityCandidate(t, f, "192.168.40.2:7445")
	operatorEqual(t, "no target means no probe", result["outcome"], any("skipped"))
	response, _ := owner.request(protocol.TypeOperatorSnapshot, nil)
	operatorEqual(t, "original operator retains session", response.text("type"), protocol.TypeOperatorSnapshot)
}

func TestMobilityAcceptanceRejectsMalformedCandidateBatches(t *testing.T) {
	tooMany := []string{"192.168.1.1:7445", "192.168.1.2:7445", "192.168.1.3:7445", "192.168.1.4:7445", "192.168.1.5:7445", "192.168.1.6:7445", "192.168.1.7:7445", "192.168.1.8:7445", "192.168.1.9:7445"}
	for _, candidates := range [][]string{nil, {"invalid"}, {"192.168.1.2:7445", "192.168.1.2:7445"}, tooMany} {
		f := newRedeemOperator(t, redeemConfig(), "")
		response := mobilityRequest(t, f, map[string]any{"contact_op": "candidates", "contact_candidates": candidates})
		operatorEqual(t, "invalid candidate batch refused", response.text("type"), protocol.TypeError)
	}
}
