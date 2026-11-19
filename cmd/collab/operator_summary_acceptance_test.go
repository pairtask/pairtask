//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Uses the existing disposable real broker fixture. No live operator socket.
func summarySeed(t *testing.T, f *offlineInboxFixture, count int) {
	t.Helper()
	f.enroll(t, (offlineSeed{}).recipients())
	for i := 1; i <= count; i++ {
		f.seq++
		spec := offlineSeed{id: fmt.Sprintf("summary-%d", i)}
		msg := spec.message(f.seq)
		msg.From = []string{"alice", "bob"}[i%2]
		if count > 32 {
			msg.From = fmt.Sprintf("agent-%d", i%5)
		}
		msg.Payload, _ = json.Marshal(map[string]string{"text": fmt.Sprintf("text-%d \x1b[2J\r", i) + strings.Repeat("界", 120) + "\nsecond-line\x1b[2J"})
		recipients := spec.recipients()
		f.persist(t, msg, recipients)
	}
}

func summaryCall(t *testing.T, socket string, flags ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"inbox", "summary", "--socket", socket}, flags...)
	var out, errout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := runOperator(ctx, args, &out, &errout)
	return code, out.String(), errout.String()
}

func summaryStart(t *testing.T, f *offlineInboxFixture) *offlineWire {
	t.Helper()
	w := f.wire(t)
	// Wait for this fixture's operator listener; close before running commands.
	p := offlineDial(t, w.operator)
	offlineOK(t, p.Close())
	return w
}

func summaryRows(t *testing.T, output string) []string {
	t.Helper()
	var rows []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "#") {
			rows = append(rows, line)
		}
	}
	return rows
}

func TestOperatorSummaryAcceptanceDefaultNewestFirstAndNoReceiptMutation(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 14)
	w := summaryStart(t, f)
	before, err := f.db.OperatorInboxStatus(context.Background())
	offlineOK(t, err)
	receipts := offlineReceipts(t, f)
	code, out, stderr := summaryCall(t, w.operator)
	if code != 0 {
		t.Fatalf("summary exit%d: %s", code, stderr)
	}
	if !strings.HasPrefix(out, "14 unacknowledged from 2 agents\n") {
		t.Fatalf("wrong header: %q", out)
	}
	rows := summaryRows(t, out)
	if len(rows) != 10 {
		t.Fatalf("default displayed %d rows: %q", len(rows), out)
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, fmt.Sprintf("#%d ", 14-i)) {
			t.Fatalf("not newest first: %q", rows)
		}
	}
	after, err := f.db.OperatorInboxStatus(context.Background())
	offlineOK(t, err)
	if !reflect.DeepEqual(before, after) || receipts != offlineReceipts(t, f) {
		t.Fatal("summary changed receipt/inbox state")
	}
}

func TestOperatorSummaryAcceptanceSkipsAcknowledgedAndOtherAudiences(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 5)
	f.adopt(t, "summary-5", protocol.Recipient{AgentID: "operator", SessionID: "operator-session"}, "")
	f.seed(t, offlineSeed{id: "other-audience", targets: []string{"reader"}, pending: "reader"})
	f.seed(t, offlineSeed{id: "outgoing", operator: true, targets: []string{"reader"}, pending: "reader"})
	w := summaryStart(t, f)
	code, out, stderr := summaryCall(t, w.operator, "--limit", "3")
	if code != 0 {
		t.Fatalf("summary exit%d: %s", code, stderr)
	}
	if !strings.HasPrefix(out, "4 unacknowledged from 2 agents\n") {
		t.Fatalf("wrong scope/count: %q", out)
	}
	rows := summaryRows(t, out)
	if len(rows) != 3 {
		t.Fatalf("limit ignored: %q", out)
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, fmt.Sprintf("#%d ", 4-i)) {
			t.Fatalf("acknowledged or other audience leaked: %q", rows)
		}
	}
}

func TestOperatorSummaryAcceptancePreviewsAreBoundedSingleLineAndValidUTF8(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 1)
	w := summaryStart(t, f)
	code, out, stderr := summaryCall(t, w.operator)
	if code != 0 {
		t.Fatalf("summary exit%d: %s", code, stderr)
	}
	if !utf8.ValidString(out) || strings.ContainsAny(out, "\x1b\r\x00") || strings.Contains(out, "second-line") {
		t.Fatalf("unsafe preview: %q", out)
	}
	rows := summaryRows(t, out)
	if len(rows) != 1 {
		t.Fatalf("preview introduced extra rows: %q", out)
	}
	at := strings.Index(rows[0], "text-1")
	if at < 0 || utf8.RuneCountInString(rows[0][at:]) > 103 {
		t.Fatalf("preview not bounded to100 characters plus ellipsis: %q", rows[0])
	}
}

func TestOperatorSummaryAcceptanceJSONAndLimit50RemainReadOnly(t *testing.T) {
	f := newOfflineInbox(t)
	summarySeed(t, f, 65)
	w := summaryStart(t, f)
	before := offlineReceipts(t, f)
	code, out, stderr := summaryCall(t, w.operator, "--limit", "50")
	if code != 0 {
		t.Fatalf("summary exit%d: %s", code, stderr)
	}
	if len(summaryRows(t, out)) != 50 {
		t.Fatalf("limit50 ignored: %q", out)
	}
	code, out, stderr = summaryCall(t, w.operator, "--limit", "2", "--json")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("bad JSON exit%d: %q %s", code, out, stderr)
	}
	if !strings.Contains(out, "text-65") || !strings.Contains(out, "text-64") || strings.Contains(out, "text-63") {
		t.Fatalf("JSON selection wrong: %s", out)
	}
	if before != offlineReceipts(t, f) {
		t.Fatal("summary consumed messages")
	}
}

func TestOperatorSummaryAcceptanceRejectsBadLimitsBeforeConnecting(t *testing.T) {
	for _, limit := range []string{"0", "-1", "51", "bad"} {
		t.Run(limit, func(t *testing.T) {
			code, out, _ := summaryCall(t, "/does/not/exist", "--limit", limit)
			if code != 2 || out != "" {
				t.Fatalf("bad limit exit%d, out%q", code, out)
			}
		})
	}
}

func TestOperatorSummaryAcceptanceEmptyInboxHasNoPhantomRows(t *testing.T) {
	f := newOfflineInbox(t)
	w := summaryStart(t, f)
	code, out, stderr := summaryCall(t, w.operator)
	if code != 0 || !regexp.MustCompile(`^0 unacknowledged from 0 agents\n`).MatchString(out) {
		t.Fatalf("empty summary: exit%d %q %s", code, out, stderr)
	}
	if len(summaryRows(t, out)) != 0 {
		t.Fatal("empty inbox has messages")
	}
}
