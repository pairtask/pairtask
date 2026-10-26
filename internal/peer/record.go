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
	for _, rule := range recordRules {
		if msg := rule(r); msg != "" {
			return &validationError{msg}
		}
	}
	return nil
}

// recordRules each name what's wrong, or return "".
var recordRules = []func(Record) string{
	func(r Record) string {
		if r.Version != SchemaVersion {
			return fmt.Sprintf("unsupported record version %d", r.Version)
		}
		return ""
	},
	func(r Record) string {
		if !knownKind[r.Kind] {
			return "unsupported kind"
		}
		return ""
	},
	func(r Record) string {
		if !boundedText(r.RequestID, MaxRequestIDBytes) || r.RequestID == "" {
			return "request_id must be 1 to 128 bytes of UTF-8"
		}
		return ""
	},
	func(r Record) string {
		if !boundedText(r.From.Name, MaxNameBytes) || !boundedText(r.From.Workspace, MaxNameBytes) {
			return "name and workspace must be at most 64 bytes of UTF-8"
		}
		return ""
	},
	func(r Record) string {
		if !boundedText(r.Note, MaxNoteBytes) {
			return "note must be at most 512 bytes of UTF-8"
		}
		return ""
	},
}

var knownKind = map[Kind]bool{KindRequest: true, KindAccept: true, KindIgnore: true}

// boundedText is valid UTF-8 of at most limit bytes.
func boundedText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s)
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
// line would exceed limit, so an endless line can't grow memory. A final
// line without a newline is still returned.
func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, isPrefix, err := r.ReadLine()
		if len(line)+len(fragment) > limit {
			return nil, errRecordTooLarge
		}
		line = append(line, fragment...)
		if err != nil {
			return lastLine(line, err)
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// lastLine keeps an unterminated last line at EOF, and passes other errors.
func lastLine(line []byte, err error) ([]byte, error) {
	if errors.Is(err, io.EOF) && len(line) > 0 {
		return line, nil
	}
	return nil, err
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
