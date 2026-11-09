package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestClassifierCLIAcceptanceReportWireAndEvidence(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			requests := make(chan protocol.Message, 1)
			r := protocol.ClassifierReport{Since: time.Now().Add(-time.Hour), Count: 3, Threshold: .7, Outcomes: map[string]int{"predicted": 2, "timeout": 1}, Models: map[string]int{"typesafe/jev-latest/jev-1.14.0": 3}, Agreement: protocol.ClassifierAgreement{Compared: 2, Agreed: 1, Disagreed: 1}, LatencyMS: protocol.ClassifierLatency{Asked: 3, P50: 215, P95: 5000, Max: 5000}}
			socket := statusSocket(t, func(c net.Conn) {
				var req protocol.Message
				if protocol.ReadFrame(bufio.NewReader(c), &req) != nil {
					return
				}
				requests <- req
				protocol.WriteFrame(c, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierReport: &r})
			})
			args := []string{"operator", "classifier", "report", "--socket", socket, "--since", "2h"}
			if asJSON {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			before := time.Now().Add(-2 * time.Hour)
			if code := run(context.Background(), args, &out, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			after := time.Now().Add(-2 * time.Hour)
			select {
			case req := <-requests:
				if req.Type != protocol.TypeOperatorClassifierReport || req.ClassifierRequest == nil || req.ClassifierRequest.Since.Before(before) || req.ClassifierRequest.Since.After(after) {
					t.Fatalf("bad report request: %+v", req)
				}
			case <-time.After(time.Second):
				t.Fatal("no report request")
			}
			if asJSON {
				var reply protocol.Message
				if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.ClassifierReport == nil || reply.ClassifierReport.Agreement.Compared != 2 {
					t.Fatalf("JSON report lost evidence: %s", out.String())
				}
			} else {
				for _, text := range []string{"shadow", "none applied", "uncalibrated", "weak label", "jev-1.14.0", "215", "5000"} {
					if !strings.Contains(out.String(), text) {
						t.Errorf("text missing %q: %s", text, out.String())
					}
				}
			}
		})
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
