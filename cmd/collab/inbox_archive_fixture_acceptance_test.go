package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

type offlineUsage struct{ messages, bytes int64 }

func offlineUsageReport(t *testing.T, value any, want offlineUsage) {
	t.Helper()
	usage := offlineMap(t, value)
	assertStatusEqual(t, "retained message count", offlineJSONCount(t, usage, "messages"), want.messages)
	assertStatusEqual(t, "retained charged bytes", offlineJSONCount(t, usage, "bytes"), want.bytes)
}

func offlineArchiveMetadata(t *testing.T, f *offlineInboxFixture, result map[string]any) {
	t.Helper()
	backup := offlineMap(t, result["backup"])
	data, err := os.ReadFile(f.output)
	offlineOK(t, err)
	assertStatusEqual(t, "archive backup path", backup["path"], any(f.output))
	assertStatusEqual(t, "archive backup hash", backup["sha256"], any(fmt.Sprintf("%x", sha256.Sum256(data))))
	assertStatusEqual(t, "archive backup bytes", offlineJSONCount(t, backup, "bytes"), int64(len(data)))
	assertStatusEqual(t, "archive is not dry-run", result["dry_run"], any(false))
}

func (f *offlineInboxFixture) archive(t *testing.T, flags ...string) map[string]any {
	t.Helper()
	flags = append([]string{"--confirm"}, flags...)
	code, output, stderr := f.call(t, "archive", flags...)
	assertStatusEqual(t, stderr, code, 0)
	return offlineResult(t, output)
}

func (f *offlineInboxFixture) payload(t *testing.T, id string) string {
	t.Helper()
	var payload string
	err := offlineSQL(t, f.database, true).QueryRow(`SELECT m.payload FROM messages m JOIN message_metadata mm USING(seq) WHERE mm.message_id=?`, id).Scan(&payload)
	offlineOK(t, err)
	return payload
}

func (f *offlineInboxFixture) archived(t *testing.T, id string) {
	t.Helper()
	if !strings.Contains(strings.ToLower(f.payload(t, id)), "archived") {
		t.Fatalf("%s has no explicit archived payload marker", id)
	}
	var marked bool
	err := offlineSQL(t, f.database, true).QueryRow(`SELECT archived_at IS NOT NULL FROM message_metadata WHERE message_id=?`, id).Scan(&marked)
	offlineOK(t, err)
	assertStatusEqual(t, "independent archive marker", marked, true)
}

func (f *offlineInboxFixture) retained(t *testing.T) (int64, int64) {
	t.Helper()
	var count, bytes int64
	err := offlineSQL(t, f.database, true).QueryRow(`SELECT COUNT(*),COALESCE(SUM(storage_bytes),0) FROM durable_messages`).Scan(&count, &bytes)
	offlineOK(t, err)
	return count, bytes
}

func offlineJSONCount(t *testing.T, result map[string]any, key string) int64 {
	t.Helper()
	n, ok := result[key].(float64)
	if !ok {
		t.Fatalf("%s is not a JSON number: %#v", key, result[key])
	}
	return int64(n)
}
