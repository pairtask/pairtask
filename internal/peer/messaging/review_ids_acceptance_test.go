package messaging_test

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewIDOutcome() messaging.ReviewOutcome {
	return messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 3, Attempt: 1, Intent: 1, State: "published", Verdict: "comment", Summary: "reviewed", URL: "https://example.test/review", Reason: "done", ReviewID: "remote:local-review"}
}

func reviewIDFields(t *testing.T, frame []byte) [][]byte {
	t.Helper()
	if len(frame) < 2 {
		t.Fatal("missing frame header")
	}
	var fields [][]byte
	for at := 2; at < len(frame); {
		if at+2 > len(frame) {
			t.Fatal("truncated field length")
		}
		n := int(binary.BigEndian.Uint16(frame[at:]))
		at += 2
		if at+n > len(frame) {
			t.Fatal("truncated field")
		}
		fields = append(fields, frame[at:at+n])
		at += n
	}
	return fields
}

func reviewIDFrame(fields [][]byte) []byte {
	b := []byte{1, messaging.FrameReviewOutcome}
	for _, field := range fields {
		b = binary.BigEndian.AppendUint16(b, uint16(len(field)))
		b = append(b, field...)
	}
	return b
}

func TestReviewIDsWireAcceptanceVersionProjectionAndExactFieldCounts(t *testing.T) {
	for _, version := range []byte{5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			o := reviewIDOutcome()
			wire, err := messaging.EncodeOutcomeFor(o, version)
			sessionOK(t, err)
			fields := reviewIDFields(t, wire)
			sessionEqual(t, len(fields), int(version)+5)
			got, err := messaging.DecodeOutcomeFor(wire, version)
			sessionOK(t, err)
			if version == 5 {
				o.ReviewID = ""
			} else {
				sessionEqual(t, string(fields[10]), o.ReviewID)
			}
			if !reflect.DeepEqual(got, o) {
				t.Fatalf("v%d outcome changed: %+v != %+v", version, got, o)
			}
		})
	}
}

func TestReviewIDsWireAcceptanceShapesAreStrictAcrossVersions(t *testing.T) {
	o := reviewIDOutcome()
	five, err := messaging.EncodeOutcomeFor(o, 5)
	sessionOK(t, err)
	six, err := messaging.EncodeOutcomeFor(o, 6)
	sessionOK(t, err)
	for _, tc := range []struct {
		version byte
		frame   []byte
	}{{5, six}, {6, five}, {5, reviewIDFrame(append(reviewIDFields(t, five), []byte("extra")))}, {6, reviewIDFrame(append(reviewIDFields(t, six), []byte("extra")))}} {
		_, err := messaging.DecodeOutcomeFor(tc.frame, tc.version)
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("v%d accepted wrong field count: %v", tc.version, err)
		}
	}
}

func TestReviewIDsWireAcceptanceEmptyIDAndInvalidIDs(t *testing.T) {
	o := reviewIDOutcome()
	o.ReviewID = ""
	wire, err := messaging.EncodeOutcomeFor(o, 6)
	sessionOK(t, err)
	sessionEqual(t, len(reviewIDFields(t, wire)), 11)
	got, err := messaging.DecodeOutcomeFor(wire, 6)
	sessionOK(t, err)
	sessionEqual(t, got.ReviewID, "")
	for _, id := range []string{"bad\xffutf8", "line\nbreak", strings.Repeat("x", 129), "nul\x00byte"} {
		o.ReviewID = id
		_, err = messaging.EncodeOutcomeFor(o, 6)
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("encoder accepted review ID %q: %v", id, err)
		}
		fields := reviewIDFields(t, wire)
		fields[10] = []byte(id)
		_, err = messaging.DecodeOutcomeFor(reviewIDFrame(fields), 6)
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("decoder accepted review ID %q: %v", id, err)
		}
	}
}

func TestReviewIDsWireAcceptanceDigestExcludesReviewID(t *testing.T) {
	o := reviewIDOutcome()
	first := o.Digest()
	for _, id := range []string{"", "another-review"} {
		o.ReviewID = id
		sessionEqual(t, o.Digest(), first)
	}
	// Historical v5 digest is checked independently of the new wire field.
	sessionEqual(t, hex.EncodeToString(first[:]), "7cdffd61de8ea3a545afdb0207e862f7026dfbd6dee32e976bc7540e6fee6fbf")
}

func TestReviewIDsWireAcceptanceUsesReviewIDValidationRatherThanMessageIDRules(t *testing.T) {
	for _, id := range []string{"review id with spaces", "review-Δ", strings.Repeat("x", 128)} {
		if err := protocol.ValidateReviewID(id); err != nil {
			t.Fatalf("invalid positive fixture %q: %v", id, err)
		}
		o := reviewIDOutcome()
		o.ReviewID = id
		wire, err := messaging.EncodeOutcomeFor(o, 6)
		sessionOK(t, err)
		got, err := messaging.DecodeOutcomeFor(wire, 6)
		sessionOK(t, err)
		sessionEqual(t, got.ReviewID, id)
	}
}
