// Package store persists conversation state (agents and messages) in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS agents (
    agent_id        TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    harness         TEXT,
    model           TEXT,
    connected_at    TIMESTAMP NOT NULL,
    disconnected_at TIMESTAMP,
    PRIMARY KEY (agent_id, session_id)
);

CREATE TABLE IF NOT EXISTS messages (
    seq       INTEGER PRIMARY KEY,
    ts        TIMESTAMP NOT NULL,
    sender    TEXT NOT NULL,
    recipient TEXT NOT NULL,
    payload   TEXT NOT NULL
);

-- Additive migration: existing history remains readable and unchanged.
CREATE TABLE IF NOT EXISTS message_metadata (
    message_id TEXT PRIMARY KEY,
    seq INTEGER NOT NULL UNIQUE,
    sender_session TEXT NOT NULL,
    in_reply_to TEXT NOT NULL,
    ack_requested INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS message_receipts (
    message_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    adapter_received_at TIMESTAMP,
    agent_acknowledged_at TIMESTAMP,
    PRIMARY KEY (message_id, agent_id)
);
CREATE INDEX IF NOT EXISTS receipts_session ON message_receipts(session_id);
CREATE INDEX IF NOT EXISTS agents_recent ON agents(connected_at DESC, agent_id, session_id);
`

// Store wraps the SQLite database handle.
type Store struct {
	db   *sql.DB
	path string
	gate *consentGate // session admission, shared by every PeerMessages (#293)
}

// Open opens (creating if necessary) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer keeps things simple and avoids SQLITE_BUSY under the hub's
	// serialized write pattern; a reader outside the broker still holds the
	// file a moment, so a commit waits for it rather than failing at once.
	db.SetMaxOpenConns(1)
	existing, err := existingTables(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous = FULL; PRAGMA busy_timeout = 5000;" + schema + durableSchema + operatorSchema + agentStatusSchema + claimsSchema + roomsSchema + peerPairingSchema + peerIdentitySchema + peerJoinsSchema + peerContactsSchema + reviewsSchema + operatorContactSchema + contactAddressSchema + contactPauseSchema + assignmentsSchema + refusalNoticesSchema + pairedSchema + consentSchema + consentOwnerSchema + schemaStepsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db, existing); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	// Tables added since the epoch transition's released shape are created
	// once it has succeeded, so a failed or refused transition leaves no new
	// schema object behind.
	if _, err := db.Exec(predictionsSchema + decisionsSchema + agentRolesSchema + signingRootsSchema + peerSealsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply classifier schema: %w", err)
	}
	if err := clearConsentOwner(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("clear session consent: %w", err)
	}
	return &Store{db: db, path: path, gate: &consentGate{}}, nil
}

// Existing tables don't gain columns from CREATE TABLE IF NOT EXISTS. Added
// columns are nullable, so legacy rows read as unknown rather than a default.
var addedColumns = []struct{ table, column, decl string }{
	{"message_metadata", "origin", "TEXT"},
	{"durable_agents", "protocol_version", "INTEGER"},
	{"message_metadata", "room", "TEXT"},              // NULL is the Square
	{"message_metadata", "mentions", "TEXT"},          // a JSON array; NULL is none (#164)
	{"message_metadata", "review_id", "TEXT"},         // a review notice's review (#220); NULL for any other message
	{"peer_joins", "destination", "TEXT"},             // NULL for joins before operator redemption (#178)
	{"peer_joins", "decision_confirmed_at", "TEXT"},   // NULL until the inviter confirms (#178)
	{"peer_pairing_requests", "delivered_at", "TEXT"}, // NULL until the joiner acknowledges (#178)
	// Messages to and from agents (#222): NULL on rows without routing.
	{"peer_messages", "from_agent", "TEXT"},
	{"peer_messages", "to_agents", "TEXT"}, // a JSON array
	{"peer_messages", "reply_id", "TEXT"},
	{"peer_messages", "reply_author", "INTEGER"},
	{"peer_messages", "local_id", "TEXT"}, // an outbound row queued from a local agent's message
	{"message_metadata", "remote_contact", "TEXT"},
	{"message_metadata", "remote_sender", "TEXT"},
	{"message_metadata", "remote_reply_id", "TEXT"},
	{"message_metadata", "remote_reply_author", "INTEGER"},
	// Review requests from paired workspaces (#235).
	{"peer_messages", "review_ask", "TEXT"}, // a JSON ReviewAsk; NULL for a message
	{"remote_imports", "kind", "TEXT"},      // NULL is a message
	{"remote_imports", "approved_at", "TEXT"},
	{"remote_imports", "reason", "TEXT"},
	{"remote_imports", "body", "TEXT"}, // the text of an import without a message: an outcome's notice
	// What this side's operator calls a contact.
	{"peer_contacts", "local_name", "TEXT NOT NULL DEFAULT ''"},
	// A contact's destination moves with a newer redemption.
	{"peer_contacts", "destination_at", "TEXT NOT NULL DEFAULT ''"}, // "" before any
	// Pausing a contact (#276): when, "" while active, and the generation
	// sessions are admitted under, moved on by each pause and resume.
	{"peer_contacts", "paused_at", "TEXT NOT NULL DEFAULT ''"},
	{"peer_contacts", "generation", "INTEGER NOT NULL DEFAULT 0"},
	{"contact_address_ops", "generation", "INTEGER NOT NULL DEFAULT 0"},
	// The operator's acknowledgment of a contact's messages (#287): when,
	// and when one expired unacknowledged; NULL for neither.
	{"peer_messages", "acked_at", "TEXT"},
	{"peer_messages", "ack_expired_at", "TEXT"},
	// The reviewer's side's own ID for a review this side asked for
	// (#259), once an outcome names it; NULL until then. Set once, never
	// changed.
	{"review_asks_out", "review_id", "TEXT"},
	{"review_asks_out", "findings", "TEXT"},
	{"review_asks_out", "findings_seq", "INTEGER"},
	{"review_asks_out", "findings_attempt", "INTEGER"},
	{"review_asks_out", "findings_intent", "INTEGER"},
	{"review_asks_out", "findings_digest", "BLOB"},
	// The digest of the directory a contact last answered (#305), and when;
	// NULL until it answers one.
	{"peer_contacts", "directory_ack", "TEXT"},
	{"peer_contacts", "directory_ack_at", "TEXT"},
	// When this side last received a contact's directory (#304), the same
	// list or not, empty or not; NULL while it never has.
	{"peer_contacts", "directory_at", "TEXT"},
	// When the contact's last outcome of a request was applied here (#304):
	// remote evidence only, never moved by settling a request here.
	{"review_asks_out", "outcome_at", "TEXT"},
	// Pending consent (#293): when the operator let a contact's undecided
	// requests wait for a decision, NULL while off; and a contact paired
	// before consent existed, whose absent grants can't prove undecided.
	{"peer_contacts", "consent_since", "TEXT"},
	{"peer_contacts", "consent_legacy", "INTEGER NOT NULL DEFAULT 0"},
	// Why a held request ended, written as it settles and sent as is; and
	// why the requester refused its outcome, '' unless it did.
	{"consent_requests", "outcome_reason", "TEXT NOT NULL DEFAULT ''"},
	{"consent_requests", "refused_reason", "TEXT NOT NULL DEFAULT ''"},
}

// laterColumns are added once the epoch transition has committed, so a
// failed transition leaves a released database exactly as it was.
var laterColumns = []struct{ table, column, decl string }{
	// When an offline archive replaced the message's body (#119); NULL
	// while it is kept. An archived ID stays reserved.
	{"message_metadata", "archived_at", "TEXT"},
	// A review request's replacement (#259): the request this one replaces,
	// and the one that replaced it; '' for none.
	{"review_asks_out", "supersedes", "TEXT NOT NULL DEFAULT ''"},
	{"review_asks_out", "superseded_by", "TEXT NOT NULL DEFAULT ''"},
	// A review outcome notice as delivered (#259): its typed outcome, JSON,
	// so a replay says what it said; NULL for any other message.
	{"message_metadata", "review_outcome", "TEXT"},
}

// columnAdded runs once, in the transaction that adds its column: what the
// new column needs from the rows already there. A failure adds neither, so
// the next open tries both again.
var columnAdded = map[string]func(*sql.Tx) error{
	"peer_messages.acked_at":       seedAckFloors,
	"peer_contacts.consent_legacy": markConsentLegacy,
}

// existingTables are the tables the file holds before Open creates any.
func existingTables(db *sql.DB) (map[string]bool, error) {
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		existing[name] = true
	}
	return existing, rows.Err()
}

// migrate adds each released column a table lacks, then runs the epoch
// transition. A column of a table the transition still has to create or
// rebuild is the transition's to add, with its callback, in its
// transaction, once every table the callback needs exists.
func migrate(db *sql.DB, existing map[string]bool) error {
	if err := restoreEpochTables(db, existing); err != nil {
		return err
	}
	for _, c := range addedColumns {
		missing, err := columnMissing(db, c.table, c.column)
		if err != nil {
			return err
		}
		if !missing {
			continue
		}
		if err := addReleasedColumn(db, c); err != nil {
			return err
		}
	}
	if err := epochTransition(db, existing); err != nil {
		return err
	}
	return addLaterColumns(db)
}

func addLaterColumns(db *sql.DB) error {
	for _, c := range laterColumns {
		if err := addMissingColumn(db, c); err != nil {
			return err
		}
	}
	return nil
}

// addMissingColumn adds a column the database doesn't have yet.
func addMissingColumn(db *sql.DB, c struct{ table, column, decl string }) error {
	missing, err := columnMissing(db, c.table, c.column)
	if err != nil || !missing {
		return err
	}
	return addColumn(db, c.table, c.column, c.decl)
}

func columnMissing(db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count)
	return count == 0, err
}

// addReleasedColumn adds a missing column now, unless its table is one the
// transition creates or rebuilds: one without pairing_epoch.
func addReleasedColumn(db *sql.DB, c struct{ table, column, decl string }) error {
	if epochTableColumns[c.table] == "" {
		return addColumn(db, c.table, c.column, c.decl)
	}
	transitionAdds, err := columnMissing(db, c.table, "pairing_epoch")
	if err != nil || transitionAdds {
		return err
	}
	return addColumn(db, c.table, c.column, c.decl)
}

// addColumn adds one column, with what it needs from the existing rows, in
// one transaction.
func addColumn(db *sql.DB, table, column, decl string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl); err != nil {
		return err
	}
	if seed := columnAdded[table+"."+column]; seed != nil {
		if err := seed(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// nullList stores a list as a JSON array, or NULL when it is empty.
func nullList(items []string) sql.NullString {
	if len(items) == 0 {
		return sql.NullString{}
	}
	encoded, _ := json.Marshal(items) // a []string always encodes
	return sql.NullString{String: string(encoded), Valid: true}
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// LastSeq returns the highest message sequence number persisted so far,
// or 0 if no messages exist. Used to resume the monotonic sequence across restarts.
func (s *Store) LastSeq(ctx context.Context) (uint64, error) {
	var seq uint64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("query last seq: %w", err)
	}
	return seq, nil
}

type SessionRecord struct {
	Identity    protocol.Recipient
	Harness     string
	Model       string
	ConnectedAt time.Time
}

// RecordConnect inserts an agent session row.
func (s *Store) RecordConnect(ctx context.Context, record SessionRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (agent_id, session_id, harness, model, connected_at) VALUES (?, ?, ?, ?, ?)`,
		record.Identity.AgentID, record.Identity.SessionID, record.Harness, record.Model, record.ConnectedAt.UTC())
	if err != nil {
		return fmt.Errorf("record connect: %w", err)
	}
	return nil
}

// RecordDisconnect stamps the session's disconnected_at.
func (s *Store) RecordDisconnect(ctx context.Context, sessionID string, ts time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET disconnected_at = ? WHERE session_id = ?`,
		ts.UTC(), sessionID)
	if err != nil {
		return fmt.Errorf("record disconnect: %w", err)
	}
	return nil
}

// InsertMessage persists a routed message. seq must be unique and monotonically increasing.
func (s *Store) InsertMessage(ctx context.Context, msg protocol.Message) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (seq, ts, sender, recipient, payload) VALUES (?, ?, ?, ?, ?)`,
		msg.Seq, msg.TS.UTC(), msg.From, msg.To, string(msg.Payload))
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

var ErrDuplicateMessage = errors.New("message ID already exists; this send was not routed again")
var ErrInvalidAck = errors.New("message is not assigned to this recipient session, or adapter receipt is missing")

// PersistMessage commits history, correlation and recipient membership atomically.
// For v3 durable messages this also enrolls recipients in the replay queue.
func (s *Store) PersistMessage(ctx context.Context, msg protocol.Message, senderSession string, recipients []protocol.Recipient) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persistMessage(ctx, tx, routedMessage{msg, senderSession, recipients}); err != nil {
		return err
	}
	return tx.Commit()
}

type routedMessage struct {
	msg           protocol.Message
	senderSession string
	recipients    []protocol.Recipient
}

func persistMessage(ctx context.Context, tx *sql.Tx, routed routedMessage) error {
	msg, recipients := routed.msg, routed.recipients
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_metadata WHERE message_id = ?`, msg.MessageID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return ErrDuplicateMessage
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages (seq, ts, sender, recipient, payload) VALUES (?, ?, ?, ?, ?)`, msg.Seq, msg.TS.UTC(), msg.From, msg.To, string(msg.Payload)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_metadata (message_id, seq, sender_session, in_reply_to, ack_requested, origin, room, mentions, review_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, msg.MessageID, msg.Seq, routed.senderSession, msg.InReplyTo, msg.AckRequested, nullString(msg.Origin), nullString(msg.Room), nullList(msg.Mentions), nullString(msg.ReviewID)); err != nil {
		return err
	}
	if err := persistSeal(ctx, tx, msg); err != nil {
		return err
	}
	if err := insertRecipients(ctx, tx, msg.MessageID, recipients); err != nil {
		return err
	}
	return persistDurable(ctx, tx, msg, recipients)
}

func insertRecipients(ctx context.Context, tx *sql.Tx, id string, recipients []protocol.Recipient) error {
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_receipts (message_id, agent_id, session_id) VALUES (?, ?, ?)`, id, r.AgentID, r.SessionID); err != nil {
			return err
		}
	}
	return nil
}

type Acknowledgment struct {
	MessageID       string
	Recipient       protocol.Recipient
	Stage           string
	ProtocolVersion int
}

// Acknowledge authenticates receipt against the session that owned the inbox at
// acceptance. Stages are monotonic and retries are idempotent.
func (s *Store) Acknowledge(ctx context.Context, ack Acknowledgment) (protocol.Recipient, error) {
	report, err := s.AcknowledgeReport(ctx, ack)
	return report.Sender, err
}

// AcknowledgeReport is Acknowledge, also reporting first transitions on
// operator-sent messages.
func (s *Store) AcknowledgeReport(ctx context.Context, ack Acknowledgment) (AckReport, error) {
	durable, err := s.isDurable(ctx, ack.MessageID)
	if err != nil {
		return AckReport{}, err
	}
	if durable {
		return s.acknowledgeDurable(ctx, ack)
	}
	sender, err := s.acknowledgeLegacy(ctx, ack)
	return AckReport{Sender: sender}, err
}

func (s *Store) acknowledgeLegacy(ctx context.Context, ack Acknowledgment) (protocol.Recipient, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Recipient{}, err
	}
	defer tx.Rollback()
	sender, received, err := ack.lookup(ctx, tx)
	if err != nil {
		return protocol.Recipient{}, err
	}
	query, err := ack.updateQuery(received)
	if err != nil {
		return protocol.Recipient{}, err
	}
	if _, err := tx.ExecContext(ctx, query, time.Now().UTC(), ack.MessageID, ack.Recipient.AgentID, ack.Recipient.SessionID); err != nil {
		return protocol.Recipient{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Recipient{}, err
	}
	return sender, nil
}

func (ack Acknowledgment) lookup(ctx context.Context, tx *sql.Tx) (protocol.Recipient, bool, error) {
	var sender protocol.Recipient
	var received sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT m.sender, mm.sender_session, r.adapter_received_at FROM message_receipts r JOIN message_metadata mm ON mm.message_id = r.message_id JOIN messages m ON m.seq = mm.seq WHERE r.message_id = ? AND r.agent_id = ? AND r.session_id = ? AND mm.ack_requested = 1`, ack.MessageID, ack.Recipient.AgentID, ack.Recipient.SessionID).Scan(&sender.AgentID, &sender.SessionID, &received)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrInvalidAck
	}
	return sender, received.Valid, err
}

func (ack Acknowledgment) updateQuery(received bool) (string, error) {
	switch ack.Stage {
	case protocol.StageAdapterReceived:
		return `UPDATE message_receipts SET adapter_received_at = COALESCE(adapter_received_at, ?) WHERE message_id = ? AND agent_id = ? AND session_id = ?`, nil
	case protocol.StageAgentAcknowledged:
		if !received {
			return "", ErrInvalidAck
		}
		return `UPDATE message_receipts SET agent_acknowledged_at = COALESCE(agent_acknowledged_at, ?) WHERE message_id = ? AND agent_id = ? AND session_id = ?`, nil
	default:
		return "", ErrInvalidAck
	}
}

type Unacknowledged struct {
	Seq           uint64
	MessageID     string
	Sender        string
	SenderSession string
	Durable       bool
}

// UnacknowledgedForSession pages retained receipts without loading an unbounded
// history or holding a database cursor while the hub notifies other sessions.
func (s *Store) UnacknowledgedForSession(ctx context.Context, session string, after uint64) ([]Unacknowledged, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mm.seq, mm.message_id, m.sender, mm.sender_session, i.message_id IS NOT NULL FROM message_receipts r JOIN message_metadata mm ON mm.message_id = r.message_id JOIN messages m ON m.seq = mm.seq LEFT JOIN durable_inbox i ON i.message_id = r.message_id AND i.agent_id = r.agent_id WHERE COALESCE(i.session_id, r.session_id) = ? AND r.agent_acknowledged_at IS NULL AND mm.ack_requested = 1 AND mm.seq > ? ORDER BY mm.seq LIMIT 64`, session, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []Unacknowledged
	for rows.Next() {
		var p Unacknowledged
		if err := rows.Scan(&p.Seq, &p.MessageID, &p.Sender, &p.SenderSession, &p.Durable); err != nil {
			return nil, err
		}
		pending = append(pending, p)
	}
	return pending, rows.Err()
}
