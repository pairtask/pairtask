package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// An archive (#119) frees retained capacity offline: it keeps every message's
// identity, sequence, headers, audience and receipts, replaces the body of
// each finished message with a marker, marks it archived, and drops its
// retained accounting. Nothing a later message or unfinished work still
// needs is touched, and a backup of the whole database comes first.

// ArchivedPayload replaces an archived message's body.
const ArchivedPayload = `{"text":"(archived)","archived":true}`

// ArchiveRequest is one archive: up to Upper, inclusive (the head when
// unset), into the backup at Backup, or only planned.
type ArchiveRequest struct {
	Upper    uint64
	HasUpper bool
	DryRun   bool
	Backup   string
}

// ArchiveUsage counts retained messages and their charged bytes.
type ArchiveUsage struct {
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

// RetainedUsage is retained capacity before and after, against its limits.
type RetainedUsage struct {
	Before        ArchiveUsage `json:"before"`
	After         ArchiveUsage `json:"after"`
	LimitMessages int64        `json:"limit_messages"`
	LimitBytes    int64        `json:"limit_bytes"`
}

// ArchiveReport is what an archive did, or would do: no backup when planned.
type ArchiveReport struct {
	Backup       *Backup        `json:"backup"`
	Upper        uint64         `json:"upper"`
	DryRun       bool           `json:"dry_run"`
	Archived     ArchiveUsage   `json:"archived"`
	KeptByReason map[string]int `json:"kept_by_reason"`
	Retained     RetainedUsage  `json:"retained"`
}

// ErrArchiveSchema: the database predates archiving, and maintenance never
// migrates.
var ErrArchiveSchema = errors.New("the database predates archiving and maintenance never migrates it: start the current broker once, stop it, then archive")

// ErrArchiveUpper: the upper sequence is beyond the last message.
var ErrArchiveUpper = errors.New("--upper is beyond the last message")

// Archive backs the database up, then archives in one transaction; a
// failed archive changes nothing and keeps the backup.
func (o *OfflineDatabase) Archive(ctx context.Context, req ArchiveRequest) (ArchiveReport, error) {
	report, err := o.planned(ctx, req)
	if err != nil || req.DryRun {
		return report, err
	}
	backup, err := o.Export(ctx, req.Backup)
	if err != nil {
		return ArchiveReport{}, err
	}
	if err := o.apply(ctx, &report, backup); err != nil {
		return ArchiveReport{}, fmt.Errorf("archive failed and changed nothing; the backup %s is complete: %w", backup.Path, err)
	}
	report.Backup = &backup
	return report, nil
}

// planned checks the schema and the bound, and reports the plan.
func (o *OfflineDatabase) planned(ctx context.Context, req ArchiveRequest) (ArchiveReport, error) {
	if err := archiveSchema(ctx, o.db); err != nil {
		return ArchiveReport{}, err
	}
	upper, err := archiveUpper(ctx, o.db, req)
	if err != nil {
		return ArchiveReport{}, err
	}
	plan, err := planArchive(ctx, o.db, upper)
	if err != nil {
		return ArchiveReport{}, err
	}
	before, err := retainedUsage(ctx, o.db)
	if err != nil {
		return ArchiveReport{}, err
	}
	return planReport(plan, upper, before, req.DryRun), nil
}

func planReport(plan archivePlan, upper uint64, before ArchiveUsage, dryRun bool) ArchiveReport {
	freed := plan.freed()
	after := ArchiveUsage{Messages: before.Messages - freed.Messages, Bytes: before.Bytes - freed.Bytes}
	return ArchiveReport{Upper: upper, DryRun: dryRun, Archived: freed, KeptByReason: plan.kept,
		Retained: RetainedUsage{Before: before, After: after, LimitMessages: MaxRetainedMessages, LimitBytes: MaxRetainedBytes}}
}

func archiveSchema(ctx context.Context, q queryer) error {
	var columns int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('message_metadata') WHERE name = 'archived_at'`).Scan(&columns)
	if err != nil {
		return err
	}
	if columns == 0 {
		return ErrArchiveSchema
	}
	return nil
}

// archiveUpper is the requested bound, which must not pass the last
// message, or the last message.
func archiveUpper(ctx context.Context, q queryer, req ArchiveRequest) (uint64, error) {
	var head uint64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&head); err != nil {
		return 0, err
	}
	if !req.HasUpper {
		return head, nil
	}
	if req.Upper > head {
		return 0, fmt.Errorf("%w (#%d)", ErrArchiveUpper, head)
	}
	return req.Upper, nil
}

func retainedUsage(ctx context.Context, q queryer) (ArchiveUsage, error) {
	var usage ArchiveUsage
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(storage_bytes), 0) FROM durable_messages`).Scan(&usage.Messages, &usage.Bytes)
	return usage, err
}

// apply archives the plan, read again in the transaction, and audits it.
func (o *OfflineDatabase) apply(ctx context.Context, report *ArchiveReport, backup Backup) error {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, err := planArchive(ctx, tx, report.Upper)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, c := range plan.eligible {
		if err := archiveMessage(ctx, tx, c, now); err != nil {
			return err
		}
	}
	before := report.Retained.Before
	*report = planReport(plan, report.Upper, before, false)
	if err := insertAudit(ctx, tx, archiveAudit(*report, backup)); err != nil {
		return err
	}
	return tx.Commit()
}

func archiveMessage(ctx context.Context, tx *sql.Tx, c archiveCandidate, now time.Time) error {
	steps := []struct {
		query string
		args  []any
	}{
		{`UPDATE messages SET payload = ? WHERE seq = ?`, []any{ArchivedPayload, c.seq}},
		{`UPDATE message_metadata SET archived_at = ? WHERE message_id = ?`, []any{stamp(now), c.id}},
		{`DELETE FROM durable_messages WHERE message_id = ?`, []any{c.id}},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return fmt.Errorf("archive %q: %w", c.id, err)
		}
	}
	return nil
}

// archiveAudit records the archive without any message's content.
func archiveAudit(report ArchiveReport, backup Backup) OperatorAudit {
	detail, _ := json.Marshal(map[string]any{"upper": report.Upper, "messages": report.Archived.Messages,
		"bytes": report.Archived.Bytes, "backup_sha256": backup.SHA256}) // plain values always encode
	return OperatorAudit{Action: "inbox_archive", PrincipalUID: os.Getuid(), Result: AuditAccepted, Detail: string(detail)}
}
