package messaging

import (
	"context"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// File references (#419), version 12: a message may name up to MaxFileRefs
// files by a label, a size and a SHA-256, as its sender states them.
// Nothing is transferred, opened or checked: the receiver gets a statement
// it can compare with a file it obtains another way. The frame wraps the
// one the message would otherwise go as, and only a message with
// references ever uses it, so every other message travels and hashes as
// before at every version.

const (
	// RefsVersion is the first version that carries file references.
	RefsVersion byte = 12
	// FrameRefs is a plain or routed message frame and its references.
	FrameRefs byte = 33
	// MaxFileRefs bounds one message's references.
	MaxFileRefs = 8
	// FileRefPathBytes bounds one reference's label.
	FileRefPathBytes = 512
	// MaxFileRefSize is the largest size a reference states: the largest
	// integer every JSON reader keeps exactly.
	MaxFileRefSize int64 = 1<<53 - 1
	// ReasonRefsUnsupported ends, on the sender's side only, a message
	// with references for a contact whose version carries none: it is
	// never sent without them.
	ReasonRefsUnsupported = "refs_unsupported"
)

// FileRef is one file as its sender names it. Path is a label and never a
// location here: this side doesn't open, resolve or create it.
type FileRef struct {
	Path   string `json:"path" jsonschema:"A label for the file, such as its path in a shared repository: 1 to 512 bytes, no control characters. It is shown to the reader and never opened"`
	Size   int64  `json:"size" jsonschema:"The file's size in bytes, 0 to 2^53-1"`
	SHA256 string `json:"sha256" jsonschema:"The file's SHA-256, 64 lower-case hex digits"`
}

// Valid reports a reference a message may carry.
func (r FileRef) Valid() bool {
	return validRefPath(r.Path) && r.Size >= 0 && r.Size <= MaxFileRefSize && validDigest(r.SHA256)
}

// validRefPath: 1 to FileRefPathBytes of UTF-8 without control characters.
func validRefPath(path string) bool {
	if path == "" || len(path) > FileRefPathBytes || !utf8.ValidString(path) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validDigest: a SHA-256 as 64 lower-case hex digits.
func validDigest(digest string) bool {
	if len(digest) != 2*sha256Bytes {
		return false
	}
	for i := 0; i < len(digest); i++ {
		if c := digest[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

const sha256Bytes = 32

// ValidFileRefs: at most MaxFileRefs references, each valid.
func ValidFileRefs(refs []FileRef) bool {
	if len(refs) > MaxFileRefs {
		return false
	}
	for _, r := range refs {
		if !r.Valid() {
			return false
		}
	}
	return true
}

// refParts is what Hash covers of a message's references, in order; none
// for a message without any, which hashes as it always did.
func refParts(refs []FileRef) []string {
	if len(refs) == 0 {
		return nil
	}
	parts := []string{"refs", strconv.Itoa(len(refs))}
	for _, r := range refs {
		parts = append(parts, r.Path, strconv.FormatInt(r.Size, 10), r.SHA256)
	}
	return parts
}

var refsShape = shape{FrameRefs, 2}

// refCarriers are the frames references may wrap, each with its decoder:
// a message to the operator or to agents, never a review request.
var refCarriers = map[byte]func([]byte) (Message, error){FrameMessage: DecodeMessage, FrameRouted: DecodeRouted}

// EncodeRefs is a message with references: the frame it would go as
// without them, then a count byte and, for each, the label with its
// length, eight bytes of size and the 32-byte digest.
func EncodeRefs(m Message) ([]byte, error) {
	if len(m.Files) == 0 || m.Review != nil || !ValidFileRefs(m.Files) {
		return nil, fmt.Errorf("%w: file references", ErrProtocol)
	}
	bare := m
	bare.Files = nil
	inner, err := encoderOf(bare)(bare)
	if err != nil {
		return nil, err
	}
	return encode(FrameRefs, inner, refList(m.Files))
}

// refList is valid references as a frame carries them: a count byte and,
// for each, the label with its length, the size and the digest.
func refList(refs []FileRef) []byte {
	list := []byte{byte(len(refs))}
	for _, r := range refs {
		digest, _ := hex.DecodeString(r.SHA256)
		list = binary.BigEndian.AppendUint16(list, uint16(len(r.Path)))
		list = append(list, r.Path...)
		list = binary.BigEndian.AppendUint64(list, uint64(r.Size))
		list = append(list, digest...)
	}
	return list
}

// refsDomain separates what RefsDigest hashes from any other use of
// SHA-512: the name, then a zero byte.
const refsDomain = "pairtask-file-refs-v1\x00"

// RefsDigest names a message's references, in order: the SHA-512 of
// refsDomain and then the references as a frame carries them, in hex; ""
// for none, or for references no frame carries. A seal binds it (#115).
// Its 128 digits are longer than an agent ID may be (AgentIDBytes), so
// what names it among a seal's recipients can never be read as one.
func RefsDigest(refs []FileRef) string {
	if len(refs) == 0 || !ValidFileRefs(refs) {
		return ""
	}
	sum := sha512.Sum512(append([]byte(refsDomain), refList(refs)...))
	return hex.EncodeToString(sum[:])
}

// DecodeRefs reads a message with references; a frame outside the bounds
// is a protocol error.
func DecodeRefs(frame []byte) (Message, error) {
	f, err := decode(frame, refsShape)
	if err != nil {
		return Message{}, err
	}
	kind, err := Kind(f[0])
	if err != nil {
		return Message{}, err
	}
	carrier, ok := refCarriers[kind]
	if !ok {
		return Message{}, fmt.Errorf("%w: file references on frame type %d", ErrProtocol, kind)
	}
	m, err := carrier(f[0])
	if err != nil {
		return Message{}, err
	}
	m.Files, err = decodeRefList(f[1])
	return m, err
}

// decodeRefList reads the references field: 1 to MaxFileRefs of them and
// nothing after.
func decodeRefList(b []byte) ([]FileRef, error) {
	if len(b) == 0 || b[0] == 0 || int(b[0]) > MaxFileRefs {
		return nil, fmt.Errorf("%w: file reference count", ErrProtocol)
	}
	rest := b[1:]
	refs := make([]FileRef, 0, int(b[0]))
	for range int(b[0]) {
		ref, more, err := nextRef(rest)
		if err != nil {
			return nil, err
		}
		refs, rest = append(refs, ref), more
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: file references trailing bytes", ErrProtocol)
	}
	return refs, nil
}

// nextRef is one reference and what follows it.
func nextRef(rest []byte) (FileRef, []byte, error) {
	path, rest, err := nextField(rest)
	if err != nil {
		return FileRef{}, nil, err
	}
	if len(rest) < 8+sha256Bytes {
		return FileRef{}, nil, fmt.Errorf("%w: truncated file reference", ErrProtocol)
	}
	size := binary.BigEndian.Uint64(rest)
	ref := FileRef{Path: string(path), SHA256: hex.EncodeToString(rest[8 : 8+sha256Bytes])}
	if size > uint64(MaxFileRefSize) {
		return FileRef{}, nil, fmt.Errorf("%w: file reference size", ErrProtocol)
	}
	if ref.Size = int64(size); !ref.Valid() {
		return FileRef{}, nil, fmt.Errorf("%w: file reference", ErrProtocol)
	}
	return ref, rest[8+sha256Bytes:], nil
}

// RefsStore is a store that keeps a message's references: Queue and Accept
// keep them, Page and Offer return them, and Offer ends a message with
// references for a session whose version carries none (ReasonRefsUnsupported)
// instead of offering it. A claim store that is one speaks RefsVersion.
type RefsStore interface {
	// KeepsFileRefs is true for a store that does all of the above.
	KeepsFileRefs() bool
}

// keepsRefs: the store keeps references.
func keepsRefs(st Store) bool {
	r, ok := st.(RefsStore)
	return ok && r.KeepsFileRefs()
}

// carriesRefs refuses to send m when it has references this session's
// version doesn't carry. The store never offers one there; this is the
// last check that none travels without them.
func (s Session) carriesRefs(_ context.Context, m Message) error {
	if len(m.Files) > 0 && s.Version < RefsVersion {
		return fmt.Errorf("%w: file references at version %d", ErrProtocol, s.Version)
	}
	return nil
}
