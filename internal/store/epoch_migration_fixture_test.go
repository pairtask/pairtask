package store_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type epochFixtureManifest struct {
	SQLSHA    string           `json:"sql_sha256"`
	Tag       string           `json:"tag"`
	Commit    string           `json:"commit"`
	Tables    map[string]int   `json:"row_counts"`
	Sequences map[string]int64 `json:"sqlite_sequence"`
	MaxCursor map[string]int64 `json:"max_cursor"`
}

func epochRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func epochReleased(t *testing.T) (string, *sql.DB, epochFixtureManifest) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "released.db")
	db := epochRaw(t, path)
	dump, err := os.ReadFile("testdata/released-847.1.sql")
	pairOK(t, err)
	_, err = db.Exec(string(dump))
	pairOK(t, err)
	data, err := os.ReadFile("testdata/released-847.1.json")
	pairOK(t, err)
	var manifest epochFixtureManifest
	pairOK(t, json.Unmarshal(data, &manifest))
	pairEqual(t, manifest.Tag, "workspace-v0.0.0-build.847.1")
	pairEqual(t, manifest.Commit, "c93024ce5c3410a6b82f59aa625a08e2c632d522")
	pairEqual(t, fmt.Sprintf("%x", sha256.Sum256(dump)), manifest.SQLSHA)
	epochValidateReleasedFixture(t, db, manifest)
	// The released dump has no live session grant. Seed one using only its
	// released columns, after validating the immutable dump and manifest.
	// Migration rollback must preserve it; successful Open must invalidate it.
	_, err = db.Exec(`INSERT INTO consent_sessions
		(contact,agent,operation,formal,epoch,gen,consent_id,decided_at)
		SELECT contact,'migration-agent','message',0,'migration-old-owner',1,id,created_at
		FROM consent_requests ORDER BY cursor LIMIT 1`)
	pairOK(t, err)
	manifest.Tables["consent_sessions"] = epochCount(t, db, "SELECT count(*) FROM consent_sessions")
	return path, db, manifest
}

func epochValidateReleasedFixture(t *testing.T, db *sql.DB, manifest epochFixtureManifest) {
	t.Helper()
	for name, want := range manifest.Tables {
		pairEqual(t, epochCount(t, db, "SELECT count(*) FROM "+epochQuote(name)), want)
	}
	for name, want := range manifest.MaxCursor {
		var got int64
		pairOK(t, db.QueryRow("SELECT coalesce(max(cursor),0) FROM "+epochQuote(name)).Scan(&got))
		pairEqual(t, got, want)
		epochSequence(t, db, name, manifest.Sequences[name])
	}
	epochNoForeignKeyErrors(t, db)
}

func epochQuote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func epochColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	return epochQueryStrings(t, db, "SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
}

func epochQueryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	pairOK(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		pairOK(t, rows.Scan(&value))
		out = append(out, value)
	}
	pairOK(t, rows.Err())
	return out
}

func epochRows(t *testing.T, db *sql.DB, table string, columns []string) []string {
	t.Helper()
	var fields []string
	for _, name := range columns {
		fields = append(fields, "quote("+epochQuote(name)+")")
	}
	out := epochQueryStrings(t, db, "SELECT "+strings.Join(fields, " || ',' || ")+" FROM "+epochQuote(table))
	if out == nil {
		out = []string{}
	}
	sort.Strings(out)
	return out
}

type epochTableSnapshot struct {
	DDL     string
	Columns []string
	Rows    []string
}

func epochSnapshot(t *testing.T, db *sql.DB, manifest epochFixtureManifest) map[string]epochTableSnapshot {
	t.Helper()
	out := map[string]epochTableSnapshot{}
	for name := range manifest.Tables {
		var ddl string
		pairOK(t, db.QueryRow("SELECT sql FROM sqlite_schema WHERE type='table' AND name=?", name).Scan(&ddl))
		cols := epochColumns(t, db, name)
		out[name] = epochTableSnapshot{ddl, cols, epochRows(t, db, name, cols)}
	}
	return out
}

func epochNoForeignKeyErrors(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_check")
	pairOK(t, err)
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation")
	}
	pairOK(t, rows.Err())
}

func epochSequence(t *testing.T, db *sql.DB, table string, want int64) {
	t.Helper()
	var count int
	var sequence int64
	pairOK(t, db.QueryRow("SELECT count(*), coalesce(max(seq),0) FROM sqlite_sequence WHERE name=?", table).Scan(&count, &sequence))
	if count != 1 || sequence != want {
		t.Fatalf("%s sqlite_sequence: count=%d seq=%d want (1,%d)", table, count, sequence, want)
	}
}

func epochCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	pairOK(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func epochSchemaObjects(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return epochQueryStrings(t, db, `SELECT quote(type)||','||quote(name)||','||quote(tbl_name)||','||quote(sql)
FROM sqlite_schema WHERE tbl_name != 'schema_steps' ORDER BY type,name`)
}

func epochIndexColumns(t *testing.T, db *sql.DB, index string) []string {
	t.Helper()
	return epochQueryStrings(t, db, "SELECT name FROM pragma_index_info(?) ORDER BY seqno", index)
}

func epochLabel(step string) string {
	return fmt.Sprintf("step-%s", strings.ReplaceAll(step, ":", "-"))
}
