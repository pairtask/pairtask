//go:build linux

package server

import (
	"bytes"
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"runtime/pprof"
	"testing"
	"time"
)

// Inspection uses a separate connection from the broker's serialized store.
func openFixtureInspection(t *testing.T, database string) *sql.DB {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: database, RawQuery: "_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	operatorNoError(t, err)
	return db
}

func TestFixtureInspectionWaitsForWriterCommit(t *testing.T) {
	database := filepath.Join(t.TempDir(), "inspection ?#.db")
	uri := url.URL{Scheme: "file", Path: database}
	writer, err := sql.Open("sqlite", uri.String())
	operatorNoError(t, err)
	defer writer.Close()
	_, err = writer.Exec("CREATE TABLE inspection(value INTEGER); INSERT INTO inspection VALUES(1)")
	operatorNoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := writer.Conn(ctx)
	operatorNoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE; UPDATE inspection SET value=2")
	operatorNoError(t, err)
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	reader := openFixtureInspection(t, database)
	defer reader.Close()
	type result struct {
		value int
		err   error
	}
	read := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		var r result
		r.err = reader.QueryRowContext(ctx, "SELECT value FROM inspection").Scan(&r.value)
		read <- r
	}()
	<-started
	select {
	case r := <-read:
		t.Fatalf("inspection did not wait for writer commit: value=%d err=%v", r.value, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	operatorNoError(t, err)
	select {
	case r := <-read:
		operatorNoError(t, r.err)
		operatorEqual(t, "inspection sees committed value", r.value, 2)
	case <-ctx.Done():
		t.Fatal("inspection did not finish after writer committed")
	}
}

// Cleanup verifies termination, not a per-listener performance target. Many
// connected agents persist their disconnects serially with FULL synchronous
// SQLite writes. Allow one bounded budget for the entire shutdown on CI disks.
func awaitFixtureShutdown(t *testing.T, listeners int, done <-chan error, hubDone <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for listeners > 0 || hubDone != nil {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
			listeners--
			if listeners == 0 {
				done = nil
			}
		case <-hubDone:
			hubDone = nil
		case <-timer.C:
			var stacks bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
			t.Errorf("fixture shutdown exceeded 15s: %d listeners pending, hub pending=%t\n%s", listeners, hubDone != nil, stacks.String())
			return
		}
	}
}
