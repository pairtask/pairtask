package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorClassifierUsage = "usage: pairtask operator classifier report [--socket PATH] [--since 24h] [--json] [--timeout 5s]"

// runOperatorClassifier serves `pairtask operator classifier report`: what
// the shadow classifier recorded, read-only (#339).
func runOperatorClassifier(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "report" {
		fmt.Fprintln(stderr, operatorClassifierUsage)
		return 2
	}
	call, since, code := parseClassifierReport(args[1:], stderr)
	if code != -1 {
		return code
	}
	req := protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierRequest: &protocol.ClassifierReportRequest{Since: time.Now().Add(-since)}}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorClassifierReport, "operator classifier report"}, stderr)
	if err != nil {
		return 1
	}
	if resp.ClassifierReport == nil {
		fmt.Fprintln(stderr, "operator classifier report returned no report")
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeClassifierReport(stdout, *resp.ClassifierReport)
	return 0
}

func parseClassifierReport(args []string, stderr io.Writer) (operatorCall, time.Duration, int) {
	var call operatorCall
	var since time.Duration
	flags := flag.NewFlagSet("operator classifier report", flag.ContinueOnError)
	call.register(flags)
	flags.DurationVar(&since, "since", 24*time.Hour, "how far back to report, greater than zero and at most 720h")
	code := parseFlags(flags, args, operatorClassifierUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("report takes no positional arguments")
		}
		if since <= 0 || since > 720*time.Hour {
			return errors.New("since must be greater than zero and at most 720h")
		}
		return call.validate()
	})
	return call, since, code
}

// writeClassifierReport prints the summary, then the newest predictions.
func writeClassifierReport(w io.Writer, r protocol.ClassifierReport) {
	fmt.Fprintf(w, "Classifier report since %s: %d predictions%s (shadow mode: recorded, none applied; threshold %.2f, uncalibrated)\n",
		r.Since.UTC().Format(time.RFC3339), r.Count, truncatedNote(r.Truncated), r.Threshold)
	fmt.Fprintln(w, "Outcomes: "+counts(r.Outcomes))
	if len(r.Reasons) > 0 {
		fmt.Fprintln(w, "Reasons: "+counts(r.Reasons))
	}
	if len(r.Models) > 0 {
		fmt.Fprintln(w, "Models: "+counts(r.Models))
	}
	fmt.Fprintf(w, "Latency (ms, %d asked): p50 %d, p95 %d, max %d\n", r.LatencyMS.Asked, r.LatencyMS.P50, r.LatencyMS.P95, r.LatencyMS.Max)
	if len(r.Usage) > 0 {
		fmt.Fprintln(w, "Usage (provider counters, summed): "+counts64(r.Usage))
	}
	writeRoles(w, r.Roles)
	a := r.Agreement
	fmt.Fprintf(w, "Agreement with the reply claim (a weak label): %d agreed, %d disagreed of %d compared; %d predicted and unclaimed\n",
		a.Agreed, a.Disagreed, a.Compared, a.Unclaimed)
	for _, p := range r.Predictions {
		line := fmt.Sprintf("  %s %s %s", p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), printable(p.MessageID), printable(p.Outcome))
		if p.Reason != "" {
			line += " (" + printable(p.Reason) + ")"
		}
		if p.AgentID != "" {
			line += fmt.Sprintf(" → %s %.2f", printable(p.AgentID), p.Confidence)
		}
		if p.Claimant != "" {
			line += " claim: " + printable(p.Claimant)
		}
		fmt.Fprintln(w, line)
	}
}

// writeRoles prints the roles the model sees, one agent per line.
func writeRoles(w io.Writer, roles map[string]string) {
	if len(roles) == 0 {
		fmt.Fprintln(w, "Roles: none declared yet (agents set their own with set_role)")
		return
	}
	fmt.Fprintln(w, "Roles (as the model sees them):")
	for _, agent := range slices.Sorted(maps.Keys(roles)) {
		fmt.Fprintf(w, "  %s: %s\n", printable(agent), printable(roles[agent]))
	}
}

// printable shows broker-supplied text without control characters: each
// non-printable rune becomes its \u escape, so nothing drives the terminal.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		} else {
			fmt.Fprintf(&b, "\\u%04x", r)
		}
	}
	return b.String()
}

// counts64 renders int64 counters like counts.
func counts64(m map[string]int64) string {
	as := make(map[string]int, len(m))
	for k, v := range m {
		as[k] = int(v)
	}
	return counts(as)
}

func truncatedNote(truncated bool) string {
	if truncated {
		return " (truncated)"
	}
	return ""
}

// counts renders a map as "key N, key N", keys sorted.
func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", printable(k), m[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
