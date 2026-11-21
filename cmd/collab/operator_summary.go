package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

const summaryUsage = "usage: pairtask operator inbox summary [--limit 1..50] [--json] [--socket PATH]"

type summaryMessage struct {
	Seq  uint64     `json:"seq"`
	From string     `json:"from"`
	Text string     `json:"text"`
	TS   *time.Time `json:"ts"`
}

type inboxSummary struct {
	Unacknowledged int              `json:"unacknowledged"`
	SenderCount    int              `json:"sender_count"`
	Messages       []summaryMessage `json:"messages"`
}

func runInboxSummary(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, limit, code := parseSummary(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, call.timeout)
	defer cancel()
	result, err := readSummary(ctx, call, limit, stderr)
	if err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(stderr, "operator inbox summary:", err)
		}
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, result)
	}
	fmt.Fprintf(stdout, "%d unacknowledged from %d agents\n", result.Unacknowledged, result.SenderCount)
	for _, m := range result.Messages {
		fmt.Fprintf(stdout, "#%d %s %s %s\n", m.Seq, preview(m.From, 128), messageAge(m.TS), m.Text)
	}
	return 0
}

func parseSummary(args []string, stderr io.Writer) (operatorCall, int, int) {
	var call operatorCall
	var limit int
	flags := flag.NewFlagSet("operator inbox summary", flag.ContinueOnError)
	call.register(flags)
	flags.IntVar(&limit, "limit", 10, "newest messages to display, 1 to 50")
	code := parseFlags(flags, args, summaryUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("summary takes no positional arguments")
		}
		if limit < 1 || limit > 50 {
			return errors.New("summary takes no positional arguments; limit must be 1 to 50")
		}
		return call.validate()
	})
	return call, limit, code
}

// Read the bounded unacknowledged inbox without sending operator_received or ack.
// Freeze the first page's upper bound so new arrivals don't prolong the scan.
func readSummary(ctx context.Context, call operatorCall, limit int, stderr io.Writer) (inboxSummary, error) {
	result := inboxSummary{Messages: []summaryMessage{}}
	senders := map[string]bool{}
	page := protocol.PageRequest{Limit: protocol.OperatorPageMax}
	for {
		resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorReceive, PageRequest: &page}, expectation{protocol.TypeOperatorPage, "operator inbox summary"}, stderr)
		if err != nil {
			return result, err
		}
		if err := validatePage(page, resp.Page); err != nil {
			return result, err
		}
		for _, m := range resp.Page.Messages {
			result.Unacknowledged++
			senders[m.From] = true
			result.Messages = append(result.Messages, summarizeMessage(m))
		}
		if len(result.Messages) > limit {
			result.Messages = result.Messages[len(result.Messages)-limit:]
		}
		if !resp.Page.More {
			break
		}
		page.AfterSeq, page.UpperSeq = resp.Page.NextAfterSeq, resp.Page.UpperSeq
	}
	result.SenderCount = len(senders)
	slices.Reverse(result.Messages)
	return result, nil
}

func summarizeMessage(m protocol.Message) summaryMessage {
	var body struct {
		Text *string `json:"text"`
	}
	text := "[no text]"
	if json.Unmarshal(m.Payload, &body) == nil && body.Text != nil {
		text = *body.Text
	}
	return summaryMessage{Seq: m.Seq, From: m.From, Text: preview(text, 100), TS: m.TS}
}

// Keep one line, discard terminal controls and format controls, and bound runes.
func preview(text string, limit int) string {
	text, _, _ = strings.Cut(text, "\n")
	clean := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text))
	if len(clean) > limit {
		return string(clean[:limit]) + "…"
	}
	return string(clean)
}

func messageAge(at *time.Time) string {
	if at == nil {
		return "unknown-age"
	}
	age := max(time.Duration(0), time.Since(*at))
	if age < time.Minute {
		return fmt.Sprintf("%ds ago", int(age.Seconds()))
	}
	if age < time.Hour {
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(age.Hours()/24))
}
