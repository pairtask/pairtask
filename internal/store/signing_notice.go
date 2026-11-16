package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A paired workspace on a version that can't sign (#395) is told how to
// update, once per state: when it first shows up so, and again if it goes
// back to such a version after signing. The notice comes from pairtask's own
// sender, never from this side's operator, as an ordinary message its older
// chat can show; newer chats draw it as a card by its first line.

const signingDocs = "https://github.com/pairtask/pairtask/blob/main/docs/pairing.md#signed-messages"

// signingNoticeLead is what the notice says the workspace's version does.
var signingNoticeLead = map[string]string{
	protocol.ContactSigningUnsupported: "Messages between our workspaces can't be verified: your pairtask version can't sign them.",
	protocol.ContactSigningDowngraded:  "Your workspace went back to a pairtask version that can't sign messages, so they can't be verified.",
}

// noteSigning queues the notice when the contact's signing state is one to
// tell it about and differs from the one it was last told; a state that
// signs clears what it was told. It runs in the transaction that recorded
// the version, so a failed queue keeps the version unrecorded for a retry.
func noteSigning(ctx context.Context, tx *sql.Tx, contact, state string) error {
	told, found, err := toldSigning(ctx, tx, contact)
	if err != nil {
		return err
	}
	if !changedSigning(found, told, state) {
		return nil
	}
	if lead := signingNoticeLead[state]; lead != "" {
		if err := queueSigningNotice(ctx, tx, contact, lead); err != nil {
			return ignoreDeferred(err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE peer_contacts SET signing_notice = ? WHERE identity_key = ?`, state, contact)
	return err
}

// toldSigning is the signing state the contact was last told about; found
// is false when the contact is gone.
func toldSigning(ctx context.Context, tx *sql.Tx, contact string) (string, bool, error) {
	var told string
	err := tx.QueryRowContext(ctx, `SELECT signing_notice FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&told)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return told, err == nil, err
}

// changedSigning: the contact is there and its state isn't the one it was
// last told about.
func changedSigning(found bool, told, state string) bool {
	return found && told != state
}

// queueSigningNotice queues the notice to the contact's operator. A full
// outbox only delays it: the state stays untold, so a later session retries.
func queueSigningNotice(ctx context.Context, tx *sql.Tx, contact, lead string) error {
	m := messaging.Message{ID: uuid.NewString(), From: messaging.SystemSender, Text: signingNoticeText(lead)}
	_, err := queueRow(ctx, tx, outgoing{key: messageKey{contact, messaging.DirOut, m.ID}, m: m, now: time.Now(), system: true})
	if errors.Is(err, messaging.ErrOutboxFull) {
		return errNoticeDeferred
	}
	return err
}

// errNoticeDeferred: the notice waits for room; nothing is recorded.
var errNoticeDeferred = errors.New("signing notice deferred")

// ignoreDeferred is err, except a deferred notice, which isn't a failure.
func ignoreDeferred(err error) error {
	if errors.Is(err, errNoticeDeferred) {
		return nil
	}
	return err
}

// signingNoticeText is the notice: the marker, what's wrong, why it
// matters, what to do and where to read more.
func signingNoticeText(lead string) string {
	return protocol.SigningNoticeMarker + "\n" + lead + "\n\n" +
		"Signed messages let each side check who really sent what.\n" +
		"To fix it: update pairtask, then run `pairtask identity init` once on your host.\n" +
		"How it works: " + signingDocs + "\n" +
		"(Sent automatically by pairtask, not typed by this workspace's operator.)"
}
