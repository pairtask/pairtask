package space_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/space"
)

func connectAttempt() [16]byte { return [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15} }
func connectRequestVector() []byte {
	// Independent fixed wire vector: v1, request, 32-byte space, 16-byte ID.
	return append([]byte("\x01\x01\x00\x20"+spaceID+"\x00\x10"), connectAttemptSlice()...)
}
func connectAttemptSlice() []byte { id := connectAttempt(); return id[:] }

func TestSpaceConnectAcceptanceRequestWireVector(t *testing.T) {
	want := space.ConnectRequest{Space: spaceID, ConnectID: connectAttempt()}
	vector := connectRequestVector()
	clientEqual(t, bytes.Equal(want.Encode(), vector), true, "request encoding")
	got, err := space.DecodeConnectRequest(vector)
	requireOK(t, err)
	clientEqual(t, got, want, "request decoding")
	// Decoded values must not alias the caller's wire buffer.
	clear(vector)
	clientEqual(t, got, want, "request ownership")
}

func TestSpaceConnectAcceptanceRequestRejectsMalformedFrames(t *testing.T) {
	valid := connectRequestVector()
	cases := map[string][]byte{
		"version":             append([]byte{2}, valid[1:]...),
		"kind":                append([]byte{1, 9}, valid[2:]...),
		"accepted-as-request": {1, 2},
		"trailing":            append(bytes.Clone(valid), 0),
		"duplicate-field":     append(bytes.Clone(valid), valid[2:]...),
		"upper-space":         bytes.Replace(valid, []byte(spaceID), []byte(strings.ToUpper(spaceID)), 1),
		"nonhex-space":        bytes.Replace(valid, []byte(spaceID), []byte(strings.Repeat("z", 32)), 1),
		"oversize":            bytes.Repeat([]byte{0}, 4097),
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) { _, err := space.DecodeConnectRequest(wire); requireError(t, err) })
	}
	for n := range len(valid) {
		t.Run("truncated-"+hex.EncodeToString([]byte{byte(n)}), func(t *testing.T) { _, err := space.DecodeConnectRequest(valid[:n]); requireError(t, err) })
	}
}

func TestSpaceConnectAcceptanceAnswerWireVectors(t *testing.T) {
	for name, tc := range map[string]struct {
		answer space.ConnectAnswer
		wire   []byte
	}{
		"accepted":   {space.ConnectAnswer{Accepted: true}, []byte{1, 2}},
		"refused":    {space.ConnectAnswer{Reason: "not a member"}, []byte("\x01\x03\x00\x0cnot a member")},
		"unicode":    {space.ConnectAnswer{Reason: "é"}, []byte("\x01\x03\x00\x02é")},
		"max-reason": {space.ConnectAnswer{Reason: strings.Repeat("x", 4092)}, append([]byte{1, 3, 15, 252}, bytes.Repeat([]byte{'x'}, 4092)...)},
	} {
		t.Run(name, func(t *testing.T) {
			clientEqual(t, bytes.Equal(tc.answer.Encode(), tc.wire), true, "answer encoding")
			got, err := space.DecodeConnectAnswer(tc.wire)
			requireOK(t, err)
			clientEqual(t, got, tc.answer, "answer decoding")
		})
	}
}

func TestSpaceConnectAcceptanceAnswerRejectsMalformedFrames(t *testing.T) {
	for name, wire := range map[string][]byte{
		"empty": nil, "version": {2, 2}, "kind": {1, 9}, "request": connectRequestVector(),
		"accepted-trailing": {1, 2, 0}, "accepted-field": {1, 2, 0, 0},
		"empty-refusal": {1, 3, 0, 0}, "missing-length": {1, 3, 0}, "short-reason": {1, 3, 0, 2, 'x'},
		"reason-trailing": {1, 3, 0, 1, 'x', 0}, "invalid-utf8": {1, 3, 0, 1, 255},
		"oversize": append([]byte{1, 3, 15, 253}, bytes.Repeat([]byte{'x'}, 4093)...),
	} {
		t.Run(name, func(t *testing.T) { _, err := space.DecodeConnectAnswer(wire); requireError(t, err) })
	}
}

func TestSpaceConnectAcceptanceInvalidEncodingReturnsNoFrame(t *testing.T) {
	for _, id := range []string{"", spaceID[:31], strings.ToUpper(spaceID), strings.Repeat("g", 32)} {
		clientEqual(t, len((space.ConnectRequest{Space: id}).Encode()), 0, "invalid request encoding")
	}
	for _, answer := range []space.ConnectAnswer{{}, {Accepted: true, Reason: "ambiguous"}, {Reason: strings.Repeat("x", 4093)}, {Reason: string([]byte{255})}} {
		clientEqual(t, len(answer.Encode()), 0, "invalid answer encoding")
	}
}

func TestSpaceConnectAcceptancePairingIDVectorAndSymmetry(t *testing.T) {
	a, b := "sha256:"+strings.Repeat("1", 64), "sha256:"+strings.Repeat("e", 64)
	got, err := space.PairingID(spaceID, a, b, connectAttempt())
	requireOK(t, err)
	clientEqual(t, hex.EncodeToString(got[:]), "2eb56f1b050c8ea6ba548e551b5ed588ecb44cdf1b7d50be6502b4338829d5a0", "pairing ID vector")
	reverse, err := space.PairingID(spaceID, b, a, connectAttempt())
	requireOK(t, err)
	clientEqual(t, reverse, got, "symmetric pairing ID")
	for name, tc := range map[string]struct {
		space, a, b string
		id          [16]byte
	}{
		"space":      {strings.Repeat("b", 32), a, b, connectAttempt()},
		"first-key":  {spaceID, "sha256:" + strings.Repeat("2", 64), b, connectAttempt()},
		"second-key": {spaceID, a, "sha256:" + strings.Repeat("f", 64), connectAttempt()},
		"attempt":    {spaceID, a, b, [16]byte{99}},
	} {
		t.Run(name, func(t *testing.T) {
			other, err := space.PairingID(tc.space, tc.a, tc.b, tc.id)
			requireOK(t, err)
			clientEqual(t, other == got, false, "pairing tuple binding")
		})
	}
}

func TestSpaceConnectAcceptancePairingIDRejectsMalformedTuple(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	for _, bad := range []string{"", key[:70], strings.ToUpper(key), "sha512:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("z", 64)} {
		_, err := space.PairingID(spaceID, bad, key, connectAttempt())
		requireError(t, err)
		_, err = space.PairingID(spaceID, key, bad, connectAttempt())
		requireError(t, err)
	}
	for _, bad := range []string{"", spaceID[:31], strings.ToUpper(spaceID), strings.Repeat("z", 32)} {
		_, err := space.PairingID(bad, key, key, connectAttempt())
		requireError(t, err)
	}
}
