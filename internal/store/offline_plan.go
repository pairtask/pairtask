package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Why an archive keeps a retained message (#119), in order: a message is
// counted once, under the first that applies.
var archiveReasons = []string{"after_upper", "unacknowledged", "legacy_unacknowledged", "claim", "assignment",
	"pending_audit", "paired", "peer_linked", "pending_import", "review", "unsettled_ask", "reply_child"}

// archiveCandidates are the retained messages, each with whether it is
// after the inclusive upper sequence ?1, and the first unfinished reason it
// is kept for, "" for none.
const archiveCandidates = `SELECT d.message_id, mm.seq, d.storage_bytes, mm.in_reply_to, mm.seq > ?1, CASE
  WHEN EXISTS (SELECT 1 FROM durable_inbox i WHERE i.message_id = d.message_id AND i.acknowledged_at IS NULL) THEN 'unacknowledged'
  WHEN mm.ack_requested = 1 AND EXISTS (SELECT 1 FROM message_receipts r WHERE r.message_id = d.message_id AND r.agent_acknowledged_at IS NULL) THEN 'legacy_unacknowledged'
  WHEN EXISTS (SELECT 1 FROM claims c WHERE c.operator_message_id = d.message_id AND c.state = 'active') THEN 'claim'
  WHEN EXISTS (SELECT 1 FROM assignments a WHERE a.operator_message_id = d.message_id AND a.state = 'assigned') THEN 'assignment'
  WHEN EXISTS (SELECT 1 FROM operator_audit o WHERE o.message_id = d.message_id AND o.result = 'pending') THEN 'pending_audit'
  WHEN EXISTS (SELECT 1 FROM paired_threads p WHERE p.local_id = d.message_id OR p.root_local_id = d.message_id) THEN 'paired'
  WHEN EXISTS (SELECT 1 FROM peer_messages p WHERE p.local_id = d.message_id) THEN 'peer_linked'
  WHEN EXISTS (SELECT 1 FROM remote_imports r WHERE r.local_id = d.message_id AND r.state IN ('pending', 'awaiting')) THEN 'pending_import'
  WHEN EXISTS (SELECT 1 FROM reviews v WHERE (v.review_id = mm.review_id OR 'review:' || v.review_id = d.message_id) AND v.state IN ` + openStates + `) THEN 'review'
  WHEN EXISTS (SELECT 1 FROM review_asks_out a WHERE a.local_id = d.message_id AND a.settled_at IS NULL) THEN 'unsettled_ask'
  ELSE '' END
FROM durable_messages d JOIN message_metadata mm USING(message_id) ORDER BY mm.seq`

// archiveCandidate is one retained message and why it stays, if it does:
// after the upper bound, or for unfinished work, which also keeps the
// messages it replies to wherever it is.
type archiveCandidate struct {
	id, parent, unfinished string
	seq                    uint64
	bytes                  int64
	afterUpper             bool
}

// reason is the first reason the message is kept, "" for none; context is
// whether an unfinished reply needs it.
func (c archiveCandidate) reason(context bool) string {
	switch {
	case c.afterUpper:
		return "after_upper"
	case c.unfinished != "":
		return c.unfinished
	case context:
		return "reply_child"
	}
	return ""
}

// archivePlan is what an archive frees and what it keeps.
type archivePlan struct {
	eligible []archiveCandidate
	kept     map[string]int
}

func (p archivePlan) freed() ArchiveUsage {
	usage := ArchiveUsage{Messages: int64(len(p.eligible))}
	for _, c := range p.eligible {
		usage.Bytes += c.bytes
	}
	return usage
}

// planArchive reads every retained message up to upper and decides it.
func planArchive(ctx context.Context, q queryer, upper uint64) (archivePlan, error) {
	candidates, err := readCandidates(ctx, q, upper)
	if err != nil {
		return archivePlan{}, err
	}
	needed, err := replyContext(ctx, q, candidates)
	if err != nil {
		return archivePlan{}, err
	}
	plan := archivePlan{kept: map[string]int{}}
	for _, reason := range archiveReasons {
		plan.kept[reason] = 0
	}
	for _, c := range candidates {
		plan.add(c, c.reason(needed[c.id]))
	}
	return plan, nil
}

func (p *archivePlan) add(c archiveCandidate, reason string) {
	if reason != "" {
		p.kept[reason]++
		return
	}
	p.eligible = append(p.eligible, c)
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readCandidates(ctx context.Context, q queryer, upper uint64) ([]archiveCandidate, error) {
	rows, err := q.QueryContext(ctx, archiveCandidates, upper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []archiveCandidate
	for rows.Next() {
		var c archiveCandidate
		if err := rows.Scan(&c.id, &c.seq, &c.bytes, &c.parent, &c.afterUpper, &c.unfinished); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrArchiveContext: unfinished replies name more context than the archive
// can follow; it archives nothing rather than guess.
var ErrArchiveContext = errors.New("unfinished replies name more context than an archive follows; nothing archived")

// replyContext is every message an unfinished reply needs, after the upper
// bound or not: its parent, that parent's, and so on, up to an archived
// parent. A cycle ends where it repeats.
func replyContext(ctx context.Context, q queryer, candidates []archiveCandidate) (map[string]bool, error) {
	walk := contextWalk{ctx: ctx, q: q, seen: map[string]bool{}}
	for _, c := range candidates {
		if c.unfinished == "" {
			continue
		}
		if err := walk.from(c.parent); err != nil {
			return nil, err
		}
	}
	return walk.seen, nil
}

type contextWalk struct {
	ctx  context.Context
	q    queryer
	seen map[string]bool
}

func (w *contextWalk) from(id string) error {
	for id != "" && !w.seen[id] {
		if len(w.seen) > MaxRetainedMessages {
			return ErrArchiveContext
		}
		parent, archived, err := w.parentOf(id)
		if err != nil || archived {
			return err
		}
		w.seen[id] = true
		id = parent
	}
	return nil
}

// parentOf is a message's parent, and whether it is archived; an unknown
// message has neither.
func (w *contextWalk) parentOf(id string) (string, bool, error) {
	var parent string
	var archived bool
	err := w.q.QueryRowContext(w.ctx, `SELECT in_reply_to, archived_at IS NOT NULL FROM message_metadata WHERE message_id = ?`, id).Scan(&parent, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read reply context: %w", err)
	}
	return parent, archived, nil
}
