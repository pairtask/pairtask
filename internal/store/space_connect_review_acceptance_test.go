package store_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestSpaceInstallReviewCodeOperationPrefixCannotChangePairingID(t *testing.T) {
	// Code-pairing IDs and stored transcripts were opaque before spaces.
	// Neither a prefix nor the full new spelling may reinterpret old rows.
	canonical := "space:" + installSpaceID + ":" + strings.Repeat("1", 32)
	for name, tc := range map[string]struct {
		operation string
		size      int
	}{
		"prefix":                           {"space:caller-chosen-code-operation", 64},
		"canonical-shape":                  {canonical, 64},
		"canonical-shape-short-transcript": {canonical, 32},
	} {
		t.Run(name, func(t *testing.T) {
			f := installFixture(t)
			outcome := f.epochOutcome(t, 101)
			outcome.OperationID = tc.operation
			outcome.TranscriptID = bytes.Repeat([]byte{7}, tc.size)
			_, err := f.db.Commit(context.Background(), outcome)
			pairOK(t, err)
			result := f.deliverEpoch(t, outcome)
			pairEqual(t, len(result.PairingID), 32)
			pairEqual(t, bytes.Equal(result.PairingID, peer.PairingID(outcome.TranscriptID)), true)
		})
	}
}
