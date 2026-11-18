package space_test

import (
	"bytes"
	"testing"

	"github.com/pairtask/pairtask/internal/space"
)

func TestSpaceConnectEndedAcceptanceExactWireVector(t *testing.T) {
	ended := space.ConnectAnswer{Ended: true}
	clientEqual(t, bytes.Equal(ended.Encode(), []byte{1, 4}), true, "ended encoding")
	got, err := space.DecodeConnectAnswer([]byte{1, 4})
	requireOK(t, err)
	clientEqual(t, got, ended, "ended decoding")
	_, err = space.DecodeConnectRequest([]byte{1, 4})
	requireError(t, err)
}
func TestSpaceConnectEndedAcceptanceRejectsAmbiguousOrTrailingData(t *testing.T) {
	for _, answer := range []space.ConnectAnswer{{Ended: true, Accepted: true}, {Ended: true, Reason: "ended"}, {Ended: true, Accepted: true, Reason: "ambiguous"}} {
		clientEqual(t, len(answer.Encode()), 0, "ambiguous ended encoding")
	}
	for _, frame := range [][]byte{{1, 4, 0}, {1, 4, 0, 0}, {1, 4, 0, 1, 'x'}, {2, 4}, {1, 4, 1, 4}} {
		_, err := space.DecodeConnectAnswer(frame)
		requireError(t, err)
	}
}
