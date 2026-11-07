package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

type offlineInboxFixture struct {
	database, output string
	db               *store.Store
	seq              uint64
}

func newOfflineInbox(t *testing.T) *offlineInboxFixture {
	t.Helper()
	root := t.TempDir()
	source, output := filepath.Join(root, "source"), filepath.Join(root, "backup")
	offlineOK(t, os.Mkdir(source, 0700))
	offlineOK(t, os.Mkdir(output, 0700))
	f := &offlineInboxFixture{database: filepath.Join(source, "broker.db"), output: filepath.Join(output, "saved.sqlite")}
	f.db = f.open(t)
	return f
}

func offlineOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *offlineInboxFixture) open(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(f.database)
	offlineOK(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func offlineSQL(t *testing.T, path string, readonly bool) *sql.DB {
	t.Helper()
	query := "_pragma=busy_timeout(5000)"
	if readonly {
		query += "&mode=ro"
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: query}
	db, err := sql.Open("sqlite", uri.String())
	offlineOK(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func (f *offlineInboxFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db := offlineSQL(t, f.database, false)
	_, err := db.Exec(query, args...)
	offlineOK(t, err)
}

func (f *offlineInboxFixture) call(t *testing.T, verb string, flags ...string) (int, []byte, string) {
	t.Helper()
	offlineOK(t, f.db.Close())
	args := []string{"operator", "inbox", verb, f.output, "--database", f.database, "--json"}
	args = append(args, flags...)
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := run(ctx, args, &stdout, &stderr)
	return code, stdout.Bytes(), stderr.String()
}

func offlineResult(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	offlineOK(t, json.Unmarshal(data, &result))
	return result
}

func offlineMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %#v", value)
	}
	return result
}
