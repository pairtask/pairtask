package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Admission of an inbound sealed message against identity state (#392,
// #421 slice 1). A seal whose root key belongs to a lineage held here is
// judged by identity.Admit, whatever contact or protocol version it came
// through; only a seal whose root belongs to no held lineage falls back to
// today's per-workspace check. The evidence of that judgement and the
// trusted-time mark are written in the transaction that stores the
// message, so a failed store leaves neither.

// identityNow is the local clock admission reads; tests replace it.
var identityNow = time.Now

// admissionHook, when set by a test, runs at named points of an admission
// and aborts the transaction by returning an error.
var admissionHook func(stage string) error

func admissionStage(stage string) error {
	if admissionHook == nil {
		return nil
	}
	return admissionHook(stage)
}

// identityMark is the persisted trusted-time high-water mark; zero when
// none was ever stored.
func identityMark(ctx context.Context, q rowQuerier) (time.Time, error) {
	var at string
	err := q.QueryRowContext(ctx, `SELECT mark FROM identity_clock WHERE id = 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, at)
}

// raiseMark moves the mark forward to at, never back.
func raiseMark(ctx context.Context, tx *sql.Tx, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_clock (id, mark) VALUES (1, ?1)
ON CONFLICT (id) DO UPDATE SET mark = MAX(mark, excluded.mark)`, msgStamp(at))
	return err
}

// sealFrame is what the seal must have signed for m to be what it says,
// from contact to this side.
func sealFrame(contact, own string, m messaging.Message) (protocol.PeerLink, signing.Frame) {
	kind, id := signing.SenderAgent, m.From
	if m.From == "" {
		kind, id = signing.SenderOperator, protocol.OperatorID
	}
	link := protocol.PeerLink{Contact: contact, Receiver: own, To: m.To, ReplyID: m.ReplyTo.ID, ReplyAuthor: m.ReplyTo.Author, Files: m.Files}
	return link, signing.Frame{MessageID: m.ID, SenderKind: kind, SenderID: id, Text: m.Text, Addresses: []string{link.Address()}}
}

// heldForSeal is the held lineage a seal's root belongs to, if any.
func heldForSeal(ctx context.Context, tx *sql.Tx, seal signing.Seal) (identity.Held, bool, error) {
	l, ok, err := lineageOfKey(ctx, tx, identity.KeyID(seal.RootKey))
	if err != nil || !ok {
		return identity.Held{}, false, err
	}
	return identityHeld(ctx, tx, l)
}

// sealedMessage is an inbound sealed message and where it is checked.
type sealedMessage struct {
	contact, own string
	m            messaging.Message
	seal         signing.Seal
}

// admitByIdentity judges a seal under its held lineage, writes the
// evidence and raises the mark, all in tx. refused is true for an invalid
// seal; an unapproved or frozen lineage is stored as unverified.
func admitByIdentity(ctx context.Context, tx *sql.Tx, s sealedMessage, h identity.Held) (bool, error) {
	link, frame := sealFrame(s.contact, s.own, s.m)
	if !link.Binds(s.seal.Header) {
		return true, nil
	}
	mark, err := identityMark(ctx, tx)
	if err != nil {
		return false, err
	}
	result, ev := identity.Admit(s.seal, frame, h, identity.Clock{Now: identityNow, Mark: mark})
	if result.State == signing.Invalid {
		return true, nil
	}
	if err := keepAdmission(ctx, tx, s, admitted{h.Lineage, ev}); err != nil {
		return false, err
	}
	if err := raiseMark(ctx, tx, ev.AdmittedAt); err != nil {
		return false, err
	}
	return false, admissionStage("after-evidence")
}

// admitted is a lineage's judgement of one message.
type admitted struct {
	lineage identity.Lineage
	ev      identity.Evidence
}

func keepAdmission(ctx context.Context, tx *sql.Tx, s sealedMessage, a admitted) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO identity_admissions
(contact, pairing_epoch, message_id, lineage, admitted_at, state, epoch, device, record_digest)
VALUES (?1, `+currentEpochOf1+`, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		s.contact, s.m.ID, lineageKey(a.lineage), msgStamp(a.ev.AdmittedAt), string(a.ev.State), int64(a.ev.Epoch), a.ev.Device, a.ev.RecordDigest.String())
	return err
}

// Admission is how an inbound message from the contact stands: its current
// verification, judged again against the identity state held now (read
// only: no mark moves, nothing is written), and the evidence kept when it
// arrived. found is false for a message that wasn't admitted by identity.
func (p *PeerMessages) Admission(ctx context.Context, contact, id string) (signing.Result, identity.Evidence, bool, error) {
	ev, lineage, found, err := storedAdmission(ctx, p.db, contact, id)
	if err != nil || !found {
		return signing.Result{}, identity.Evidence{}, found, err
	}
	current, err := p.currentAdmission(ctx, contact, id, lineage)
	return current, ev, true, err
}

func storedAdmission(ctx context.Context, q rowQuerier, contact, id string) (identity.Evidence, identity.Lineage, bool, error) {
	var ev identity.Evidence
	var at, state, lineage, digest string
	var epoch int64
	err := q.QueryRowContext(ctx, `SELECT admitted_at, state, epoch, device, record_digest, lineage FROM identity_admissions
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND message_id = ?2`, contact, id).Scan(&at, &state, &epoch, &ev.Device, &digest, &lineage)
	if errors.Is(err, sql.ErrNoRows) {
		return ev, identity.Lineage{}, false, nil
	}
	if err != nil {
		return ev, identity.Lineage{}, false, err
	}
	if ev.AdmittedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return ev, identity.Lineage{}, false, err
	}
	ev.State, ev.Epoch = signing.State(state), uint64(epoch)
	if err := decodeHex32(digest, ev.RecordDigest[:]); err != nil {
		return ev, identity.Lineage{}, false, err
	}
	var l identity.Lineage
	err = decodeHex32(lineage, l[:])
	return ev, l, true, err
}

// currentAdmission judges the stored message again under today's state.
func (p *PeerMessages) currentAdmission(ctx context.Context, contact, id string, l identity.Lineage) (signing.Result, error) {
	h, _, err := identityHeld(ctx, p.db, l)
	if err != nil {
		return signing.Result{}, err
	}
	row, found, err := messageRow(ctx, p.db, messageKey{contact, messaging.DirIn, id})
	if err != nil || !found {
		return signing.Result{}, err
	}
	rows := []messaging.Row{row.Row}
	if err := withKept(ctx, p.db, rows); err != nil {
		return signing.Result{}, err
	}
	seal, err := signing.ParseSeal(rows[0].Seal)
	if err != nil {
		return signing.Result{State: signing.Invalid, Reason: "unreadable seal"}, nil
	}
	own, _, err := ownIdentity(ctx, p.db)
	if err != nil {
		return signing.Result{}, err
	}
	mark, err := identityMark(ctx, p.db)
	if err != nil {
		return signing.Result{}, err
	}
	_, frame := sealFrame(contact, own, rows[0].Message)
	result, _ := identity.Admit(seal, frame, h, identity.Clock{Now: identityNow, Mark: mark})
	return result, nil
}

func decodeHex32(s string, out []byte) error {
	var l identity.Lineage
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(l) {
		return identity.ErrMalformed
	}
	copy(out, b)
	return nil
}
