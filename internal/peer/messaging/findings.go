package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Findings (#303): from FindingsVersion an outcome with a verdict carries
// the verdict's findings, so they reach the requester even when nothing is
// published. They are the reviewer side's words: data, bounded so the
// outcome always fits one frame, and never shortened where they point.

// FindingsVersion is the first version whose outcomes carry findings.
const FindingsVersion byte = 7

// Bounds of an outcome's findings.
const (
	FindingsItems         = 24       // findings carried
	FindingPathBytes      = 512      // a path, carried whole
	FindingSummaryBytes   = 512      // a summary, cut past this
	FindingsTotal         = 100      // the findings a verdict may have
	FindingsEnvelopeBytes = 16 << 10 // the findings as encoded
)

// Finding is one remark on the reviewed diff; Truncated says its summary
// was cut to fit.
type Finding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Summary   string `json:"summary"`
	Truncated bool   `json:"truncated"`
}

// FindingsEnvelope is the findings an outcome carries: the first of the
// verdict's, in its order, and how many it has in all.
type FindingsEnvelope struct {
	Total int       `json:"total"`
	Items []Finding `json:"items"`
}

// ProjectFindings is what an outcome carries of a verdict's findings, in
// order: each summary cut to its bound, then as many as fit the envelope.
func ProjectFindings(total int, full []Finding) *FindingsEnvelope {
	e := &FindingsEnvelope{Total: max(total, len(full)), Items: []Finding{}}
	for _, f := range full[:min(len(full), FindingsItems)] {
		f.Summary, f.Truncated = cutSummary(f.Summary)
		next := FindingsEnvelope{Total: e.Total, Items: append(e.Items[:len(e.Items):len(e.Items)], f)}
		if encoded, err := canonical(next); err != nil || len(encoded) > FindingsEnvelopeBytes {
			break
		}
		e.Items = next.Items
	}
	return e
}

// cutSummary is a summary within its bound, cut on a rune boundary, and
// whether it was cut.
func cutSummary(s string) (string, bool) {
	if len(s) <= FindingSummaryBytes {
		return s, false
	}
	cut := FindingSummaryBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// EncodeFindings is an envelope as an outcome carries it: nothing for
// none, else its canonical JSON.
func EncodeFindings(e *FindingsEnvelope) ([]byte, error) {
	if e == nil {
		return nil, nil
	}
	if err := e.valid(); err != nil {
		return nil, err
	}
	return canonical(*e)
}

// DecodeFindings reads an outcome's findings: nothing is none; anything
// else must be a valid envelope in exactly its canonical form.
func DecodeFindings(b []byte) (*FindingsEnvelope, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var e FindingsEnvelope
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil || dec.More() {
		return nil, fmt.Errorf("%w: findings", ErrProtocol)
	}
	if err := e.valid(); err != nil {
		return nil, err
	}
	if again, err := canonical(e); err != nil || !bytes.Equal(again, b) {
		return nil, fmt.Errorf("%w: findings not canonical", ErrProtocol)
	}
	return &e, nil
}

// canonical is an envelope's one encoding: Go's JSON without HTML escaping
// or a trailing newline, items never null.
func canonical(e FindingsEnvelope) ([]byte, error) {
	if e.Items == nil {
		e.Items = []Finding{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// valid: within every bound.
func (e FindingsEnvelope) valid() error {
	if !e.counted() {
		return fmt.Errorf("%w: findings count", ErrProtocol)
	}
	for _, f := range e.Items {
		if !f.valid() {
			return fmt.Errorf("%w: finding", ErrProtocol)
		}
	}
	if encoded, err := canonical(e); err != nil || len(encoded) > FindingsEnvelopeBytes {
		return fmt.Errorf("%w: findings size", ErrProtocol)
	}
	return nil
}

// counted: within the bound on items, and a total that covers them within
// the bound on a verdict's findings.
func (e FindingsEnvelope) counted() bool {
	return len(e.Items) <= FindingsItems && len(e.Items) <= e.Total && e.Total <= FindingsTotal
}

// valid: a path to point at, a line, and a summary within bounds.
func (f Finding) valid() bool {
	return validPath(f.Path) && f.Line >= 0 && f.Line <= math.MaxInt32 &&
		len(f.Summary) <= FindingSummaryBytes && utf8.ValidString(f.Summary)
}

func validPath(p string) bool {
	return p != "" && len(p) <= FindingPathBytes && utf8.ValidString(p) && !strings.ContainsFunc(p, unicode.IsControl)
}
