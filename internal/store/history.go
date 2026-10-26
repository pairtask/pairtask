package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pairtask/pairtask/internal/protocol"
)

// HistoryQuery is a history page request plus the bytes available for
// entries, which the caller derives from the exact response envelope.
type HistoryQuery struct {
	Page   protocol.PageRequest
	Budget int
}

// historyScope matches the operator's conversation by trusted provenance:
// operator-sent messages and messages in the operator inbox.
const historyScope = `(mm.origin = 'operator' OR EXISTS (SELECT 1 FROM durable_inbox o WHERE o.message_id = mm.message_id AND o.agent_id = 'operator'))`

// ReadOperatorHistory returns a newest-first page of the operator's
// conversation. It changes nothing.
func (s *Store) ReadOperatorHistory(ctx context.Context, q HistoryQuery) (protocol.HistoryPage, error) {
	req := q.Page
	limit, err := historyLimit(req.Limit)
	if err != nil {
		return protocol.HistoryPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return protocol.HistoryPage{}, err
	}
	defer tx.Rollback()
	page, err := historyBounds(ctx, tx, req)
	if err != nil {
		return protocol.HistoryPage{}, err
	}
	scan := historyScan{before: req.BeforeSeq, upper: page.UpperSeq, limit: limit, budget: q.Budget}
	if page.Entries, err = historyEntries(ctx, tx, scan); err != nil {
		return protocol.HistoryPage{}, err
	}
	page.NextBeforeSeq = req.BeforeSeq
	if n := len(page.Entries); n > 0 {
		page.NextBeforeSeq = page.Entries[n-1].Message.Seq
	}
	page.More, err = historyBelow(ctx, tx, page)
	return page, err
}

func historyLimit(limit int) (int, error) {
	if limit == 0 {
		return protocol.HistoryDefault, nil
	}
	if limit < 0 || limit > protocol.HistoryMax {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// historyBounds captures the scan's upper bound from committed history.
func historyBounds(ctx context.Context, tx *sql.Tx, req protocol.PageRequest) (protocol.HistoryPage, error) {
	var committed uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&committed); err != nil {
		return protocol.HistoryPage{}, err
	}
	upper := req.UpperSeq
	if upper == 0 {
		upper = committed
	}
	if upper > committed || req.BeforeSeq > upper {
		return protocol.HistoryPage{}, ErrInvalidCursor
	}
	return protocol.HistoryPage{UpperSeq: upper, Entries: []protocol.HistoryEntry{}}, nil
}

type historyRow struct {
	entry   protocol.HistoryEntry
	payload string
}

type historyScan struct {
	before, upper uint64
	limit, budget int
}

func historyEntries(ctx context.Context, tx *sql.Tx, scan historyScan) ([]protocol.HistoryEntry, error) {
	rows, err := historyRows(ctx, tx, scan)
	if err != nil {
		return nil, err
	}
	entries := []protocol.HistoryEntry{}
	budget := scan.budget
	for _, row := range rows {
		if err := attachHistoryStages(ctx, tx, &row.entry); err != nil {
			return nil, err
		}
		entry, size, err := fitEntry(row, budget, len(entries) == 0)
		if err != nil {
			return nil, err
		}
		if size > budget {
			break // Never skip a row: it starts the next page.
		}
		budget -= size
		entries = append(entries, entry)
	}
	return entries, nil
}

// fitEntry includes the payload unless the entry alone can't fit an otherwise
// empty page; then the body is omitted instead of truncated.
func fitEntry(row historyRow, budget int, first bool) (protocol.HistoryEntry, int, error) {
	entry := row.entry
	entry.Message.Payload = retainedPayload(row.payload)
	size, err := entrySize(entry)
	if err != nil {
		return entry, size, err
	}
	if !mustOmitBody(size, budget, first) {
		return entry, size, nil
	}
	entry.Message.Payload = nil
	entry.BodyOmitted = true
	size, err = entrySize(entry)
	return entry, size, err
}

// entrySize counts the entry plus a separating comma; the comma is an exact
// over-count of one byte only for the first entry.
// mustOmitBody holds only for a first entry that can't fit an otherwise empty
// page; any later entry that doesn't fit starts the next page instead.
func mustOmitBody(size, budget int, first bool) bool {
	return first && size > budget
}

func entrySize(entry protocol.HistoryEntry) (int, error) {
	data, err := json.Marshal(entry)
	return len(data) + 1, err
}

func historyRows(ctx context.Context, tx *sql.Tx, scan historyScan) ([]historyRow, error) {
	query := `SELECT mm.message_id, m.seq, m.ts, m.sender, mm.sender_session, m.recipient, m.payload, mm.in_reply_to, mm.ack_requested, mm.origin, ` + groupColumn + `, mm.room, mm.mentions
	 FROM message_metadata mm JOIN messages m USING(seq)
	 WHERE ` + historyScope + ` AND m.seq <= ? AND (? = 0 OR m.seq < ?) ORDER BY m.seq DESC LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, scan.upper, scan.before, scan.before, scan.limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyRow
	for rows.Next() {
		row, err := scanHistoryRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanHistoryRow(rows *sql.Rows) (historyRow, error) {
	var row historyRow
	var origin, group, room, mentions sql.NullString
	m := &row.entry.Message
	err := rows.Scan(&m.MessageID, &m.Seq, &m.TS, &m.From, &row.entry.SenderSession, &m.To, &row.payload, &m.InReplyTo, &m.AckRequested, &origin, &group, &room, &mentions)
	if err != nil {
		return row, err
	}
	m.Room = room.String
	if m.Mentions, err = groupList(mentions); err != nil {
		return row, err
	}
	m.Type, m.Durable, m.Origin = protocol.TypeMsg, true, protocol.DisplayOrigin(origin.String)
	if m.Group, err = groupList(group); err != nil {
		return row, err
	}
	row.entry.Direction = "received"
	if origin.String == protocol.OriginOperator {
		row.entry.Direction = "sent"
	}
	return row, err
}

// attachHistoryStages adds current recipient stages to sent entries and the
// operator's own receipt stage to received ones.
func attachHistoryStages(ctx context.Context, tx *sql.Tx, entry *protocol.HistoryEntry) error {
	if entry.Direction == "received" {
		var received, acknowledged sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT received_at, acknowledged_at FROM durable_inbox WHERE message_id = ? AND agent_id = ?`, entry.Message.MessageID, protocol.OperatorID).Scan(&received, &acknowledged)
		entry.OperatorStage = currentStage(received, acknowledged)
		return err
	}
	stages, err := stagesIn(ctx, tx, StageQuery{MessageID: entry.Message.MessageID, Limit: protocol.HistoryStages + 1})
	if err != nil {
		return err
	}
	entry.ReceiptsTruncated = len(stages) > protocol.HistoryStages
	entry.Recipients = stages[:min(len(stages), protocol.HistoryStages)]
	return nil
}

func historyBelow(ctx context.Context, tx *sql.Tx, page protocol.HistoryPage) (bool, error) {
	cut := page.NextBeforeSeq
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_metadata mm JOIN messages m USING(seq) WHERE `+historyScope+` AND m.seq <= ? AND (? = 0 OR m.seq < ?)`, page.UpperSeq, cut, cut).Scan(&count)
	// An empty page from a scan without a cut has nothing below it.
	return count > 0 && (cut != 0 || len(page.Entries) > 0), err
}

// retainedPayload shows a message stored without a payload as JSON null, so an
// absent body is never confused with an omitted one.
func retainedPayload(stored string) json.RawMessage {
	if stored == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(stored)
}
