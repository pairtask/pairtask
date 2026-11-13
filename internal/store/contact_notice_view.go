package store

import (
	"context"
	"time"
)

// What the operator sees of contact notices (#276): the peer's state as
// this side last stored it, and this side's newest notice of the current
// pairing that a newer one hasn't superseded.

// ContactNoticeState is one contact's notice view. PeerState is paused or
// unpaired, empty while active, resumed or unknown, with PeerStateAt when
// it was stored; PeerVersion is the message version a session with it last
// agreed, zero while unknown. Notice is the delivery state of this side's
// newest notice, empty if none, with its attempts. PairingEpoch is the
// contact's current or last pairing, Legacy that it has no pairing ID,
// and UnpairedAt when this side unpaired it, zero while paired. SealsSeen
// is that a session with it once agreed a version with seals (#115).
type ContactNoticeState struct {
	PeerState      string
	PeerStateAt    time.Time
	PeerVersion    int
	Notice         string
	NoticeAttempts int
	PairingEpoch   int64
	Legacy         bool
	UnpairedAt     time.Time
	SealsSeen      bool
}

// contactNoticeRead joins each contact to its newest non-superseded notice
// queued under its current pairing.
const contactNoticeRead = `SELECT c.identity_key, c.peer_state, COALESCE(c.peer_state_at, ''), COALESCE(c.peer_version, 0),
    COALESCE(n.state, ''), COALESCE(n.attempts, 0), c.pairing_epoch, c.pairing_id IS NULL, c.unpaired_at,
    EXISTS (SELECT 1 FROM peer_seal_seen s WHERE s.contact = c.identity_key)
FROM peer_contacts c LEFT JOIN contact_notices_out n ON n.contact = c.identity_key AND n.gen = (
    SELECT MAX(m.gen) FROM contact_notices_out m
    WHERE m.contact = c.identity_key AND m.state != 'superseded' AND m.pairing_id IS c.pairing_id)`

// ContactNoticeStates are every contact's notice views, by identity key.
func (s *Store) ContactNoticeStates(ctx context.Context) (map[string]ContactNoticeState, error) {
	rows, err := s.db.QueryContext(ctx, contactNoticeRead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ContactNoticeState{}
	for rows.Next() {
		key, state, err := scanContactNotice(rows)
		if err != nil {
			return nil, err
		}
		out[key] = state
	}
	return out, rows.Err()
}

func scanContactNotice(row rowScanner) (string, ContactNoticeState, error) {
	var key, at, unpairedAt string
	var c ContactNoticeState
	if err := row.Scan(&key, &c.PeerState, &at, &c.PeerVersion, &c.Notice, &c.NoticeAttempts, &c.PairingEpoch, &c.Legacy, &unpairedAt, &c.SealsSeen); err != nil {
		return "", c, err
	}
	var err error
	if c.UnpairedAt, err = storedTime(unpairedAt); err != nil {
		return "", c, err
	}
	if c.PeerState == noticeResumed {
		c.PeerState = "" // a resume is what active means
	}
	if at == "" || c.PeerState == "" {
		c.PeerStateAt = time.Time{}
		return key, c, nil
	}
	c.PeerStateAt, err = time.Parse(time.RFC3339Nano, at)
	return key, c, err
}

// Notice is the contact's notice of generation gen as stored, whatever its
// state; one that doesn't exist is sql.ErrNoRows. Only the root store
// reads it.
func (p *PeerMessages) Notice(ctx context.Context, contact string, gen int64) (NoticeDue, error) {
	if err := p.localOnly(); err != nil {
		return NoticeDue{}, err
	}
	return scanNoticeDue(p.db.QueryRowContext(ctx, `SELECT `+noticeDueColumns+` FROM contact_notices_out WHERE contact = ? AND gen = ?`,
		contact, gen))
}

// storedTime is a stored time, zero for "".
func storedTime(at string) (time.Time, error) {
	if at == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, at)
}
