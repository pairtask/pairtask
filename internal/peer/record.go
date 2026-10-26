// Package peer is the probe for peer workspaces (#178): an opaque online
// relay, a TLS gate that runs inside secured, and the small versioned records
// they carry. Nothing here touches the broker, the hub or the infrastructure;
// the store is an in-memory stand-in until the broker integration.
package peer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// SchemaVersion is the records' wire version; a different one is refused.
const SchemaVersion = 1

// Bounds on what the gate reads. A record is one JSON object on one line.
const (
	MaxRecordBytes    = 16 << 10
	MaxNoteBytes      = 512
	MaxRequestIDBytes = 128
	MaxNameBytes      = 64
)

// Kind is what a record asks for. Only contact kinds exist in this slice;
// grants and revocation are later slices and are refused until then.
type Kind string

const (
	KindRequest Kind = "contact_request"
	KindAccept  Kind = "contact_accept"
	KindIgnore  Kind = "contact_ignore"
)

// Identity is what a peer says about itself for display. Who the peer is
// comes from its TLS certificate, never from here.
type Identity struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

// Record is one request from a peer.
type Record struct {
	Version   int      `json:"v"`
	Kind      Kind     `json:"kind"`
	RequestID string   `json:"request_id"`
	From      Identity `json:"from"`
	Note      string   `json:"note,omitempty"`
}

// Reply statuses.
const (
	StatusPending   = "pending"
	StatusAccepted  = "accepted"
	StatusIgnored   = "ignored"
	StatusDuplicate = "duplicate"
	StatusRefused   = "refused"
)

// Reply is the gate's answer to one record.
type Reply struct {
	Version   int    `json:"v"`
	RequestID string `json:"request_id,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

var errRecordTooLarge = errors.New("record exceeds the size limit")

// validationError is a refusal the gate may say back verbatim: it names a
// rule, never the input.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

// Validate checks a record's shape and bounds, not its meaning.
func (r Record) Validate() error {
	switch {
	case r.Version != SchemaVersion:
		return &validationError{fmt.Sprintf("unsupported record version %d", r.Version)}
	case r.Kind != KindRequest && r.Kind != KindAccept && r.Kind != KindIgnore:
		return &validationError{"unsupported kind"}
	case r.RequestID == "" || len(r.RequestID) > MaxRequestIDBytes || !utf8.ValidString(r.RequestID):
		return &validationError{"request_id must be 1 to 128 bytes of UTF-8"}
	case len(r.From.Name) > MaxNameBytes || len(r.From.Workspace) > MaxNameBytes:
		return &validationError{"name and workspace must be at most 64 bytes"}
	case !utf8.ValidString(r.From.Name) || !utf8.ValidString(r.From.Workspace) || !utf8.ValidString(r.Note):
		return &validationError{"text must be UTF-8"}
	case len(r.Note) > MaxNoteBytes:
		return &validationError{"note must be at most 512 bytes"}
	}
	return nil
}

// ReadRecord reads one line and decodes it strictly: unknown fields and
// trailing data are refused, and a line over the limit is refused before
// it is decoded.
func ReadRecord(r *bufio.Reader) (Record, error) {
	line, err := readBoundedLine(r, MaxRecordBytes)
	if err != nil {
		return Record{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		return Record{}, fmt.Errorf("malformed record: %w", err)
	}
	if rest := bytes.TrimSpace(line[dec.InputOffset():]); len(rest) != 0 {
		return Record{}, errors.New("malformed record: trailing data")
	}
	return rec, rec.Validate()
}

// readBoundedLine reads up to and including a newline, failing once the
// line would exceed limit, so an endless line can't grow memory.
func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, isPrefix, err := r.ReadLine()
		if len(line)+len(fragment) > limit {
			return nil, errRecordTooLarge
		}
		line = append(line, fragment...)
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// WriteJSON writes one value as one line.
func WriteJSON(w io.Writer, v any) error {
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}

// Fingerprint identifies a certificate: the hex SHA-256 of its DER bytes.
// In this probe it stands in for a peer's identity, which is fixture-only:
// renewing a certificate changes it. A stable workspace identity is #115's
// cross-host extension, not this.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
