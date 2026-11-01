package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pairtask/pairtask/internal/protocol"
)

// The per-inbox limits fit a full reconnect replay in the existing 64-frame
// writer queue and 4 MiB adapter buffer, without a new paging protocol.
const (
	MaxPendingMessages    = 32
	MaxPendingBytes       = 2 << 20
	MaxGlobalPending      = 4096
	MaxGlobalPendingBytes = 64 << 20
	MaxRetainedMessages   = 100000
	MaxRetainedBytes      = 256 << 20
)

var ErrInboxFull = errors.New("durable storage limit reached; message not accepted; acknowledge pending work or archive the database explicitly")

const durableSchema = `
CREATE TABLE IF NOT EXISTS durable_agents (agent_id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS durable_messages (
 message_id TEXT PRIMARY KEY,
 wire_bytes INTEGER NOT NULL,
 storage_bytes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS durable_inbox (
 message_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 received_at TIMESTAMP,
 acknowledged_at TIMESTAMP,
 PRIMARY KEY (message_id, agent_id)
);
CREATE INDEX IF NOT EXISTS durable_pending ON durable_inbox(agent_id) WHERE acknowledged_at IS NULL;
`

func (s *Store) KnownDurableAgent(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_agents WHERE agent_id = ?`, id).Scan(&count)
	return count != 0, err
}

func persistDurable(ctx context.Context, tx *sql.Tx, msg protocol.Message, recipients []protocol.Recipient) error {
	if !msg.Durable {
		return nil
	}
	bytes, err := DurableWireBytes(msg)
	if err != nil {
		return err
	}
	if err := checkDurableCapacity(ctx, tx, durableSize{bytes, recipients, msg.From}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO durable_messages VALUES (?, ?, ?)`, msg.MessageID, bytes, bytes+len(recipients)*1024)
	if err != nil {
		return err
	}
	for _, r := range recipients {
		if _, err := tx.ExecContext(ctx, `INSERT INTO durable_inbox (message_id, agent_id, session_id) VALUES (?, ?, ?)`, msg.MessageID, r.AgentID, r.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// DurableWireBytes includes replay metadata and a conservative allowance for
// receipt rows and IDs. It bounds logical durable storage, not SQLite overhead.
func DurableWireBytes(msg protocol.Message) (int, error) {
	data, err := json.Marshal(msg)
	return len(data) + 64, err
}

type durableSize struct {
	bytes      int
	recipients []protocol.Recipient
	sender     string
}

func checkDurableCapacity(ctx context.Context, tx *sql.Tx, size durableSize) error {
	var count, bytes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(storage_bytes),0) FROM durable_messages`).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= MaxRetainedMessages {
		return inboxFull("retained messages", count, MaxRetainedMessages, "messages")
	}
	if bytes+size.bytes+len(size.recipients)*1024 > MaxRetainedBytes {
		return inboxFull("retained bytes", bytes+size.bytes+len(size.recipients)*1024, MaxRetainedBytes, "bytes")
	}
	query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.acknowledged_at IS NULL`
	if err := tx.QueryRowContext(ctx, query).Scan(&count, &bytes); err != nil {
		return err
	}
	if count+len(size.recipients) > MaxGlobalPending {
		return inboxFull("global pending", count+len(size.recipients), MaxGlobalPending, "recipient entries")
	}
	if bytes+size.bytes*len(size.recipients) > MaxGlobalPendingBytes {
		return inboxFull("global pending bytes", bytes+size.bytes*len(size.recipients), MaxGlobalPendingBytes, "bytes")
	}
	return checkRecipientCapacity(ctx, tx, size)
}

func checkRecipientCapacity(ctx context.Context, tx *sql.Tx, size durableSize) error {
	for _, recipient := range size.recipients {
		if recipient.AgentID == protocol.OperatorID {
			if err := checkOperatorInboxCapacity(ctx, tx, size); err != nil {
				return err
			}
			continue
		}
		var count, bytes int
		query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
		if err := tx.QueryRowContext(ctx, query, recipient.AgentID).Scan(&count, &bytes); err != nil {
			return err
		}
		if count >= MaxPendingMessages {
			return inboxFull("recipient messages", count, MaxPendingMessages, "unacknowledged messages")
		}
		if bytes+size.bytes > MaxPendingBytes {
			return inboxFull("recipient bytes", bytes+size.bytes, MaxPendingBytes, "unacknowledged bytes")
		}
	}
	return nil
}

// ClaimPending claims an inbox for a v3 owner, which cannot display origin.
func (s *Store) ClaimPending(ctx context.Context, recipient protocol.Recipient) ([]protocol.Message, error) {
	return s.ClaimPendingFor(ctx, recipient, protocol.DurableVersion)
}

// ClaimPendingFor commits the new delivery owner before exposing pending frames.
// Only v3+ hub registration calls this; legacy history is never enrolled. Owners
// below OriginVersion are not given operator messages: those stay pending,
// unclaimed, for the next owner that can display their origin.
func (s *Store) ClaimPendingFor(ctx context.Context, recipient protocol.Recipient, version int) ([]protocol.Message, error) {
	messages, _, err := s.ClaimPendingReport(ctx, recipient, version)
	return messages, err
}

// ClaimPendingReport also returns the operator-sent messages this claim moved
// to the new session; their delivery stage is accepted again.
func (s *Store) ClaimPendingReport(ctx context.Context, recipient protocol.Recipient, version int) ([]protocol.Message, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	shows := replayable{origin: version >= protocol.OriginVersion, room: version >= protocol.RoomVersion, remote: version >= protocol.RemoteVersion,
		assignment: version >= protocol.AssignVersion}
	var reclaimed []string
	if shows.origin {
		if reclaimed, err = pendingOperatorIDs(ctx, tx, recipient.AgentID, shows.room); err != nil {
			return nil, nil, err
		}
	}
	// Rows this session can't show stay pending for a later, capable owner.
	_, err = tx.ExecContext(ctx, `UPDATE durable_inbox SET session_id = ?, received_at = NULL WHERE agent_id = ? AND acknowledged_at IS NULL AND (? OR NOT `+operatorRow+`) AND (? OR NOT `+roomRow+`) AND (? OR NOT `+remoteRow+`)`, recipient.SessionID, recipient.AgentID, shows.origin, shows.room, shows.remote)
	if err != nil {
		return nil, nil, err
	}
	messages, err := readPending(ctx, tx, recipient.AgentID, shows)
	if err != nil {
		return nil, nil, err
	}
	// A failed recovery must not make a new logical ID eligible for offline sends.
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_agents (agent_id, protocol_version) VALUES (?, ?) ON CONFLICT(agent_id) DO UPDATE SET protocol_version = excluded.protocol_version`, recipient.AgentID, version); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return messages, reclaimed, nil
}

func pendingOperatorIDs(ctx context.Context, tx *sql.Tx, agentID string, showsRoom bool) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT message_id FROM durable_inbox WHERE agent_id = ? AND acknowledged_at IS NULL AND `+operatorRow+` AND (? OR NOT `+roomRow+`) ORDER BY message_id`, agentID, showsRoom)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

const operatorRow = `EXISTS (SELECT 1 FROM message_metadata o WHERE o.message_id = durable_inbox.message_id AND o.origin = 'operator')`

// roomRow is a pending room post, which only a v9 session can tell apart
// from the Square.
const roomRow = `EXISTS (SELECT 1 FROM message_metadata r WHERE r.message_id = durable_inbox.message_id AND r.room IS NOT NULL)`

// remoteRow is a pending message from a paired workspace, which only a v12
// session can tell apart from a local one.
const remoteRow = `EXISTS (SELECT 1 FROM message_metadata x WHERE x.message_id = durable_inbox.message_id AND x.origin = 'remote')`

// replayable is what a reconnecting session can show: operator origin (v4),
// rooms (v9) and messages from paired workspaces (v12).
type replayable struct {
	origin, room, remote bool
	assignment           bool // the agent reads assignments (#278)
}

// AgentProtocolVersion returns the version negotiated at the agent's last durable
// registration, or 0 when unknown.
func (s *Store) AgentProtocolVersion(ctx context.Context, id string) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT protocol_version FROM durable_agents WHERE agent_id = ?`, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return int(version.Int64), err
}

const durableColumns = `mm.message_id, m.seq, m.ts, m.sender, mm.sender_session, m.recipient, m.payload, mm.in_reply_to, mm.origin, ` + groupColumn + `, mm.room, mm.mentions, mm.review_id, ` + remoteColumns

// groupColumn is a group send's canonical members (JSON), or NULL: an
// operator group's, or a paired workspace's message to several agents.
const groupColumn = `COALESCE((SELECT o.members FROM operator_requests o WHERE o.message_id = mm.message_id AND o.scope_kind = 'group'),
    (SELECT ri.recipients FROM remote_imports ri WHERE ri.local_id = mm.message_id AND mm.origin = 'remote' AND json_array_length(ri.recipients) > 1))`

// remoteColumns are where a message from a paired workspace came from.
const remoteColumns = `mm.remote_contact, (SELECT pc.name FROM peer_contacts pc WHERE pc.identity_key = mm.remote_contact),
    mm.remote_sender, mm.remote_reply_id, mm.remote_reply_author`

func readPending(ctx context.Context, tx *sql.Tx, id string, shows replayable) ([]protocol.Message, error) {
	query := `SELECT ` + durableColumns + ` FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL AND (? OR mm.origin IS NULL OR mm.origin != 'operator') AND (? OR mm.room IS NULL) AND (? OR mm.origin IS NULL OR mm.origin != 'remote') ORDER BY m.seq LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, id, shows.origin, shows.room, shows.remote, MaxPendingMessages+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []protocol.Message
	for rows.Next() {
		msg, err := scanDurableMessage(rows)
		if err != nil {
			return nil, err
		}
		msg.Replayed = true
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) > MaxPendingMessages {
		return nil, ErrInboxFull
	}
	return messages, withReplayedAssignments(ctx, tx, messages, id, shows)
}

// withReplayedAssignments gives each replayed operator message the agent's
// assignment as it stands now (#278), for an agent that reads assignments.
func withReplayedAssignments(ctx context.Context, tx *sql.Tx, messages []protocol.Message, agent string, shows replayable) error {
	if !shows.assignment {
		return nil
	}
	for i := range messages {
		if messages[i].Origin != protocol.OriginOperator {
			continue
		}
		view, err := agentAssignment(ctx, tx, messages[i].MessageID, agent)
		if err != nil {
			return err
		}
		messages[i].Assignment = view
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanDurableMessage(row scanner) (protocol.Message, error) {
	msg := protocol.Message{Type: protocol.TypeMsg, Durable: true, AckRequested: true}
	var payload string
	var origin, group, room, mentions, reviewID sql.NullString
	var remote remoteFields
	err := row.Scan(&msg.MessageID, &msg.Seq, &msg.TS, &msg.From, &msg.SessionID, &msg.To, &payload, &msg.InReplyTo, &origin, &group, &room, &mentions, &reviewID,
		&remote.contact, &remote.label, &remote.sender, &remote.replyID, &remote.replyAuthor)
	if err != nil {
		return msg, err
	}
	msg.Remote = remote.view()
	msg.Room, msg.ReviewID = room.String, reviewID.String
	if msg.Mentions, err = groupList(mentions); err != nil {
		return msg, err
	}
	msg.Payload = json.RawMessage(payload)
	msg.Origin = protocol.DisplayOrigin(origin.String)
	msg.Group, err = groupList(group)
	return msg, err
}

// groupList decodes stored group members; NULL is no group.
func groupList(group sql.NullString) ([]string, error) {
	if !group.Valid {
		return nil, nil
	}
	if group.String == "" {
		return nil, nil // an empty stored value is no group, as before
	}
	var members []string // JSON null decodes to no members
	if err := json.Unmarshal([]byte(group.String), &members); err != nil {
		return nil, fmt.Errorf("group members: %w", err)
	}
	return members, nil
}

// remoteFields are a message's remote columns, all NULL unless it came
// from a paired workspace.
type remoteFields struct {
	contact, label, sender, replyID sql.NullString
	replyAuthor                     sql.NullInt64
}

func (r remoteFields) view() *protocol.Remote {
	if !r.contact.Valid {
		return nil
	}
	return &protocol.Remote{Contact: r.contact.String, Label: r.label.String, Sender: r.sender.String,
		ReplyID: r.replyID.String, ReplyAuthor: byte(r.replyAuthor.Int64)}
}
