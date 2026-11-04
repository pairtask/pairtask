package messaging_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

const findingsKnownEmpty = `{"total":0,"items":[]}`

func findingsOutcome(t *testing.T, envelope string) messaging.ReviewOutcome {
	t.Helper()
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer", Seq: 3, Attempt: 1, State: "completed", Verdict: "comment", Summary: "reviewed"}
	data, err := json.Marshal(o)
	sessionOK(t, err)
	var fields map[string]json.RawMessage
	sessionOK(t, json.Unmarshal(data, &fields))
	if envelope != "" {
		fields["findings"] = json.RawMessage(envelope)
	}
	data, err = json.Marshal(fields)
	sessionOK(t, err)
	sessionOK(t, json.Unmarshal(data, &o))
	return o
}

func findingsWireFields(t *testing.T, frame []byte) [][]byte {
	t.Helper()
	var fields [][]byte
	for rest := frame[2:]; len(rest) > 0; {
		if len(rest) < 2 {
			t.Fatal("incomplete field size")
		}
		size := int(binary.BigEndian.Uint16(rest))
		rest = rest[2:]
		if size > len(rest) {
			t.Fatal("incomplete field")
		}
		fields = append(fields, rest[:size])
		rest = rest[size:]
	}
	return fields
}

func findingsRawFrame(t *testing.T, envelope string) []byte {
	t.Helper()
	frame, err := messaging.EncodeOutcomeFor(findingsOutcome(t, ""), 6)
	sessionOK(t, err)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(envelope)))
	return append(frame, []byte(envelope)...)
}

func TestFindingsWireAcceptanceVersionShapeAndKnownEmpty(t *testing.T) {
	o := findingsOutcome(t, findingsKnownEmpty)
	for _, tc := range []struct {
		version byte
		fields  int
	}{{5, 10}, {6, 11}, {7, 12}} {
		frame, err := messaging.EncodeOutcomeFor(o, tc.version)
		sessionOK(t, err)
		fields := findingsWireFields(t, frame)
		if len(fields) != tc.fields {
			t.Fatalf("v%d field count=%d, want %d", tc.version, len(fields), tc.fields)
		}
		if tc.version == 7 && !bytes.Equal(fields[11], []byte(findingsKnownEmpty)) {
			t.Fatalf("known empty lost: %q", fields[11])
		}
		decoded, err := messaging.DecodeOutcomeFor(frame, tc.version)
		sessionOK(t, err)
		if decoded.Digest() != o.Digest() {
			t.Fatal("findings changed core digest")
		}
	}
}

func TestFindingsWireAcceptanceCanonicalGoldenAndUnavailable(t *testing.T) {
	for _, envelope := range []string{"", findingsKnownEmpty, `{"total":1,"items":[{"path":"a<b>.go","line":0,"summary":"keep <tag> & data","truncated":false}]}`} {
		frame := findingsRawFrame(t, envelope)
		got, err := messaging.DecodeOutcomeFor(frame, 7)
		sessionOK(t, err)
		again, err := messaging.EncodeOutcomeFor(got, 7)
		sessionOK(t, err)
		if !bytes.Equal(frame, again) {
			t.Fatal("canonical frame was not byte-stable")
		}
	}
}

func TestFindingsWireAcceptanceRejectsNoncanonicalEnvelopes(t *testing.T) {
	for _, envelope := range []string{
		` {"total":0,"items":[]}`,
		`{"items":[],"total":0}`,
		`{"total":0,"items":[],"extra":true}`,
		`{"total":0,"total":0,"items":[]}`,
		`{"total":0.0,"items":[]}`,
		`{"total":0,"items":null}`,
		`{"total":1,"items":[{"path":"x.go","line":0,"summary":"x"}]}`,
		`{"total":1,"items":[{"path":"a\u003cb.go","line":0,"summary":"x","truncated":false}]}`,
	} {
		_, err := messaging.DecodeOutcomeFor(findingsRawFrame(t, envelope), 7)
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("noncanonical envelope accepted: %s (%v)", envelope, err)
		}
	}
}

func TestFindingsWireAcceptanceDowngradeNeverDecodesEnvelope(t *testing.T) {
	frame := findingsRawFrame(t, findingsKnownEmpty)
	for _, version := range []byte{5, 6} {
		_, err := messaging.DecodeOutcomeFor(frame, version)
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("v%d accepted v7 findings field", version)
		}
	}
}

func TestFindingsWireAcceptanceProjectionClipsOnRuneBoundary(t *testing.T) {
	for _, summary := range []string{strings.Repeat("x", 512), strings.Repeat("x", 513), strings.Repeat("x", 511) + "界tail"} {
		full := []messaging.Finding{{Path: "src/whole-path.go", Line: math.MaxInt32, Summary: summary}}
		got := messaging.ProjectFindings(1, full)
		if len(got.Items) != 1 {
			t.Fatal("projection lost a small finding")
		}
		item := got.Items[0]
		if !utf8.ValidString(item.Summary) || len(item.Summary) > 512 || !strings.HasPrefix(summary, item.Summary) {
			t.Fatal("summary cut was not a UTF-8 prefix within 512 bytes")
		}
		if item.Truncated != (len(summary) > 512) {
			t.Fatal("truncation flag does not describe an actual cut")
		}
		if item.Path != full[0].Path || item.Line != full[0].Line {
			t.Fatal("projection changed finding location")
		}
		if full[0].Summary != summary {
			t.Fatal("projection mutated source verdict")
		}
	}
}

func findingsCanonical(t *testing.T, envelope messaging.FindingsEnvelope) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	sessionOK(t, encoder.Encode(envelope))
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

func TestFindingsWireAcceptanceEscapingBudgetSelectsDeterministicPrefix(t *testing.T) {
	full := make([]messaging.Finding, 100)
	for i := range full {
		full[i] = messaging.Finding{Path: strings.Repeat(`"`, 512), Line: i, Summary: strings.Repeat("\x01", 512)}
	}
	got := messaging.ProjectFindings(len(full), full)
	if got.Total != 100 || len(got.Items) == 0 || len(got.Items) > 24 {
		t.Fatal("projection lost total or item bounds")
	}
	wire, err := messaging.EncodeFindings(got)
	sessionOK(t, err)
	if len(wire) > 16<<10 {
		t.Fatal("escaped envelope exceeds byte budget")
	}
	if !reflect.DeepEqual(got.Items, full[:len(got.Items)]) {
		t.Fatal("selection is not verdict-order prefix")
	}
	next := messaging.FindingsEnvelope{Total: 100, Items: full[:len(got.Items)+1]}
	if len(findingsCanonical(t, next)) <= 16<<10 {
		t.Fatal("projection stopped before next finding exceeded budget")
	}
	again := messaging.ProjectFindings(100, full)
	if !reflect.DeepEqual(got, again) {
		t.Fatal("projection is nondeterministic")
	}
}

func TestFindingsWireAcceptanceCanonicalEscapesAndFrameBudget(t *testing.T) {
	envelope := &messaging.FindingsEnvelope{Total: 1, Items: []messaging.Finding{{Path: `a"\<>&.go`, Summary: "\x00\x01\x1f\u2028", Line: 0}}}
	wire, err := messaging.EncodeFindings(envelope)
	sessionOK(t, err)
	if !bytes.Equal(wire, findingsCanonical(t, *envelope)) {
		t.Fatalf("unexpected canonical escapes: %s", wire)
	}
	for _, escape := range []string{`\u0000`, `\u0001`, `\u001f`, `\u2028`, `<>&`} {
		if !bytes.Contains(wire, []byte(escape)) {
			t.Fatalf("missing escape/data %q in %s", escape, wire)
		}
	}
	full := make([]messaging.Finding, 24)
	for i := range full {
		full[i] = messaging.Finding{Path: strings.Repeat("p", 512), Summary: strings.Repeat("s", 512), Line: math.MaxInt32}
	}
	o := findingsOutcome(t, "")
	o.Findings = messaging.ProjectFindings(100, full)
	o.AskID, o.Reviewer, o.State, o.Verdict, o.Reason, o.URL = strings.Repeat("a", 512), strings.Repeat("r", 512), strings.Repeat("s", 512), strings.Repeat("v", 512), strings.Repeat("x", 512), strings.Repeat("u", 512)
	o.Summary, o.ReviewID = strings.Repeat("s", 4096), strings.Repeat("i", 128)
	frame, err := messaging.EncodeOutcomeFor(o, 7)
	sessionOK(t, err)
	if len(frame) > messaging.MaxFrameBytes {
		t.Fatal("maximum fields and findings exceed frame budget")
	}
	_, err = messaging.DecodeOutcomeFor(frame, 7)
	sessionOK(t, err)
}

func TestFindingsWireAcceptanceEncoderAndDecoderEnforceBounds(t *testing.T) {
	for name, change := range map[string]func(*messaging.FindingsEnvelope){
		"negative total":     func(e *messaging.FindingsEnvelope) { e.Total = -1 },
		"total over100":      func(e *messaging.FindingsEnvelope) { e.Total = 101 },
		"items exceed total": func(e *messaging.FindingsEnvelope) { e.Total = 0 },
		"25 items":           func(e *messaging.FindingsEnvelope) { e.Total = 25; e.Items = make([]messaging.Finding, 25) },
		"empty path":         func(e *messaging.FindingsEnvelope) { e.Items[0].Path = "" },
		"long path":          func(e *messaging.FindingsEnvelope) { e.Items[0].Path = strings.Repeat("p", 513) },
		"control path":       func(e *messaging.FindingsEnvelope) { e.Items[0].Path = "a\nb" },
		"negative line":      func(e *messaging.FindingsEnvelope) { e.Items[0].Line = -1 },
		"line over int32":    func(e *messaging.FindingsEnvelope) { e.Items[0].Line = math.MaxInt32 + 1 },
		"long summary":       func(e *messaging.FindingsEnvelope) { e.Items[0].Summary = strings.Repeat("s", 513) },
	} {
		t.Run(name, func(t *testing.T) {
			e := messaging.FindingsEnvelope{Total: 1, Items: []messaging.Finding{{Path: "x.go", Summary: "note"}}}
			change(&e)
			if _, err := messaging.EncodeFindings(&e); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("encoder accepted invalid envelope: %v", err)
			}
			if _, err := messaging.DecodeFindings(findingsCanonical(t, e)); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("decoder accepted invalid envelope: %v", err)
			}
		})
	}
}

func TestFindingsWireAcceptanceInvalidUTF8AndEnvelopeBudget(t *testing.T) {
	for _, field := range []string{"path", "summary"} {
		e := messaging.FindingsEnvelope{Total: 1, Items: []messaging.Finding{{Path: "x.go", Summary: "note"}}}
		if field == "path" {
			e.Items[0].Path = string([]byte{0xff})
		} else {
			e.Items[0].Summary = string([]byte{0xff})
		}
		if _, err := messaging.EncodeFindings(&e); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("invalid UTF-8 %s encoded: %v", field, err)
		}
	}
	raw := []byte(`{"total":1,"items":[{"path":"x.go","line":0,"summary":"`)
	raw = append(raw, 0xff)
	raw = append(raw, []byte(`","truncated":false}]}`)...)
	if _, err := messaging.DecodeFindings(raw); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("decoder accepted invalid UTF-8 JSON")
	}
	e := messaging.FindingsEnvelope{Total: 24, Items: make([]messaging.Finding, 24)}
	for i := range e.Items {
		e.Items[i] = messaging.Finding{Path: strings.Repeat("p", 512), Summary: strings.Repeat("\x01", 512)}
	}
	if len(findingsCanonical(t, e)) <= 16<<10 {
		t.Fatal("fixture did not exceed envelope budget")
	}
	if _, err := messaging.EncodeFindings(&e); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("encoder accepted oversized envelope")
	}
	if _, err := messaging.DecodeFindings(findingsCanonical(t, e)); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("decoder accepted oversized envelope")
	}
}

func TestFindingsWireAcceptanceProjectionLimitsItemCountAndPreservesFullTotal(t *testing.T) {
	full := make([]messaging.Finding, 100)
	for i := range full {
		full[i] = messaging.Finding{Path: "x.go", Line: i, Summary: "note"}
	}
	got := messaging.ProjectFindings(100, full)
	if got.Total != 100 || len(got.Items) != 24 {
		t.Fatalf("projection count=%d total=%d", len(got.Items), got.Total)
	}
	if !reflect.DeepEqual(got.Items, full[:24]) {
		t.Fatal("item limit changed prefix selection")
	}
}

func findingsSessionOutcome(t *testing.T, peerVersion byte) []byte {
	t.Helper()
	db, _ := sessionFixture(t)
	o := findingsOutcome(t, findingsKnownEmpty)
	p := &outcomeProbe{routingSessionProbe: &routingSessionProbe{Store: db.PeerMessages()}, pending: []messaging.ReviewOutcome{o}}
	ack, err := messaging.EncodeOutcomeReply(messaging.OutcomeReply{AskID: o.AskID, Seq: o.Seq})
	sessionOK(t, err)
	frames := &scriptFrames{input: append(outcomePhasePrefix(t, peerVersion), ack, routingSessionEnd(t))}
	_, version, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionOK(t, err)
	sessionEqual(t, version, peerVersion)
	var outcomes [][]byte
	for _, frame := range frames.sent {
		kind, err := messaging.Kind(frame)
		sessionOK(t, err)
		if kind != messaging.FrameReviewOutcome {
			continue
		}
		outcomes = append(outcomes, frame)
	}
	sessionEqual(t, len(outcomes), 1)
	return outcomes[0]
}

func TestFindingsWireAcceptanceNegotiatedV6SessionNeverSendsEnvelope(t *testing.T) {
	frame := findingsSessionOutcome(t, 6)
	sessionEqual(t, len(findingsWireFields(t, frame)), 11)
	decoded, err := messaging.DecodeOutcomeFor(frame, 6)
	sessionOK(t, err)
	if decoded.Findings != nil {
		t.Fatal("v6 session leaked findings envelope")
	}
}

func TestFindingsWireAcceptanceNegotiatedV7SessionSendsKnownEmpty(t *testing.T) {
	frame := findingsSessionOutcome(t, 7)
	sessionEqual(t, len(findingsWireFields(t, frame)), 12)
	decoded, err := messaging.DecodeOutcomeFor(frame, 7)
	sessionOK(t, err)
	if decoded.Findings == nil {
		t.Fatal("v7 session dropped known-empty findings")
	}
	sessionEqual(t, decoded.Findings.Total, 0)
	sessionEqual(t, len(decoded.Findings.Items), 0)
}
