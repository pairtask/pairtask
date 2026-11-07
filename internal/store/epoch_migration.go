package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The pairing-epoch transition (#276): one atomic transaction, run by Open
// after the released columns, that puts pairing_epoch into the keys of
// every per-contact table, adds the epoch columns elsewhere and creates the
// exchange ledger. A released database is copied table by table; a fresh
// one already has the new shape and only gains the rest. Any failure rolls
// all of it back, and the next Open tries again.

// migrationHook is called inside the transition before each step; nil in
// production. Tests set it through export_test.go.
var migrationHook func(step string) error

// epochSchemaStep marks the transition done in schema_steps.
const epochSchemaStep = "pairing_epoch"

// migrationRebuildTables are the tables the transition rebuilds with
// pairing_epoch in their keys, in rebuild order.
var migrationRebuildTables = []string{
	"peer_messages", "peer_message_sequences", "peer_message_floors", "peer_ack_floors",
	"peer_agent_reads", "peer_agent_acks", "remote_imports",
	"peer_grants", "peer_review_grants", "peer_directory",
	"review_asks_out", "review_outcome_seq", "review_outcomes_out",
	"consent_requests", "consent_held_out", "consent_decisions", "consent_sessions",
}

// epochTableColumns is each rebuilt table's new definition, as its schema
// string creates it.
var epochTableColumns = map[string]string{
	"peer_messages":          peerMessagesColumns,
	"peer_message_sequences": peerMessageSequencesColumns,
	"peer_message_floors":    peerMessageFloorsColumns,
	"peer_ack_floors":        peerAckFloorsColumns,
	"peer_agent_reads":       peerAgentReadsColumns,
	"peer_agent_acks":        peerAgentAcksColumns,
	"remote_imports":         remoteImportsColumns,
	"peer_grants":            peerGrantsColumns,
	"peer_review_grants":     peerReviewGrantsColumns,
	"peer_directory":         peerDirectoryColumns,
	"review_asks_out":        reviewAsksOutColumns,
	"review_outcome_seq":     reviewOutcomeSeqColumns,
	"review_outcomes_out":    reviewOutcomesOutColumns,
	"consent_requests":       consentRequestsColumns,
	"consent_held_out":       consentHeldOutColumns,
	"consent_decisions":      consentDecisionsColumns,
	"consent_sessions":       consentSessionsColumns,
}

// schemaStepsSchema records one-time transitions by name.
const schemaStepsSchema = `
CREATE TABLE IF NOT EXISTS schema_steps (
    name       TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);
`

// epochIndex is one index on a rebuilt table, listed explicitly: never
// replayed from the released SQL.
type epochIndex struct{ name, table, columns string }

var epochIndexes = []epochIndex{
	{"peer_messages_state", "peer_messages", "contact, pairing_epoch, direction, state"},
	{"consent_requests_state", "consent_requests", "state, contact, pairing_epoch"},
	{"remote_imports_pending", "remote_imports", "state, staged_at"},
}

// epochColumns are the columns the transition adds to tables it doesn't
// rebuild, each only where it is missing.
var epochColumns = []struct{ table, column, decl string }{
	{"peer_contacts", "pairing_epoch", "INTEGER NOT NULL DEFAULT 1"},
	{"peer_contacts", "pairing_id", "BLOB"}, // NULL: legacy
	{"peer_contacts", "pairing_op", "TEXT NOT NULL DEFAULT ''"},
	{"peer_contacts", "authority_mark", "INTEGER NOT NULL DEFAULT 0"},
	{"peer_contacts", "unpaired_at", "TEXT NOT NULL DEFAULT ''"},
	{"peer_contacts", "retired_xseq", "INTEGER NOT NULL DEFAULT 0"},
	{"peer_contacts", "peer_state", "TEXT NOT NULL DEFAULT ''"},
	{"peer_contacts", "peer_state_gen", "INTEGER NOT NULL DEFAULT 0"},
	{"peer_contacts", "peer_state_at", "TEXT"},
	{"peer_contacts", "peer_version", "INTEGER"},
	{"peer_pairing_requests", "operation_id", "TEXT NOT NULL DEFAULT ''"},
	{"peer_joins", "retired_at", "TEXT"},
	{"peer_joins", "begun_mark", "INTEGER NOT NULL DEFAULT 0"},
	// The identity the operator declared a join repairs, '' for none.
	{"peer_joins", "repair", "TEXT NOT NULL DEFAULT ''"},
	{"reviews", "remote_epoch", "INTEGER"},          // NULL is 1
	{"message_metadata", "remote_epoch", "INTEGER"}, // NULL is 1
	{"paired_threads", "pairing_epoch", "INTEGER NOT NULL DEFAULT 1"},
	{"paired_reply_outcomes", "pairing_epoch", "INTEGER NOT NULL DEFAULT 1"},
	{"contact_address_ops", "pairing_epoch", "INTEGER NOT NULL DEFAULT 1"},
}

// epochLedgerSchema is the exchange ledger, the authority marks, unpair
// results and outbound contact notices.
const epochLedgerSchema = `
CREATE TABLE IF NOT EXISTS pairing_exchanges (
    xseq            INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id    TEXT NOT NULL UNIQUE,
    identity_key    TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('inviter', 'joiner')),
    kind            TEXT NOT NULL CHECK (kind IN ('pair', 'repair', 'address_move')),
    repair_key      TEXT NOT NULL DEFAULT '', -- the declared repair, written once at commit
    transcript_id   BLOB NOT NULL,
    begun_mark      INTEGER NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('open', 'installed', 'retired', 'refused', 'legacy')),
    mode            TEXT NOT NULL CHECK (mode IN ('epoch', 'legacy')),
    decision        TEXT NOT NULL DEFAULT 'pending' CHECK (decision IN ('pending', 'accepted', 'rejected')),
    decided_at      TEXT,
    delivered_at    TEXT,
    installed_at    TEXT,
    installed_epoch INTEGER,
    retired_at      TEXT,
    reason          TEXT NOT NULL DEFAULT '',
    recorded_at     TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pairing_marks (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    mark INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS contact_unpair_results (
    operation_id  TEXT PRIMARY KEY,
    changed       INTEGER NOT NULL,
    generation    INTEGER NOT NULL,
    pairing_epoch INTEGER NOT NULL,
    unpaired_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS contact_notices_out (
    contact       TEXT NOT NULL,
    pairing_id    BLOB,
    gen           INTEGER NOT NULL,
    contact_state TEXT NOT NULL CHECK (contact_state IN ('paused', 'resumed', 'unpaired')), -- the state the notice carries
    state         TEXT NOT NULL CHECK (state IN ('pending', 'delivered', 'undelivered', 'superseded')), -- its delivery
    attempts      INTEGER NOT NULL DEFAULT 0, -- every attempt, the answered one included
    next_attempt_at TEXT, -- when a pending notice is next due, NULL for now
    delivered_at  TEXT,
    PRIMARY KEY (contact, gen)
);
INSERT OR IGNORE INTO pairing_marks (id, mark) VALUES (1, 0);
`

var errEpochShape = errors.New("unsupported schema")

// epochMarked: the transition is done.
func epochMarked(ctx context.Context, db *sql.DB) (bool, error) {
	var marked int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_steps WHERE name = ?`, epochSchemaStep).Scan(&marked)
	return marked != 0, err
}

// restoreEpochTables creates, in a database the transition is done for,
// each epoch-keyed table it lacks, in the new shape: Open's schema strings
// no longer do. Before the transition it does nothing; the transition
// creates them in its own transaction.
func restoreEpochTables(db *sql.DB, existing map[string]bool) error {
	ctx := context.Background()
	marked, err := epochMarked(ctx, db)
	if err != nil || !marked {
		return err
	}
	for _, table := range migrationRebuildTables {
		if existing[table] {
			continue
		}
		if err := createEpochTable(ctx, db, epochTable(table)); err != nil {
			return fmt.Errorf("pairing epoch: %s: %w", table, err)
		}
	}
	return nil
}

func createEpochTable(ctx context.Context, db *sql.DB, t epochTable) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range append(t.createStatements(t), t.indexStatements()...) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// epochTransition runs the transition once: a database marked done returns
// at once, calling no step. existing are the tables the file held before
// this Open created any.
func epochTransition(db *sql.DB, existing map[string]bool) error {
	ctx := context.Background()
	marked, err := epochMarked(ctx, db)
	if err != nil || marked {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pairing epoch: %w", err)
	}
	defer conn.Close()
	if err := withoutForeignKeys(ctx, conn, (&epochRun{ctx: ctx, conn: conn, existing: existing}).run); err != nil {
		return fmt.Errorf("pairing epoch: %w", err)
	}
	return nil
}

// withoutForeignKeys runs fn with foreign keys off on conn, restoring them
// after if they were on. The pragma is a no-op inside a transaction, so
// this is outside it.
func withoutForeignKeys(ctx context.Context, conn *sql.Conn, fn func() error) error {
	var on int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil {
		return err
	}
	if on == 0 {
		return fn()
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	err := fn()
	if _, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err == nil {
		err = restoreErr
	}
	return err
}

// epochRun is one attempt at the transition, on one connection.
type epochRun struct {
	ctx      context.Context
	conn     *sql.Conn
	tx       *sql.Tx
	existing map[string]bool // the tables there before this Open
	legacy   []epochTable    // existing tables of the released shape: rebuild them
	seeds    []string        // "table.column" for each released column the transition adds
}

type epochStep struct {
	name string
	fn   func() error
}

func (r *epochRun) run() error {
	defer r.rollback()
	for _, s := range []epochStep{{"begin", r.begin}, {"shape", r.shape}} {
		if err := r.step(s); err != nil {
			return err
		}
	}
	for _, s := range r.laterSteps() {
		if err := r.step(s); err != nil {
			return err
		}
	}
	return nil
}

// laterSteps are the steps after the shape check: a rebuild for each
// existing table of the released shape.
func (r *epochRun) laterSteps() []epochStep {
	var steps []epochStep
	for _, table := range r.legacy {
		steps = append(steps, epochStep{"rebuild:" + string(table), r.rebuilder(table)})
	}
	return append(steps,
		epochStep{"indexes", r.indexes},
		epochStep{"columns", r.columns},
		epochStep{"ledger", r.exec(epochLedgerSchema)},
		epochStep{"foreign_key_check", r.foreignKeyCheck},
		epochStep{"marker", r.marker},
		epochStep{"commit", r.commit},
	)
}

// step calls the hook, then the step; either error names the step.
func (r *epochRun) step(s epochStep) error {
	if migrationHook != nil {
		if err := migrationHook(s.name); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	if err := s.fn(); err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}
	return nil
}

func (r *epochRun) begin() error {
	tx, err := r.conn.BeginTx(r.ctx, nil)
	r.tx = tx
	return err
}

func (r *epochRun) rollback() {
	if r.tx != nil {
		_ = r.tx.Rollback()
	}
}

func (r *epochRun) commit() error {
	err := r.tx.Commit()
	r.tx = nil
	return err
}

func (r *epochRun) exec(query string, args ...any) func() error {
	return func() error {
		_, err := r.tx.ExecContext(r.ctx, query, args...)
		return err
	}
}

func (r *epochRun) marker() error {
	return r.exec(`INSERT INTO schema_steps (name, applied_at) VALUES (?, ?)`, epochSchemaStep, msgStamp(time.Now()))()
}

// shape decides the path. A table the file lacks is created in the new
// shape. Of the tables there before, every one has pairing_epoch (nothing
// to rebuild) or none does (rebuild them all). A mix, a column outside the
// known ones, or an index or trigger not listed aborts.
func (r *epochRun) shape() error {
	withEpoch := 0
	for _, table := range migrationRebuildTables {
		has, err := r.classify(epochTable(table))
		if err != nil {
			return err
		}
		if has {
			withEpoch++
		}
	}
	if withEpoch != 0 && len(r.legacy) != 0 {
		return fmt.Errorf("%w: pairing_epoch on %d existing tables, missing on %v", errEpochShape, withEpoch, r.legacy)
	}
	return r.noOtherObjects()
}

// classify checks one table's shape and reports whether it existed with
// pairing_epoch; an existing one without it joins the tables to rebuild. A
// table the file lacks is created here, in the new shape, inside the
// transition, so a failure leaves none behind.
func (r *epochRun) classify(t epochTable) (bool, error) {
	if !r.existing[string(t)] {
		r.addSeeds(t, nil)
		return false, r.create(t)
	}
	has, err := r.tableShape(t)
	if err == nil && !has {
		r.legacy = append(r.legacy, t)
	}
	return has, err
}

// create makes the table in its new shape.
func (r *epochRun) create(t epochTable) error {
	for _, statement := range t.createStatements(t) {
		if err := r.exec(statement)(); err != nil {
			return err
		}
	}
	return nil
}

// createStatements make the table's new shape under the given name, with
// the released added columns its definition lacks.
func (t epochTable) createStatements(named epochTable) []string {
	statements := []string{`CREATE TABLE ` + string(named) + ` ` + t.definition()}
	for _, c := range t.added() {
		statements = append(statements, `ALTER TABLE `+string(named)+` ADD COLUMN `+c.column+` `+c.decl)
	}
	return statements
}

// indexStatements create the listed indexes on the table: a restored table
// gets them with it, since the marked transition's indexes step won't run.
func (t epochTable) indexStatements() []string {
	var statements []string
	for _, ix := range epochIndexes {
		if ix.table == string(t) {
			statements = append(statements, `CREATE INDEX IF NOT EXISTS `+ix.name+` ON `+ix.table+` (`+ix.columns+`)`)
		}
	}
	return statements
}

// tableShape reports whether the table has pairing_epoch, after checking
// its columns are exactly the known ones for that shape.
func (r *epochRun) tableShape(t epochTable) (bool, error) {
	got, err := r.strings(`SELECT name FROM pragma_table_info(?)`, string(t))
	if err != nil {
		return false, err
	}
	sort.Strings(got)
	has := slices.Contains(got, "pairing_epoch")
	if !t.shapeHolds(got, has) {
		return false, fmt.Errorf("%w: %s has columns %v, want %v", errEpochShape, t, got, t.known(has))
	}
	if !has {
		r.addSeeds(t, got)
	}
	return has, nil
}

// shapeHolds: an epoch-shaped table has exactly its known columns; a
// released one has its definition's and only known others, as an earlier
// release may predate some added columns.
func (t epochTable) shapeHolds(got []string, withEpoch bool) bool {
	want := t.known(withEpoch)
	if withEpoch {
		return slices.Equal(got, want)
	}
	for _, c := range got {
		if !slices.Contains(want, c) {
			return false
		}
	}
	return !slices.ContainsFunc(t.defined(), func(c string) bool { return c != "pairing_epoch" && !slices.Contains(got, c) })
}

// addSeeds notes the released columns the transition adds to the table:
// those of its added columns it lacks.
func (r *epochRun) addSeeds(t epochTable, got []string) {
	for _, c := range t.added() {
		if !slices.Contains(got, c.column) {
			r.seeds = append(r.seeds, c.table+"."+c.column)
		}
	}
}

// noOtherObjects: no index or trigger on a rebuilt table but the listed
// indexes and the constraints' own.
func (r *epochRun) noOtherObjects() error {
	listed := map[string]bool{}
	for _, ix := range epochIndexes {
		listed[ix.name] = true
	}
	tables := "'" + strings.Join(migrationRebuildTables, "','") + "'"
	names, err := r.strings(`SELECT name FROM sqlite_schema WHERE type IN ('index', 'trigger') AND sql IS NOT NULL AND tbl_name IN (` + tables + `)`)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !listed[name] {
			return fmt.Errorf("%w: unknown index or trigger %s", errEpochShape, name)
		}
	}
	return nil
}

// epochTable is one of migrationRebuildTables.
type epochTable string

// definition is the table's new column list, as its schema string has it.
func (t epochTable) definition() string { return epochTableColumns[string(t)] }

// staging is the name the table is built under before it takes the
// table's place.
func (t epochTable) staging() epochTable { return t + "__epoch" }

// defined are the column names in the definition, one per line, skipping
// table constraints.
func (t epochTable) defined() []string {
	var cols []string
	for _, line := range strings.Split(t.definition(), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "("))
		if len(fields) < 2 || tableConstraint[fields[0]] {
			continue
		}
		cols = append(cols, fields[0])
	}
	return cols
}

var tableConstraint = map[string]bool{"PRIMARY": true, "UNIQUE": true, "CHECK": true, "FOREIGN": true, "CONSTRAINT": true}

// added are the table's released added columns its definition lacks.
func (t epochTable) added() []struct{ table, column, decl string } {
	defined := t.defined()
	var out []struct{ table, column, decl string }
	for _, c := range addedColumns {
		if c.table == string(t) && !slices.Contains(defined, c.column) {
			out = append(out, c)
		}
	}
	return out
}

// known are the table's columns, sorted: its definition and its released
// added columns, with or without pairing_epoch.
func (t epochTable) known(withEpoch bool) []string {
	cols := slices.DeleteFunc(t.defined(), func(c string) bool { return !withEpoch && c == "pairing_epoch" })
	for _, c := range t.added() {
		cols = append(cols, c.column)
	}
	sort.Strings(cols)
	return cols
}

func (r *epochRun) strings(query string, args ...any) ([]string, error) {
	rows, err := r.tx.QueryContext(r.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *epochRun) count(query string, args ...any) (int64, error) {
	var n int64
	err := r.tx.QueryRowContext(r.ctx, query, args...).Scan(&n)
	return n, err
}

func (r *epochRun) rebuilder(t epochTable) func() error {
	return func() error { return r.rebuild(t) }
}

// rebuild copies the table into its new shape with explicit columns, so
// cursor and id keep their values, every row at epoch 1, and keeps an
// AUTOINCREMENT high-water mark where it was.
func (r *epochRun) rebuild(t epochTable) error {
	highWater, err := r.count(`SELECT COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = ?`, string(t))
	if err != nil {
		return err
	}
	if err := r.copyTable(t); err != nil {
		return err
	}
	if !strings.Contains(t.definition(), "AUTOINCREMENT") {
		return nil
	}
	return r.keepHighWater(t, highWater)
}

// copyTable builds the staging table, copies every row and checks the
// count, then puts it in the table's place.
func (r *epochRun) copyTable(t epochTable) error {
	statements, err := r.copyStatements(t)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if err := r.exec(statement)(); err != nil {
			return err
		}
	}
	if err := r.sameCount(t); err != nil {
		return err
	}
	if err := r.exec(`DROP TABLE ` + string(t))(); err != nil {
		return err
	}
	return r.exec(`ALTER TABLE ` + string(t.staging()) + ` RENAME TO ` + string(t))()
}

// copyStatements create the staging table with the released added columns
// and copy the rows into it, listing the existing columns.
func (r *epochRun) copyStatements(t epochTable) ([]string, error) {
	cols, err := r.strings(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, string(t))
	if err != nil {
		return nil, err
	}
	list := `"` + strings.Join(cols, `", "`) + `"`
	copyRows := `INSERT INTO ` + string(t.staging()) + ` (` + list + `, pairing_epoch) SELECT ` + list + `, 1 FROM ` + string(t)
	return append(t.createStatements(t.staging()), copyRows), nil
}

func (r *epochRun) sameCount(t epochTable) error {
	before, err := r.count(`SELECT COUNT(*) FROM ` + string(t))
	if err != nil {
		return err
	}
	after, err := r.count(`SELECT COUNT(*) FROM ` + string(t.staging()))
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("%s copied %d of %d rows", t, after, before)
	}
	return nil
}

// keepHighWater writes the table's sqlite_sequence row at the larger of the
// released mark and the highest cursor: delete, then insert, since
// sqlite_sequence has no unique constraint, then check exactly that one row.
func (r *epochRun) keepHighWater(t epochTable, highWater int64) error {
	top, err := r.count(`SELECT COALESCE(MAX(cursor), 0) FROM ` + string(t))
	if err != nil {
		return err
	}
	want := max(highWater, top)
	if err := r.exec(`DELETE FROM sqlite_sequence WHERE name = ?`, string(t))(); err != nil {
		return err
	}
	if err := r.exec(`INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)`, string(t), want)(); err != nil {
		return err
	}
	var rows, seq int64
	if err := r.tx.QueryRowContext(r.ctx, `SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = ?`, string(t)).Scan(&rows, &seq); err != nil {
		return err
	}
	if rows != 1 || seq != want {
		return fmt.Errorf("%s sqlite_sequence has %d rows at %d, want 1 at %d", t, rows, seq, want)
	}
	return nil
}

// indexes creates the listed indexes, first dropping one of the same name
// that lacks its listed columns.
func (r *epochRun) indexes() error {
	for _, ix := range epochIndexes {
		if err := r.index(ix); err != nil {
			return err
		}
	}
	return nil
}

func (r *epochRun) index(ix epochIndex) error {
	cols, err := r.strings(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, ix.name)
	if err != nil {
		return err
	}
	if len(cols) != 0 && strings.Join(cols, ", ") != ix.columns {
		if err := r.exec(`DROP INDEX ` + ix.name)(); err != nil {
			return err
		}
	}
	return r.exec(`CREATE INDEX IF NOT EXISTS ` + ix.name + ` ON ` + ix.table + ` (` + ix.columns + `)`)()
}

// columns adds each column the tables don't have yet.
func (r *epochRun) columns() error {
	for _, c := range epochColumns {
		n, err := r.count(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column)
		if err != nil {
			return err
		}
		if n != 0 {
			continue
		}
		if err := r.exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.decl)(); err != nil {
			return err
		}
	}
	return r.seed()
}

// seed runs the callback of each released column the transition added, in
// its transaction, now that every table exists.
func (r *epochRun) seed() error {
	for _, key := range r.seeds {
		if callback := columnAdded[key]; callback != nil {
			if err := callback(r.tx); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}

// foreignKeyCheck aborts on any violation the copy left.
func (r *epochRun) foreignKeyCheck() error {
	rows, err := r.tx.QueryContext(r.ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign key violation")
	}
	return rows.Err()
}
