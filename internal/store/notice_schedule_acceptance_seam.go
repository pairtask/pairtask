package store

import (
	"context"
	"time"
)

// Compile-only seam for the frozen #276 notice scheduling tests; no
// scheduling behaviour yet.

// NoticeDue is a pending contact notice due for an attempt.
type NoticeDue struct {
	Contact   string
	PairingID []byte
	Gen       int64
	State     string
	Attempts  int
	NextAt    time.Time
}

// NoticeOutcome is what one notice attempt did.
type NoticeOutcome string

const (
	NoticeSent   NoticeOutcome = "sent"
	NoticeFailed NoticeOutcome = "failed"
)

// NoticeAttempt is one attempt at delivering a notice.
type NoticeAttempt struct {
	Contact  string
	Gen      int64
	Outcome  NoticeOutcome
	At, Next time.Time
}

// PendingNotices are the notices due at now (seam: none).
func (p *PeerMessages) PendingNotices(ctx context.Context, now time.Time) ([]NoticeDue, error) {
	return nil, nil
}

// RecordNoticeAttempt records one attempt (seam: no-op).
func (p *PeerMessages) RecordNoticeAttempt(ctx context.Context, a NoticeAttempt) (NoticeDue, error) {
	return NoticeDue{}, nil
}

// ClearRemotePaused clears a stored remote pause (seam: no-op).
func (p *PeerMessages) ClearRemotePaused(ctx context.Context) error { return nil }
