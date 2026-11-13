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
	Epoch      int64              // the pairing epoch it was staged under; 0 is the legacy 1
	RemoteID   string             // the message's ID on the peer
	LocalID    string             // reserved at Accept; ImportRemote may move it
	Sender     string             // the remote agent, "" for their operator
	Recipients []string           // this side's agents, frozen at Accept
	ReplyTo    messaging.ReplyRef // as the peer claimed it
	Text       string
	CreatedAt  time.Time // the sender's time
	StagedAt   time.Time
	Order      int64                // its place in staging order, for paging
	Kind       string               // importMessage or importReview
	Review     *messaging.ReviewAsk // a review request's ask, frozen at Accept
	Approved   bool                 // this side's operator approved the request
	// Consent is the evidence of the operator's decision on the hold it
	// was staged from (#293); empty for one staged under a grant.
	Consent ConsentEvidence
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
	ReviewID   string               // the review a request became, when it did
}

// pendingImport is an inbound row whose message isn't imported yet: its
// text and its row must stay.
const pendingImport = `EXISTS (SELECT 1 FROM remote_imports ri WHERE ri.contact = peer_messages.contact
    AND ri.pairing_epoch = peer_messages.pairing_epoch AND ri.remote_id = peer_messages.message_id
    AND ri.state IN ('pending', 'awaiting'))`

// stageImport stages an inbound message for agents with its row, under
// the contact's current pairing, read in the same transaction (#276).
func stageImport(ctx context.Context, tx *sql.Tx, r newRow) error {
	if len(r.m.To) == 0 {
		return nil
	}
	src, err := currentPairing(ctx, tx, r.key.contact)
	if err != nil {
		return err
	}
	state, err := insertStaged(ctx, tx, r, src)
	if err != nil || r.m.Review == nil {
		return err
	}
	return stagedAsk(ctx, tx, r, staged{RemoteImport{Contact: src.contact, Epoch: src.epoch, RemoteID: r.m.ID}, state})
}

// insertStaged writes a staged message's import row in its pairing, and
// answers the state it starts in.
func insertStaged(ctx context.Context, tx *sql.Tx, r newRow, src pairingKey) (string, error) {
	localID, err := freeLocalID(ctx, tx, src, r.m.ID)
	if err != nil {
		return "", err
	}
	kind, state, err := stagedAs(ctx, tx, r)
	if err != nil {
		return "", err
	}
	recipients, _ := json.Marshal(r.m.To) // a []string always encodes
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_imports
(contact, pairing_epoch, remote_id, local_id, sender, recipients, reply_id, reply_author, state, staged_at, kind, approved_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, src.contact, src.epoch, r.m.ID, localID, r.m.From, string(recipients),
		r.m.ReplyTo.ID, int(r.m.ReplyTo.Author), state, msgStamp(r.now), kind, r.approvedAt())
	return state, err
}

// staged is an import as staging wrote it, and the state it starts in.
type staged struct {
	imp   RemoteImport
	state string
}

// stagedAsk reserves a staged request's outcomes, and tells its requester
// when it waits for this side's operator.
func stagedAsk(ctx context.Context, tx *sql.Tx, r newRow, s staged) error {
	if err := reserveOutcomes(ctx, tx, r); err != nil || s.state != ImportAwaiting {
		return err
	}
	return askOutcome(ctx, tx, s.imp, askEnd{"waiting", "", r.now})
}

// approvedAt is when this side's operator approved a staged review
// request: as it is staged, for one allowed from a hold; NULL otherwise.
func (r newRow) approvedAt() sql.NullString {
	if !r.consented || r.m.Review == nil {
		return sql.NullString{}
	}
	return nullString(msgStamp(r.now))
}

// stagedAs is what a staged message is and where it starts: a message
// waits for import; a review request for its operator or for dispatch, by
// the grant it came under, or by the operator's consent.
func stagedAs(ctx context.Context, tx *sql.Tx, r newRow) (string, string, error) {
	if r.m.Review == nil {
		return importMessage, ImportPending, nil
	}
	if r.consented {
		return importReview, ImportPending, nil
	}
	g, _, err := reviewGrant(ctx, tx, r.key.contact, r.m.To[0])
	return importReview, askState(g), err
}

// importedBefore: a message for agents reuses the ID of one staged before
// in the current pairing whose row is gone, never the same message; an
// earlier pairing's import is history, not a conflict (#276).
func importedBefore(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if len(m.To) == 0 {
		return false, nil
	}
	var held bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM remote_imports WHERE contact = ? AND pairing_epoch = `+currentEpoch+`
    AND remote_id = ?)`, contact, contact, m.ID).Scan(&held)
	return held, err
}

// importBacklogFull: the contact's messages staged in its current pairing
// and not yet imported are at their bound, or the remembered identities are.
func importBacklogFull(ctx context.Context, tx *sql.Tx, contact string, size int) (bool, error) {
	var staged, stagedBytes int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM peer_messages
WHERE contact = ? AND pairing_epoch = `+currentEpoch+` AND direction = 'in' AND `+pendingImport, contact, contact).Scan(&staged, &stagedBytes)
	backlog := budget{messaging.ImportBacklog, messaging.UnreadBytes}
	if err != nil || !backlog.holds(budget{staged, stagedBytes + size}) {
		return true, err
	}
	return identitiesFull(ctx, tx, contact)
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
// that no local message and no other import holds; the same remote ID in
// another pairing is another import (#276).
func freeLocalID(ctx context.Context, tx *sql.Tx, src pairingKey, remoteID string) (string, error) {
	base := localIDBase(src.contact, remoteID)
	for i := 0; i <= localIDAlternates; i++ {
		id := base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i)
		}
		var taken bool
		// An import's local ID may become a review's, and its notice's.
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM message_metadata WHERE message_id IN (?1, 'review:' || ?1))
    OR EXISTS (SELECT 1 FROM reviews WHERE review_id = ?1)
    OR EXISTS (SELECT 1 FROM remote_imports WHERE local_id = ?1 AND NOT (contact = ?2 AND pairing_epoch = ?4 AND remote_id = ?3))`,
			id, src.contact, remoteID, src.epoch).Scan(&taken)
		if err != nil || !taken {
			return id, err
		}
	}
	return "", ErrLocalIDsTaken
}

const importColumns = `ri.contact, ri.pairing_epoch, ri.remote_id, ri.local_id, ri.sender, ri.recipients, ri.reply_id, ri.reply_author,
    ri.staged_at, ri.state, COALESCE(pm.text, ri.body, ''), COALESCE(pm.created_at, ri.staged_at), ri.rowid,
    COALESCE(ri.kind, 'message'), pm.review_ask, ri.approved_at IS NOT NULL,
    COALESCE(cr.evidence_kind, ''), COALESCE(cr.evidence_epoch, ''), COALESCE(cr.evidence_gen, 0)`

// importFrom joins an import to its message and the hold it was allowed
// from, each in the import's own pairing (#276).
const importFrom = ` FROM remote_imports ri LEFT JOIN peer_messages pm
    ON pm.contact = ri.contact AND pm.pairing_epoch = ri.pairing_epoch AND pm.direction = 'in' AND pm.message_id = ri.remote_id
    LEFT JOIN consent_requests cr ON cr.contact = ri.contact AND cr.pairing_epoch = ri.pairing_epoch
        AND cr.message_id = ri.remote_id AND cr.state = 'allowed'`

func scanImport(row rowScanner) (RemoteImport, string, error) {
	var imp RemoteImport
	var recipients, staged, state, created string
	var ask sql.NullString
	err := row.Scan(&imp.Contact, &imp.Epoch, &imp.RemoteID, &imp.LocalID, &imp.Sender, &recipients, &imp.ReplyTo.ID, &imp.ReplyTo.Author,
		&staged, &state, &imp.Text, &created, &imp.Order, &imp.Kind, &ask, &imp.Approved,
		&imp.Consent.Kind, &imp.Consent.Epoch, &imp.Consent.Gen)
	if err != nil {
		return imp, "", err
	}
	if imp.Review, err = scanReviewAsk(ask); err != nil {
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
// contacts, each in its contact's current pairing, which hasn't ended.
func (s *Store) PendingImports(ctx context.Context, max int) ([]RemoteImport, error) {
	return s.PendingImportsAfter(ctx, 0, max)
}

// PendingImportsAfter pages PendingImports: those staged after the one
// whose Order is after.
func (s *Store) PendingImportsAfter(ctx context.Context, after int64, max int) ([]RemoteImport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.state = 'pending' AND `+livePairingImport+` AND ri.rowid > ? ORDER BY ri.rowid LIMIT ?`, after, max)
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
// froze; only imp's contact, epoch, remote ID and sessions count. An epoch
// the contact has since left, or an unpaired contact, is refused with
// ErrUnpaired and nothing changes (#276). Every recipient must still be
// shared, or the whole message is withheld. A local message that took its
// ID since moves it to the next free alternate. A full inbox writes
// nothing and leaves it pending; one settled before is answered as it is.
func (s *Store) ImportRemote(ctx context.Context, imp RemoteImport, seq uint64, ts time.Time) (ImportResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()
	frozen, state, err := frozenImport(ctx, tx, imp)
	if err != nil {
		return ImportResult{}, err
	}
	// A paused contact's imports wait, review dispatch included (#276).
	if err := requireActive(ctx, tx, imp.Contact); err != nil {
		return ImportResult{State: state}, err
	}
	if state != ImportPending {
		return ImportResult{State: state}, nil
	}
	frozen.Sessions = imp.Sessions
	result, err := importIn(ctx, tx, frozen, importSlot{seq, ts})
	if err != nil {
		return ImportResult{}, err
	}
	// The dispatch boundary of a request allowed from a hold (#293).
	if err := settleConsentOutcomes(ctx, tx, imp.Contact); err != nil {
		return ImportResult{}, err
	}
	return result, tx.Commit()
}

// frozenImport is imp as Accept froze it, staged under imp's pairing, and
// its state; a pairing that has ended is refused before any read.
func frozenImport(ctx context.Context, tx *sql.Tx, imp RemoteImport) (RemoteImport, string, error) {
	src := imp.source()
	if err := refuseRetired(ctx, tx, src); err != nil {
		return RemoteImport{}, "", err
	}
	frozen, state, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.contact = ? AND ri.pairing_epoch = ? AND ri.remote_id = ?`, src.contact, src.epoch, imp.RemoteID))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteImport{}, "", ErrNoImport
	}
	return frozen, state, err
}

// importSlot is where an import goes in the message order, and when.
type importSlot struct {
	seq uint64
	ts  time.Time
}

// importIn settles a pending import inside its transaction; one allowed
// from a hold whose scope was explicitly denied since is withheld (#293).
func importIn(ctx context.Context, tx *sql.Tx, imp RemoteImport, at importSlot) (ImportResult, error) {
	if result, withheld, err := dispatchDenied(ctx, tx, imp, at); withheld || err != nil {
		return result, err
	}
	switch imp.Kind {
	case importReview:
		return dispatchReview(ctx, tx, imp, at)
	case importOutcome:
		return dispatchOutcome(ctx, tx, imp, at)
	}
	return importPlain(ctx, tx, imp, at)
}

// importPlain settles a pending message: withheld unless every recipient
// is shared with the contact, or the operator allowed this one message,
// else the durable message it becomes.
func importPlain(ctx context.Context, tx *sql.Tx, imp RemoteImport, at importSlot) (ImportResult, error) {
	ts := at.ts
	shared, err := allGranted(ctx, tx, imp.Contact, imp.Recipients)
	if err != nil {
		return ImportResult{}, err
	}
	if !shared && !imp.Consent.authorizes() {
		src := imp.source()
		_, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'withheld', settled_at = ?
WHERE contact = ? AND pairing_epoch = ? AND remote_id = ?`, msgStamp(ts), src.contact, src.epoch, imp.RemoteID)
		return ImportResult{State: ImportWithheld, Changed: true}, err
	}
	return persistImport(ctx, tx, imp, at)
}

// persistImport makes a pending import the durable message it becomes,
// which keeps the pairing the import came under.
func persistImport(ctx context.Context, tx *sql.Tx, imp RemoteImport, at importSlot) (ImportResult, error) {
	ts, src := at.ts, imp.source()
	var err error
	if imp.LocalID, err = freeLocalID(ctx, tx, src, imp.RemoteID); err != nil {
		return ImportResult{}, err
	}
	msg, recipients, err := importedMessage(ctx, tx, imp, at)
	if err != nil {
		return ImportResult{}, err
	}
	if err := persistMessage(ctx, tx, routedMessage{msg: msg, recipients: recipients}); err != nil {
		return ImportResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE message_metadata SET remote_contact = ?, remote_sender = ?, remote_reply_id = ?, remote_reply_author = ?,
    remote_epoch = ? WHERE message_id = ?`, imp.Contact, imp.Sender, nullString(imp.ReplyTo.ID), int(imp.ReplyTo.Author), src.epoch, msg.MessageID); err != nil {
		return ImportResult{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'imported', local_id = ?, settled_at = ?
WHERE contact = ? AND pairing_epoch = ? AND remote_id = ?`, imp.LocalID, msgStamp(ts), src.contact, src.epoch, imp.RemoteID)
	return ImportResult{State: ImportImported, Changed: true, Msg: msg, Recipients: recipients}, err
}

// importedMessage is the durable message an import becomes: from the remote
// sender, or "operator" for the contact's operator, under origin remote.
func importedMessage(ctx context.Context, tx *sql.Tx, imp RemoteImport, at importSlot) (protocol.Message, []protocol.Recipient, error) {
	seq, ts := at.seq, at.ts.UTC()
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
	msg := protocol.Message{Type: protocol.TypeMsg, Seq: seq, TS: &ts, From: sender, To: imp.Recipients[0],
		Payload: payload, MessageID: imp.LocalID, Origin: protocol.OriginRemote, InReplyTo: parent,
		AckRequested: true, Durable: true,
		Remote: &protocol.Remote{Contact: imp.Contact, Label: label, Sender: imp.Sender, ReplyID: imp.ReplyTo.ID, ReplyAuthor: imp.ReplyTo.Author}}
	if len(imp.Recipients) > 1 {
		msg.To, msg.Group = protocol.Broadcast, slices.Clone(imp.Recipients)
	}
	if msg.Seal, err = sealText(ctx, tx, imp); err != nil {
		return protocol.Message{}, nil, err
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
// Either is in the import's own pairing (#276).
func localParent(ctx context.Context, tx *sql.Tx, imp RemoteImport) (string, error) {
	switch imp.ReplyTo.Author {
	case messaging.ReplyBySender:
		return senderParent(ctx, tx, imp)
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
WHERE pm.contact = ? AND pm.pairing_epoch = ? AND pm.direction = 'out' AND pm.message_id = ?
    AND pm.local_id IS NOT NULL AND pm.from_agent IS NOT NULL`, imp.Contact, imp.source().epoch, imp.ReplyTo.ID).Scan(&localID, &from, &to, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	addressed, err := groupList(to)
	if err != nil || !answers(imp, sentParent{from, sender, addressed}) {
		return "", err
	}
	return localID, nil
}

// sentParent is a message one of this side's agents sent the contact: the
// agent its row names, the sender its history names, and whom it was to.
type sentParent struct {
	from, sender string
	to           []string
}

// answers: the parent's agent sent it and is among the reply's
// recipients, and the parent was to the reply's sender.
func answers(imp RemoteImport, parent sentParent) bool {
	if parent.from != parent.sender || !slices.Contains(imp.Recipients, parent.from) {
		return false
	}
	if imp.Sender == "" {
		return len(parent.to) == 0
	}
	return slices.Contains(parent.to, imp.Sender)
}

// senderParent is the import of theirs a reply by the sender names. A review
// request is named only by a reply from the agent that asked, to the
// reviewer it asked (#259); any other reply arrives unthreaded.
func senderParent(ctx context.Context, tx *sql.Tx, imp RemoteImport) (string, error) {
	src := imp.source()
	var parent namedImport
	err := tx.QueryRowContext(ctx, `SELECT local_id, COALESCE(kind, ''), sender, recipients FROM remote_imports
WHERE contact = ? AND pairing_epoch = ? AND remote_id = ? AND state = 'imported'`, src.contact, src.epoch, imp.ReplyTo.ID).
		Scan(&parent.localID, &parent.kind, &parent.sender, &parent.recipients)
	if err != nil {
		return "", ignoreNoRows(err)
	}
	if parent.kind != importReview {
		return parent.localID, nil
	}
	return parent.answeredBy(imp)
}

// namedImport is an import a reply by its sender names.
type namedImport struct{ localID, kind, sender, recipients string }

// answeredBy is the review request's local ID when the reply comes from the
// agent that asked and goes to a reviewer it asked, else "".
func (p namedImport) answeredBy(reply RemoteImport) (string, error) {
	var reviewers []string
	if err := json.Unmarshal([]byte(p.recipients), &reviewers); err != nil {
		return "", fmt.Errorf("import recipients: %w", err)
	}
	if p.sender != reply.Sender {
		return "", nil
	}
	if !slices.ContainsFunc(reviewers, func(r string) bool { return slices.Contains(reply.Recipients, r) }) {
		return "", nil
	}
	return p.localID, nil
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

// NoticeReview is the review request import a paired workspace's review
// notice stands for, by the notice's review ID (#259): found only when
// that import is a review, imported.
func (s *Store) NoticeReview(ctx context.Context, reviewID string) (RemoteImport, bool, error) {
	imp, found, err := s.RemoteParent(ctx, reviewID)
	return imp, found && imp.Kind == importReview, err
}

// RemoteReply is a local agent's reply to an imported message.
type RemoteReply struct {
	Msg           protocol.Message // as routed here: its ID, seq, sender and recipient
	SenderSession string
	Parent        string // the import's local ID
	Text          string
	Now           time.Time
	Sealer        PeerSealer // seals the reply as queued (#115); nil leaves it unsigned
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
	// Advisories are a review request's, read before it was queued (#304).
	Advisories []protocol.ReviewAdvisory
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
	return queueFromAgent(ctx, tx, agentMessage{local: r.Msg, session: r.SenderSession, out: o})
}

// agentMessage is a local agent's message to a contact: as kept in
// history here, from which session, and as queued.
type agentMessage struct {
	local   protocol.Message
	session string
	out     outgoing
}

// queueFromAgent keeps an agent's message in history and queues it, then
// commits; a retry of one queued already is answered as first queued.
func queueFromAgent(ctx context.Context, tx *sql.Tx, a agentMessage) (RemoteReplyResult, error) {
	held, found, err := messageRow(ctx, tx, a.out.key)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	if found {
		return repeatedReply(ctx, tx, held, a.out)
	}
	advice, err := askAdvice(ctx, tx, a.out.key.contact, a.out.m)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	local := a.local
	local.Durable = false // kept as history: it has no inbox here
	if err := persistMessage(ctx, tx, routedMessage{msg: local, senderSession: a.session}); err != nil {
		return RemoteReplyResult{}, err
	}
	row, err := queueIn(ctx, tx, a.out)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	return RemoteReplyResult{Row: row, Seq: local.Seq, Advisories: advice}, tx.Commit()
}

// replyOutgoing is the message a reply queues, once its parent allows it.
func replyOutgoing(ctx context.Context, tx *sql.Tx, r RemoteReply) (outgoing, error) {
	parent, err := replyParent(ctx, tx, r)
	if err != nil {
		return outgoing{}, err
	}
	reply := messaging.Message{ID: r.Msg.MessageID, Text: r.Text, From: r.Msg.From, ReplyTo: parent.answeredBy()}
	if parent.Sender != "" {
		reply.To = []string{parent.Sender}
	}
	if !validOutgoing(reply) {
		return outgoing{}, fmt.Errorf("%w: ID, text or routing", messaging.ErrInvalid)
	}
	return outgoing{key: messageKey{parent.Contact, messaging.DirOut, reply.ID}, m: reply, now: r.Now, localID: r.Msg.MessageID, sealer: r.Sealer}, nil
}

// replyParent is the import a reply answers: imported under the contact's
// current pairing, which hasn't ended, with the reply's sender among its
// recipients and the reply to its sender. One an earlier pairing imported
// is ErrUnpaired: the new pairing never carries a reply to it (#276).
func replyParent(ctx context.Context, tx *sql.Tx, r RemoteReply) (RemoteImport, error) {
	parent, state, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.local_id = ?`, r.Parent))
	if err != nil || state != ImportImported {
		return RemoteImport{}, errors.Join(err, ErrNoImport)
	}
	if err := refuseRetired(ctx, tx, parent.source()); err != nil {
		return RemoteImport{}, err
	}
	if !slices.Contains(parent.Recipients, r.Msg.From) || r.Msg.To != parent.RemoteSender() {
		return RemoteImport{}, messaging.ErrNotRecipient
	}
	if parent.Kind == importOutcome && !parent.requestOutcome() {
		return RemoteImport{}, ErrOutcomeNotice // a refusal this side wrote: nothing there to answer
	}
	return parent, nil
}

// requestOutcome: the import is the notice of an outcome of a review request
// this side sent, which a reply answers as that request (#259). A refusal
// notice or summary answers none.
func (imp RemoteImport) requestOutcome() bool {
	return imp.Kind == importOutcome && strings.HasPrefix(imp.RemoteID, outcomeKeyPrefix) && imp.ReplyTo.ID != ""
}

// answeredBy is what a reply to the import names on the wire: the message
// the peer sent, or, for an outcome notice, the request this side sent.
func (imp RemoteImport) answeredBy() messaging.ReplyRef {
	if imp.requestOutcome() {
		return messaging.ReplyRef{ID: imp.ReplyTo.ID, Author: messaging.ReplyBySender}
	}
	return messaging.ReplyRef{ID: imp.RemoteID, Author: messaging.ReplyByReceiver}
}

// ErrOutcomeNotice refuses a reply to a refusal notice or summary: this side
// wrote it, and it answers no request of that workspace, so nothing there is
// answered (#259). An outcome of a request this side sent is answerable.
var ErrOutcomeNotice = errors.New("a refusal notice is not a chat reply target")

// repeatedReply answers a reply whose ID is queued already: the same text
// and routing, queued from the same agent's message, is the same reply.
func repeatedReply(ctx context.Context, tx *sql.Tx, held storedRow, o outgoing) (RemoteReplyResult, error) {
	row, err := o.replayedAsk(ctx, tx, held, nil)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	var seq uint64
	var sender string
	err = tx.QueryRowContext(ctx, `SELECT m.seq, m.sender FROM peer_messages pm
    JOIN message_metadata mm ON mm.message_id = pm.local_id JOIN messages m ON m.seq = mm.seq
WHERE pm.cursor = ? AND pm.local_id = ?`, held.Cursor, o.localID).Scan(&seq, &sender)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return RemoteReplyResult{}, messaging.ErrConflict
	case err != nil:
		return RemoteReplyResult{}, err
	case sender != o.m.From:
		return RemoteReplyResult{}, messaging.ErrConflict
	}
	return RemoteReplyResult{Row: row, Seq: seq, Repeated: true}, nil
}
