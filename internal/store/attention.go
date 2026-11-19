package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Attention (#422): an agent's do-not-disturb, and what each message it
// was sent means for it. The decision is made once, with the durable
// admit, and kept beside the inbox row: a message admitted while DND is on
// stays quiet however often it is replayed and whatever DND is later, and
// the operator's urgent one stays urgent. When DND ends with quiet
// messages still unacknowledged, one summary is owed to the agent, under a
// generation its adapter confirms. Nothing here delays, drops or
// acknowledges a message.

const attentionSchema = `
CREATE TABLE IF NOT EXISTS attention_settings (
    agent_id   TEXT PRIMARY KEY,
    dnd        INTEGER NOT NULL DEFAULT 0,
    since      TEXT NOT NULL DEFAULT '',
    enforced   INTEGER NOT NULL DEFAULT 1,
    generation INTEGER NOT NULL DEFAULT 0,
    owed       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS inbox_attention (
    message_id    TEXT NOT NULL,
    agent_id      TEXT NOT NULL,
    disposition   TEXT NOT NULL,
    suppressed_at TEXT NOT NULL,
    PRIMARY KEY (message_id, agent_id)
);
CREATE TABLE IF NOT EXISTS urgent_messages (
    message_id TEXT PRIMARY KEY
);
`

// Attention is an agent's stored DND and what it holds.
type Attention struct {
	DND       bool
	Since     string
	Pending   int // kept quiet and unacknowledged
	InboxUsed int // unacknowledged, quiet or not
}

// quietPending counts or lists an agent's quiet, unacknowledged messages.
const quietPending = ` FROM inbox_attention a JOIN durable_inbox d USING (message_id, agent_id)
WHERE a.agent_id = ?1 AND a.disposition = '` + protocol.AttentionSuppressed + `' AND d.acknowledged_at IS NULL`

// ReadAttention is the agent's DND and inbox, as stored.
func (s *Store) ReadAttention(ctx context.Context, agent string) (Attention, error) {
	return readAttention(ctx, s.db, agent)
}

func readAttention(ctx context.Context, q rowQuerier, agent string) (Attention, error) {
	var a Attention
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT dnd FROM attention_settings WHERE agent_id = ?1), 0),
    COALESCE((SELECT since FROM attention_settings WHERE agent_id = ?1), ''),
    (SELECT COUNT(*)`+quietPending+`),
    (SELECT COUNT(*) FROM durable_inbox WHERE agent_id = ?1 AND acknowledged_at IS NULL)`, agent).Scan(&a.DND, &a.Since, &a.Pending, &a.InboxUsed)
	return a, err
}

// SetAttention turns the agent's DND on or off. Turning it off with quiet
// messages still unacknowledged owes the agent one summary, under a new
// generation; a repeat of the setting it already has changes nothing.
// It reports whether the setting changed.
func (s *Store) SetAttention(ctx context.Context, agent string, on bool, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	before, err := readAttention(ctx, tx, agent)
	if err != nil || before.DND == on {
		return false, err
	}
	if err := switchAttention(ctx, tx, agent, attentionSwitch{on, before.Pending > 0, stamp(now)}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// attentionSwitch is a change of the setting: to on or off, at a time,
// and whether quiet messages are still unacknowledged.
type attentionSwitch struct {
	on, pending bool
	at          string
}

func switchAttention(ctx context.Context, tx *sql.Tx, agent string, sw attentionSwitch) error {
	if sw.on {
		_, err := tx.ExecContext(ctx, `INSERT INTO attention_settings (agent_id, dnd, since) VALUES (?1, 1, ?2)
ON CONFLICT (agent_id) DO UPDATE SET dnd = 1, since = excluded.since`, agent, sw.at)
		return err
	}
	owes := 0
	if sw.pending {
		owes = 1
	}
	_, err := tx.ExecContext(ctx, `UPDATE attention_settings SET dnd = 0, since = '', generation = generation + ?2, owed = MAX(owed, ?2)
WHERE agent_id = ?1`, agent, owes)
	return err
}

// SetAttentionEnforced records whether the agent's DND can be enforced as
// messages are admitted: not while an adapter that predates it is
// connected. An agent that isn't connected counts as enforceable, since
// the decision then waits for it.
func (s *Store) SetAttentionEnforced(ctx context.Context, agent string, enforced bool) error {
	if enforced { // the default: only a row that says otherwise changes
		_, err := s.db.ExecContext(ctx, `UPDATE attention_settings SET enforced = 1 WHERE agent_id = ? AND enforced = 0`, agent)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO attention_settings (agent_id, enforced) VALUES (?1, 0)
ON CONFLICT (agent_id) DO UPDATE SET enforced = 0`, agent)
	return err
}

// markAttention decides, with a durable message's admit, what it means for
// each recipient whose DND is on and enforceable: quiet, or urgent for the
// local operator's urgent message. Others get no mark and are notified.
func markAttention(ctx context.Context, tx *sql.Tx, msg protocol.Message, recipients []protocol.Recipient) error {
	disposition := protocol.AttentionSuppressed
	if localUrgent(msg) {
		disposition = protocol.AttentionUrgent
		if _, err := tx.ExecContext(ctx, `INSERT INTO urgent_messages (message_id) VALUES (?) ON CONFLICT DO NOTHING`, msg.MessageID); err != nil {
			return err
		}
	}
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO inbox_attention (message_id, agent_id, disposition, suppressed_at)
SELECT ?1, ?2, ?3, ?4 WHERE EXISTS (SELECT 1 FROM attention_settings WHERE agent_id = ?2 AND dnd = 1 AND enforced = 1)`,
			msg.MessageID, r.AgentID, disposition, stamp(time.Now())); err != nil {
			return err
		}
	}
	return nil
}

// pruneAttention drops the decisions whose own inbox row is gone: the
// same message for the same agent, never another's of the same ID.
func pruneAttention(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM inbox_attention WHERE NOT EXISTS (SELECT 1 FROM durable_inbox d
    WHERE d.message_id = inbox_attention.message_id AND d.agent_id = inbox_attention.agent_id)`)
	return err
}

// localUrgent: the local operator's own message, marked urgent. A message
// from an agent or from a paired workspace never is.
func localUrgent(msg protocol.Message) bool {
	return msg.Urgent && msg.Origin == protocol.OriginOperator && msg.Remote == nil
}

// MessageUrgent reports whether the stored message was sent urgent.
func (s *Store) MessageUrgent(ctx context.Context, id string) (bool, error) {
	var urgent bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM urgent_messages WHERE message_id = ?)`, id).Scan(&urgent)
	return urgent, err
}

// Disposition is what the message means for the agent, as decided at its
// admit: "" for a message that notifies, or that was never durable here.
func (s *Store) Disposition(ctx context.Context, id, agent string) (string, error) {
	var d string
	err := s.db.QueryRowContext(ctx, `SELECT disposition FROM inbox_attention WHERE message_id = ? AND agent_id = ?`, id, agent).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return d, err
}

// OwedSummary is the summary owed to the agent now: none while DND is on,
// when none is owed, or when nothing quiet is left unacknowledged.
func (s *Store) OwedSummary(ctx context.Context, agent string) (*protocol.AttentionSummary, error) {
	var generation uint64
	err := s.db.QueryRowContext(ctx, `SELECT generation FROM attention_settings WHERE agent_id = ? AND owed = 1 AND dnd = 0`, agent).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids, err := queryStrings(ctx, s.db, `SELECT a.message_id`+quietPending+` ORDER BY (SELECT mm.seq FROM message_metadata mm WHERE mm.message_id = a.message_id)`, agent)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return &protocol.AttentionSummary{Generation: generation, Count: len(ids), MessageIDs: ids}, nil
}

// SummarySubmitted records that the agent's host was shown the summary of
// that generation: accepted only for the one owed to this agent now.
func (s *Store) SummarySubmitted(ctx context.Context, agent string, generation uint64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE attention_settings SET owed = 0 WHERE agent_id = ? AND owed = 1 AND dnd = 0 AND generation = ?`, agent, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
