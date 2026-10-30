package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A message from a contact to this side's agents is staged with its row,
// in Accept's transaction: its recipients, sender, reply and local ID are
// frozen there. The hub imports it into the agents' durable inboxes on its
// own loop, which owns the message sequence, all recipients at once; until
// then it stays pending across restarts, its row protected from collection.

// Import states.
const (
	ImportPending  = "pending"
	ImportImported = "imported"
	ImportWithheld = "withheld" // a recipient was unshared first; never imported
)

// ErrNoImport is an import no message was staged for.
var ErrNoImport = errors.New("no message staged for import under that contact and ID")

// RemoteImport is a message staged for this side's agents.
type RemoteImport struct {
	Contact    string             // the peer's identity key
	RemoteID   string             // the message's ID on the peer
	LocalID    string             // reserved at Accept; ImportRemote may move it
	Sender     string             // the remote agent, "" for their operator
	Recipients []string           // this side's agents, frozen at Accept
	ReplyTo    messaging.ReplyRef // as the peer claimed it
	Text       string
	CreatedAt  time.Time // the sender's time
	StagedAt   time.Time
	Order      int64 // its place in staging order, for paging
	// Sessions are the recipients connected now, by agent; not frozen, and
	// the others are enrolled as offline durable recipients are.
	Sessions map[string]string
}

// ImportResult is what ImportRemote settled.
type ImportResult struct {
	State      string               // ImportImported or ImportWithheld
	Changed    bool                 // this call settled it; false when settled before
	Msg        protocol.Message     // the durable message as stored, when imported by this call
	Recipients []protocol.Recipient // its inbox rows, for live delivery
}

// pendingImport is an inbound row whose message isn't imported yet: its
// text and its row must stay.
const pendingImport = `EXISTS (SELECT 1 FROM remote_imports ri WHERE ri.contact = peer_messages.contact
    AND ri.remote_id = peer_messages.message_id AND ri.state = 'pending')`

// stageImport stages an inbound message for agents with its row.
func stageImport(ctx context.Context, tx *sql.Tx, r newRow) error {
	if len(r.m.To) == 0 {
		return nil
	}
	localID, err := freeLocalID(ctx, tx, r.key.contact, r.m.ID)
	if err != nil {
		return err
	}
	recipients, _ := json.Marshal(r.m.To) // a []string always encodes
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_imports
(contact, remote_id, local_id, sender, recipients, reply_id, reply_author, state, staged_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`, r.key.contact, r.m.ID, localID, r.m.From, string(recipients),
		r.m.ReplyTo.ID, int(r.m.ReplyTo.Author), msgStamp(r.now))
	return err
}

// importedBefore: a message for agents reuses the ID of one staged before
// whose row is gone, never the same message.
func importedBefore(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if len(m.To) == 0 {
		return false, nil
	}
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM remote_imports WHERE contact = ? AND remote_id = ?)`,
		contact, m.ID).Scan(&held)
	return held, err
}

// importBacklogFull: the contact's messages staged and not yet imported are
// at their bound, or the remembered ones are.
func importBacklogFull(ctx context.Context, tx *sql.Tx, contact string, size int) (bool, error) {
	var staged, stagedBytes, held int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM peer_messages
WHERE contact = ? AND direction = 'in' AND `+pendingImport, contact).Scan(&staged, &stagedBytes)
	if err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_messages WHERE contact = ? AND direction = 'in'`, contact).Scan(&held); err != nil {
		return false, err
	}
	backlog := budget{messaging.ImportBacklog, messaging.UnreadBytes}
	return !backlog.holds(budget{staged, stagedBytes + size}) || held >= messaging.Tombstones, nil
}

// localIDBase is an import's local ID: derived from the contact and the
// remote ID, so it can't be chosen by the peer.
func localIDBase(contact, remoteID string) string {
	sum := sha256.Sum256([]byte(contact + "\x00" + remoteID))
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return "remote:" + encoded[:52]
}

// localIDAlternates bounds the numbered alternates tried for one import.
const localIDAlternates = 64

// ErrLocalIDsTaken is an import whose derived ID and every alternate are
// held by local messages; it stays pending, and a later attempt may pass.
var ErrLocalIDsTaken = errors.New("every local ID for this remote message is taken")

// freeLocalID is the first of the derived ID and its numbered alternates
// that no local message and no other import holds.
func freeLocalID(ctx context.Context, tx *sql.Tx, contact, remoteID string) (string, error) {
	base := localIDBase(contact, remoteID)
	for i := 0; i <= localIDAlternates; i++ {
		id := base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		var taken bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM message_metadata WHERE message_id = ?1)
    OR EXISTS (SELECT 1 FROM remote_imports WHERE local_id = ?1 AND NOT (contact = ?2 AND remote_id = ?3))`,
			id, contact, remoteID).Scan(&taken)
		if err != nil || !taken {
			return id, err
		}
	}
	return "", ErrLocalIDsTaken
}

const importColumns = `ri.contact, ri.remote_id, ri.local_id, ri.sender, ri.recipients, ri.reply_id, ri.reply_author,
    ri.staged_at, ri.state, COALESCE(pm.text, ''), COALESCE(pm.created_at, ri.staged_at), ri.rowid`

const importFrom = ` FROM remote_imports ri LEFT JOIN peer_messages pm
    ON pm.contact = ri.contact AND pm.direction = 'in' AND pm.message_id = ri.remote_id`

func scanImport(row rowScanner) (RemoteImport, string, error) {
	var imp RemoteImport
	var recipients, staged, state, created string
	err := row.Scan(&imp.Contact, &imp.RemoteID, &imp.LocalID, &imp.Sender, &recipients, &imp.ReplyTo.ID, &imp.ReplyTo.Author,
		&staged, &state, &imp.Text, &created, &imp.Order)
	if err != nil {
		return imp, "", err
	}
	if err := json.Unmarshal([]byte(recipients), &imp.Recipients); err != nil {
		return imp, "", fmt.Errorf("import recipients: %w", err)
	}
	if imp.StagedAt, err = time.Parse(time.RFC3339Nano, staged); err != nil {
		return imp, "", err
	}
	imp.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return imp, state, err
}

// PendingImports is the oldest staged messages not yet imported, across
// contacts.
func (s *Store) PendingImports(ctx context.Context, max int) ([]RemoteImport, error) {
	return s.PendingImportsAfter(ctx, 0, max)
}

// PendingImportsAfter pages PendingImports: those staged after the one
// whose Order is after.
func (s *Store) PendingImportsAfter(ctx context.Context, after int64, max int) ([]RemoteImport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.state = 'pending' AND ri.rowid > ? ORDER BY ri.rowid LIMIT ?`, after, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteImport
	for rows.Next() {
		imp, _, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, imp)
	}
	return out, rows.Err()
}

// ImportRemote imports a staged message into its recipients' durable
// inboxes at seq, in one transaction, reading it again from what Accept
// froze; only imp's contact, remote ID and sessions count. Every recipient
// must still be shared, or the whole message is withheld. A local message
// that took its ID since moves it to the next free alternate. A full inbox
// writes nothing and leaves it pending; one settled before is answered as
// it is.
func (s *Store) ImportRemote(ctx context.Context, imp RemoteImport, seq uint64, ts time.Time) (ImportResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()
	frozen, state, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.contact = ? AND ri.remote_id = ?`, imp.Contact, imp.RemoteID))
	if errors.Is(err, sql.ErrNoRows) {
		return ImportResult{}, ErrNoImport
	}
	if err != nil || state != ImportPending {
		return ImportResult{State: state}, err
	}
	frozen.Sessions = imp.Sessions
	result, err := importIn(ctx, tx, frozen, seq, ts)
	if err != nil {
		return ImportResult{}, err
	}
	return result, tx.Commit()
}

// importIn settles a pending import inside its transaction.
func importIn(ctx context.Context, tx *sql.Tx, imp RemoteImport, seq uint64, ts time.Time) (ImportResult, error) {
	shared, err := allGranted(ctx, tx, imp.Contact, imp.Recipients)
	if err != nil {
		return ImportResult{}, err
	}
	if !shared {
		_, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'withheld', settled_at = ? WHERE contact = ? AND remote_id = ?`,
			msgStamp(ts), imp.Contact, imp.RemoteID)
		return ImportResult{State: ImportWithheld, Changed: true}, err
	}
	if imp.LocalID, err = freeLocalID(ctx, tx, imp.Contact, imp.RemoteID); err != nil {
		return ImportResult{}, err
	}
	msg, recipients, err := importedMessage(ctx, tx, imp, seq, ts)
	if err != nil {
		return ImportResult{}, err
	}
	if err := persistMessage(ctx, tx, routedMessage{msg: msg, recipients: recipients}); err != nil {
		return ImportResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE message_metadata SET remote_contact = ?, remote_sender = ?, remote_reply_id = ?, remote_reply_author = ?
WHERE message_id = ?`, imp.Contact, imp.Sender, nullString(imp.ReplyTo.ID), int(imp.ReplyTo.Author), msg.MessageID); err != nil {
		return ImportResult{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'imported', local_id = ?, settled_at = ? WHERE contact = ? AND remote_id = ?`,
		imp.LocalID, msgStamp(ts), imp.Contact, imp.RemoteID)
	return ImportResult{State: ImportImported, Changed: true, Msg: msg, Recipients: recipients}, err
}

// importedMessage is the durable message an import becomes: from the remote
// sender, or "operator" for the contact's operator, under origin remote.
func importedMessage(ctx context.Context, tx *sql.Tx, imp RemoteImport, seq uint64, ts time.Time) (protocol.Message, []protocol.Recipient, error) {
	payload, _ := json.Marshal(map[string]string{"text": imp.Text}) // a map of strings always encodes
	parent, err := localParent(ctx, tx, imp)
	if err != nil {
		return protocol.Message{}, nil, err
	}
	label, err := contactLabel(ctx, tx, imp.Contact)
	if err != nil {
		return protocol.Message{}, nil, err
	}
	sender := imp.Sender
	if sender == "" {
		sender = protocol.OperatorID
	}
	at := ts.UTC()
	msg := protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &at, From: sender, To: imp.Recipients[0],
		Payload: payload, MessageID: imp.LocalID, Origin: protocol.OriginRemote, InReplyTo: parent,
		AckRequested: true, Durable: true,
		Remote: &protocol.Remote{Contact: imp.Contact, Label: label, Sender: imp.Sender, ReplyID: imp.ReplyTo.ID, ReplyAuthor: imp.ReplyTo.Author}}
	if len(imp.Recipients) > 1 {
		msg.To, msg.Group = protocol.Broadcast, slices.Clone(imp.Recipients)
	}
	recipients := make([]protocol.Recipient, 0, len(imp.Recipients))
	for _, agent := range imp.Recipients {
		recipients = append(recipients, protocol.Recipient{AgentID: agent, SessionID: imp.Sessions[agent]})
	}
	return msg, recipients, nil
}

func contactLabel(ctx context.Context, tx *sql.Tx, contact string) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// localParent is the local message an import answers, or "" when its claim
// doesn't resolve: by the sender, a message of theirs imported here; by the
// receiver, a message one of this side's agents sent them, whose sender is
// among this reply's recipients and which was sent to this reply's sender.
func localParent(ctx context.Context, tx *sql.Tx, imp RemoteImport) (string, error) {
	switch imp.ReplyTo.Author {
	case messaging.ReplyBySender:
		return optionalString(tx.QueryRowContext(ctx, `SELECT local_id FROM remote_imports
WHERE contact = ? AND remote_id = ? AND state = 'imported'`, imp.Contact, imp.ReplyTo.ID))
	case messaging.ReplyByReceiver:
		return answeredLocal(ctx, tx, imp)
	}
	return "", nil
}

func answeredLocal(ctx context.Context, tx *sql.Tx, imp RemoteImport) (string, error) {
	var localID, from, sender string
	var to sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT pm.local_id, pm.from_agent, pm.to_agents, m.sender FROM peer_messages pm
    JOIN message_metadata mm ON mm.message_id = pm.local_id JOIN messages m ON m.seq = mm.seq
WHERE pm.contact = ? AND pm.direction = 'out' AND pm.message_id = ? AND pm.local_id IS NOT NULL AND pm.from_agent IS NOT NULL`,
		imp.Contact, imp.ReplyTo.ID).Scan(&localID, &from, &to, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	addressed, err := groupList(to)
	if err != nil {
		return "", err
	}
	answers := from == sender && slices.Contains(imp.Recipients, from) &&
		(slices.Contains(addressed, imp.Sender) || imp.Sender == "" && len(addressed) == 0)
	if !answers {
		return "", nil
	}
	return localID, nil
}

// optionalString is a row's one text column, "" for no row.
func optionalString(row *sql.Row) (string, error) {
	var s string
	err := row.Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// RemoteParent is the import with this local ID, when it was imported: the
// message an agent's reply with that in_reply_to answers.
func (s *Store) RemoteParent(ctx context.Context, localID string) (RemoteImport, bool, error) {
	imp, state, err := scanImport(s.db.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.local_id = ?`, localID))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteImport{}, false, nil
	}
	return imp, err == nil && state == ImportImported, err
}

// RemoteReply is a local agent's reply to an imported message.
type RemoteReply struct {
	Msg           protocol.Message // as routed here: its ID, seq, sender and recipient
	SenderSession string
	Parent        string // the import's local ID
	Text          string
	Now           time.Time
}

// RemoteSender is who an import shows as its sender here, and so whom a
// reply to it is addressed to: the remote agent, or the operator.
func (imp RemoteImport) RemoteSender() string {
	if imp.Sender == "" {
		return protocol.OperatorID
	}
	return imp.Sender
}

// RemoteReplyResult is a queued reply.
type RemoteReplyResult struct {
	Row      messaging.Row
	Seq      uint64 // its sequence in history here
	Repeated bool   // a retry of a reply already queued: nothing was written
}

// QueueRemoteReply keeps an agent's reply to an imported message in history
// and queues it to the contact, in one transaction, reading the parent
// again: the sender must be one of its recipients and the reply addressed
// to its sender, or it is ErrNotRecipient. A retry of the same reply is
// answered as first queued; anything else under its ID is ErrConflict.
func (s *Store) QueueRemoteReply(ctx context.Context, r RemoteReply) (RemoteReplyResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	defer tx.Rollback()
	o, err := replyOutgoing(ctx, tx, r)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	held, found, err := messageRow(ctx, tx, o.key)
	if err != nil || found {
		return repeatedReply(ctx, tx, held, o, err)
	}
	local := r.Msg
	local.Durable = false // kept as history: it has no inbox here
	if err := persistMessage(ctx, tx, routedMessage{msg: local, senderSession: r.SenderSession}); err != nil {
		return RemoteReplyResult{}, err
	}
	row, err := queueIn(ctx, tx, o)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	return RemoteReplyResult{Row: row, Seq: r.Msg.Seq}, tx.Commit()
}

// replyOutgoing is the message a reply queues, once its parent allows it.
func replyOutgoing(ctx context.Context, tx *sql.Tx, r RemoteReply) (outgoing, error) {
	parent, state, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.local_id = ?`, r.Parent))
	if err != nil || state != ImportImported {
		return outgoing{}, errors.Join(err, ErrNoImport)
	}
	if !slices.Contains(parent.Recipients, r.Msg.From) || r.Msg.To != parent.RemoteSender() {
		return outgoing{}, messaging.ErrNotRecipient
	}
	reply := messaging.Message{ID: r.Msg.MessageID, Text: r.Text, From: r.Msg.From,
		ReplyTo: messaging.ReplyRef{ID: parent.RemoteID, Author: messaging.ReplyByReceiver}}
	if parent.Sender != "" {
		reply.To = []string{parent.Sender}
	}
	if !messaging.ValidID(reply.ID) || !messaging.ValidText(reply.Text) || !messaging.ValidRouting(reply) {
		return outgoing{}, fmt.Errorf("%w: ID, text or routing", messaging.ErrInvalid)
	}
	return outgoing{key: messageKey{parent.Contact, messaging.DirOut, reply.ID}, m: reply, now: r.Now, localID: r.Msg.MessageID}, nil
}

// repeatedReply answers a reply whose ID is queued already: the same text
// and routing, queued from the same agent's message, is the same reply.
func repeatedReply(ctx context.Context, tx *sql.Tx, held storedRow, o outgoing, err error) (RemoteReplyResult, error) {
	row, err := replayed(held, o, err)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	var seq uint64
	var sender string
	err = tx.QueryRowContext(ctx, `SELECT m.seq, m.sender FROM peer_messages pm
    JOIN message_metadata mm ON mm.message_id = pm.local_id JOIN messages m ON m.seq = mm.seq
WHERE pm.cursor = ? AND pm.local_id = ?`, held.Cursor, o.localID).Scan(&seq, &sender)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sender != o.m.From {
		return RemoteReplyResult{}, messaging.ErrConflict
	}
	return RemoteReplyResult{Row: row, Seq: seq, Repeated: true}, err
}
