package protocol

import (
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// File references (#419): a message to a paired workspace may name up to
// eight files by a label, a size and a SHA-256, as its sender states them.
// Nothing is transferred, opened or checked by pairtask; the reader gets a
// statement to compare with a file it obtains another way. A reference
// grants nothing.

// FileRefsVersion: a msg with workspace, and an operator_contact send,
// take files; an imported message's payload and a contact message carry
// them. An older broker ignores the field, so a client refuses to send
// references to one.
const FileRefsVersion = 31

// ErrRefsUnsupported: the paired workspace's pairtask predates file
// references; the message was not queued.
const ErrRefsUnsupported = "refs_unsupported"

// FileRef is one file as its sender names it: path is a label of at most
// 512 bytes without control characters, never opened here; size is 0 to
// 2^53-1 bytes; sha256 is 64 lower-case hex digits.
type FileRef = messaging.FileRef

// MaxFileRefs bounds one message's references.
const MaxFileRefs = messaging.MaxFileRefs

// ValidateFiles checks references before anything is written.
func ValidateFiles(files []FileRef) error {
	if len(files) > MaxFileRefs {
		return errors.New("files holds at most 8 references")
	}
	if !messaging.ValidFileRefs(files) {
		return errors.New("each file needs a path of 1 to 512 bytes without control characters, a size from 0 to 2^53-1 and a sha256 of 64 lower-case hex digits")
	}
	return nil
}
