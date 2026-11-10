package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestClassifierCLIAcceptanceReportWireAndEvidence(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) { checkClassifierCLIReport(t, asJSON) })
	}
}
func checkClassifierCLIReport(t *testing.T, asJSON bool) {
	t.Helper()
	r := protocol.ClassifierReport{Since: time.Now().Add(-time.Hour), Count: 3, Threshold: .7, Outcomes: map[string]int{"predicted": 2, "timeout": 1}, Models: map[string]int{"typesafe/jev-latest/jev-1.14.0": 3}, Agreement: protocol.ClassifierAgreement{Compared: 2, Agreed: 1, Disagreed: 1}, LatencyMS: protocol.ClassifierLatency{Asked: 3, P50: 215, P95: 5000, Max: 5000}}
	socket, requests := classifierReportSocket(t, r)
	args := []string{"operator", "classifier", "report", "--socket", socket, "--since", "2h"}
	if asJSON {
		args = append(args, "--json")
	}
	var out, stderr bytes.Buffer
	before := time.Now().Add(-2 * time.Hour)
	code := run(context.Background(), args, &out, &stderr)
	after := time.Now().Add(-2 * time.Hour)
	classifierCLIEqual(t, code, 0)
	checkClassifierRequest(t, requests, before, after)
	checkClassifierOutput(t, out.Bytes(), asJSON)
}
func classifierReportSocket(t *testing.T, r protocol.ClassifierReport) (string, <-chan protocol.Message) {
	t.Helper()
	requests := make(chan protocol.Message, 1)
	socket := statusSocket(t, func(c net.Conn) {
		var req protocol.Message
		if protocol.ReadFrame(bufio.NewReader(c), &req) != nil {
			return
		}
		requests <- req
		protocol.WriteFrame(c, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierReport: &r})
	})
	return socket, requests
}
func checkClassifierRequest(t *testing.T, requests <-chan protocol.Message, before, after time.Time) {
	t.Helper()
	select {
	case req := <-requests:
		classifierCLIEqual(t, req.Type, protocol.TypeOperatorClassifierReport)
		classifierCLIEqual(t, req.ClassifierRequest != nil, true)
		classifierCLIEqual(t, req.ClassifierRequest.Since.Before(before), false)
		classifierCLIEqual(t, req.ClassifierRequest.Since.After(after), false)
	case <-time.After(time.Second):
		t.Fatal("no report request")
	}
}
func checkClassifierOutput(t *testing.T, data []byte, asJSON bool) {
	t.Helper()
	if asJSON {
		var reply protocol.Message
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatal(err)
		}
		classifierCLIEqual(t, reply.ClassifierReport != nil, true)
		classifierCLIEqual(t, reply.ClassifierReport.Agreement.Compared, 2)
		return
	}
	for _, text := range []string{"shadow", "none applied", "uncalibrated", "weak label", "jev-1.14.0", "215", "5000"} {
		if !strings.Contains(string(data), text) {
			t.Errorf("text missing %q: %s", text, data)
		}
	}
}
func classifierCLIEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestClassifierCLIAcceptanceInvalidReportArguments(t *testing.T) {
	for _, rest := range [][]string{{}, {"assign"}, {"report", "--since", "0s"}, {"report", "--since", "-1h"}, {"report", "--since", "721h"}, {"report", "extra"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"operator", "classifier"}, rest...)
		if len(rest) > 0 && rest[0] == "report" {
			args = append(args, "--socket", filepath.Join(t.TempDir(), "missing.sock"))
		}
		if code := run(context.Background(), args, &out, &stderr); code != 2 {
			t.Errorf("args %v exit=%d stderr=%s", rest, code, stderr.String())
		}
		if out.Len() != 0 {
			t.Error("usage error polluted stdout")
		}
	}
}

func TestClassifierCLIAcceptanceEscapesTerminalControls(t *testing.T) {
	r := protocol.ClassifierReport{Models: map[string]int{"model\x1b[2J": 1}, Reasons: map[string]int{"reason\x1b[2J": 1}, Predictions: []protocol.ClassifierPrediction{{MessageID: "message\x1b[2J", Outcome: "error", Reason: "reason\x1b[2J", AgentID: "agent\x1b[2J", Claimant: "claimant\x1b[2J"}}}
	socket := statusSocket(t, func(c net.Conn) {
		var req protocol.Message
		if protocol.ReadFrame(bufio.NewReader(c), &req) == nil {
			protocol.WriteFrame(c, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierReport: &r})
		}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "classifier", "report", "--socket", socket}, &out, &stderr)
	if code != 0 {
		t.Fatalf("report failed: %s", stderr.String())
	}
	if strings.ContainsRune(out.String()+stderr.String(), '\x1b') {
		t.Fatal("report emitted terminal controls from stored/provider fields")
	}
}
