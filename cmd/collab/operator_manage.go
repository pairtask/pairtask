package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

const (
	operatorInboxUsage = "usage: pairtask operator inbox status [--socket PATH] [--json] [--timeout 5s]\n       pairtask operator inbox ack [--socket PATH] [--from AGENT] [--after SEQ] [--upper SEQ] [--limit N] [--all] [--json] [--timeout 5s]\n" +
		"       pairtask operator inbox export PATH --database DB [--json] (offline: stop the broker first)\n" +
		"       pairtask operator inbox archive PATH --database DB (--confirm | --dry-run) [--upper SEQ] [--json] (offline: stop the broker first)"
	operatorInboxStatusUsage = "usage: pairtask operator inbox status [--socket PATH] [--json] [--timeout 5s]"
	operatorInboxAckUsage    = "usage: pairtask operator inbox ack [--socket PATH] [--from AGENT] [--after SEQ] [--upper SEQ] [--limit N] [--all] [--json] [--timeout 5s]"
)

// runOperatorInbox manages the operator inbox: usage against its bounds, and
// bulk acknowledgment over a stable, explicitly bounded selection.
func runOperatorInbox(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorInboxUsage)
		return 2
	}
	switch args[0] {
	case "status":
		return runInboxStatus(ctx, args[1:], stdout, stderr)
	case "ack":
		return runInboxAck(ctx, args[1:], stdout, stderr)
	case "export":
		return runInboxExport(ctx, args[1:], stdout, stderr)
	case "archive":
		return runInboxArchive(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, operatorInboxUsage)
	return 2
}

func runInboxStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var call operatorCall
	flags := flag.NewFlagSet("operator inbox status", flag.ContinueOnError)
	call.register(flags)
	if code := parseFlags(flags, args, operatorInboxStatusUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("inbox status takes no arguments")
		}
		return call.validate()
	}); code != -1 {
		return code
	}
	status, err := call.inboxStatus(ctx, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &status})
	}
	writeInboxStatus(stdout, status)
	return 0
}

// inboxStatus reads usage; the broker audits the read and changes nothing.
func (c operatorCall) inboxStatus(ctx context.Context, stderr io.Writer) (protocol.InboxStatus, error) {
	resp, err := c.expect(ctx, protocol.Message{Type: protocol.TypeOperatorInboxStatus}, expectation{protocol.TypeOperatorInboxStatus, "operator inbox status"}, stderr)
	if err != nil {
		return protocol.InboxStatus{}, err
	}
	if resp.Inbox == nil {
		fmt.Fprintln(stderr, "operator inbox status response has no inbox; nothing read")
		return protocol.InboxStatus{}, errReported
	}
	return *resp.Inbox, nil
}

func writeInboxStatus(w io.Writer, s protocol.InboxStatus) {
	fmt.Fprintf(w, "operator inbox: %d unacknowledged (%d not yet received), %s of %s, %d/%d messages; head #%d, oldest unacknowledged #%d\n",
		s.Unacknowledged, s.Unreceived, humanBytes(s.Bytes), humanBytes(s.Limits.InboxBytes), s.Unacknowledged, s.Limits.InboxMessages, s.HeadSeq, s.OldestUnacknowledgedSeq)
	for _, u := range s.Senders {
		fmt.Fprintf(w, "  %-24q %3d/%d unacknowledged (%d not yet received), %s of %s%s\n", u.AgentID, u.Unacknowledged, s.Limits.SenderMessages, u.Unreceived, humanBytes(u.Bytes), humanBytes(s.Limits.SenderBytes), nearBound(u, s.Limits))
	}
	fmt.Fprintf(w, "retained history: %d/%d messages, %s of %s\n", s.Retained.Messages, s.Retained.LimitMessages, humanBytes(s.Retained.Bytes), humanBytes(s.Retained.LimitBytes))
}

// nearBound marks a sender at or near its bound, where new sends get refused.
func nearBound(u protocol.SenderUsage, l protocol.InboxLimits) string {
	switch {
	case u.Unacknowledged >= l.SenderMessages || u.Bytes >= l.SenderBytes:
		return "  AT BOUND: this sender's messages are refused until acknowledged"
	case u.Unacknowledged*4 >= l.SenderMessages*3 || u.Bytes*4 >= l.SenderBytes*3:
		return "  near bound"
	}
	return ""
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// inboxAck is one bulk acknowledgment run: a frozen upper bound, a start,
// and whether to continue until the selection is exhausted.
type inboxAck struct {
	call operatorCall
	sel  protocol.AckSelection
	all  bool
	set  map[string]bool
}

func runInboxAck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	run, code := parseInboxAck(args, stderr)
	if code != -1 {
		return code
	}
	if err := run.resolveBounds(ctx, stderr); err != nil {
		return 1
	}
	if run.sel.UpperSeq == 0 {
		fmt.Fprintln(stdout, "nothing to acknowledge")
		return 0
	}
	total, err := run.acknowledge(ctx, stdout, stderr)
	if err != nil {
		run.reportPartial(stderr, total)
		return 1
	}
	if !run.call.asJSON {
		fmt.Fprintf(stdout, "acknowledged %d (received, not approval); %d already acknowledged; %d not yet received and left pending\n", total.Acknowledged, total.AlreadyAcknowledged, total.Skipped)
	}
	return 0
}

func parseInboxAck(args []string, stderr io.Writer) (inboxAck, int) {
	var run inboxAck
	flags := flag.NewFlagSet("operator inbox ack", flag.ContinueOnError)
	run.call.register(flags)
	flags.StringVar(&run.sel.From, "from", "", "acknowledge messages from this agent only")
	flags.Uint64Var(&run.sel.AfterSeq, "after", 0, "start after this sequence; default: just before the oldest unacknowledged message")
	flags.Uint64Var(&run.sel.UpperSeq, "upper", 0, "acknowledge up to this sequence; default: the inbox head, frozen for the whole run")
	flags.IntVar(&run.sel.Limit, "limit", protocol.AckManyDefault, "rows per request, 1 to 256")
	flags.BoolVar(&run.all, "all", false, "continue with the same frozen upper bound until the selection is exhausted")
	code := parseFlags(flags, args, operatorInboxAckUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("inbox ack takes no positional arguments")
		}
		run.set = map[string]bool{}
		flags.Visit(func(f *flag.Flag) { run.set[f.Name] = true })
		if err := run.validateSelection(); err != nil {
			return err
		}
		return run.call.validate()
	})
	return run, code
}

// validateSelection rejects bounds that could never form a valid page,
// before anything is sent.
func (r inboxAck) validateSelection() error {
	if r.sel.Limit < 1 || r.sel.Limit > protocol.AckManyMax {
		return errors.New("limit must be 1 to 256")
	}
	if err := r.validateBounds(); err != nil {
		return err
	}
	if !r.set["from"] {
		return nil
	}
	if err := protocol.ValidateAgentID(r.sel.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	return nil
}

// validateBounds checks an explicit upper bound; an omitted one is frozen
// from the inbox status later.
func (r inboxAck) validateBounds() error {
	switch {
	case !r.set["upper"]:
		return nil
	case r.sel.UpperSeq == 0:
		return errors.New("upper must be a committed sequence above 0; omit it to use the inbox head")
	case r.sel.AfterSeq >= r.sel.UpperSeq:
		return errors.New("after must be below upper")
	}
	return nil
}

// resolveBounds freezes the upper bound and the start from the inbox status
// when they weren't given, so new arrivals are never swept in.
func (r *inboxAck) resolveBounds(ctx context.Context, stderr io.Writer) error {
	if r.set["upper"] && r.set["after"] {
		return nil
	}
	status, err := r.call.inboxStatus(ctx, stderr)
	if err != nil {
		return err
	}
	if !r.set["upper"] {
		r.sel.UpperSeq = status.HeadSeq
	}
	if !r.set["after"] && status.OldestUnacknowledgedSeq > 0 {
		r.sel.AfterSeq = status.OldestUnacknowledgedSeq - 1
	}
	if status.Unacknowledged == 0 {
		r.sel.UpperSeq = 0 // nothing to acknowledge; never a malformed write
	}
	return nil
}

// reportPartial says what a run confirmed before it failed, and how to
// continue it under the same frozen upper bound, so a retry is safe.
func (r *inboxAck) reportPartial(stderr io.Writer, total protocol.AckManyResult) {
	if total.Acknowledged+total.AlreadyAcknowledged+total.Skipped == 0 {
		// The request may have committed before its response was lost; the
		// retry re-examines the same rows and reports them as already done.
		fmt.Fprintf(stderr, "no acknowledgments confirmed; retry with %s\n", r.resume(r.sel.AfterSeq))
		return
	}
	fmt.Fprintf(stderr, "partial: acknowledged %d, %d already acknowledged, %d not yet received, confirmed through #%d under upper #%d; continue with %s\n",
		total.Acknowledged, total.AlreadyAcknowledged, total.Skipped, total.NextAfterSeq, r.sel.UpperSeq, r.resume(total.NextAfterSeq))
}

// resume is the command tail that continues this run with its exact scope:
// the frozen upper bound and the sender filter, never widened. The sender
// is shell-quoted so pasting the hint can't expand it; an ID with control
// characters is never advertised as pasteable.
func (r *inboxAck) resume(after uint64) string {
	tail := fmt.Sprintf("--after %d --upper %d", after, r.sel.UpperSeq)
	switch {
	case r.sel.From == "":
	case hasControl(r.sel.From):
		tail += " and the same --from value passed literally (it contains control characters)"
	default:
		tail += " --from " + shellQuote(r.sel.From)
	}
	return tail
}

// shellQuote single-quotes s for a POSIX shell; nothing inside expands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// acknowledge runs one request, or continues under the frozen upper bound
// until the selection is exhausted.
func (r *inboxAck) acknowledge(ctx context.Context, stdout, stderr io.Writer) (protocol.AckManyResult, error) {
	var total protocol.AckManyResult
	for {
		page, err := r.page(ctx, stdout, stderr)
		if err != nil {
			return total, err
		}
		total = addPage(total, page)
		if !r.all || !page.More {
			return total, nil
		}
		r.sel.AfterSeq = page.NextAfterSeq
	}
}

// page runs one request and returns its verified result.
func (r *inboxAck) page(ctx context.Context, stdout, stderr io.Writer) (protocol.AckManyResult, error) {
	resp, err := r.call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorAckMany, AckSelection: &r.sel}, expectation{protocol.TypeAck, "operator inbox ack"}, stderr)
	if err != nil {
		return protocol.AckManyResult{}, err
	}
	if err := protocol.ValidateAckMany(r.sel, resp); err != nil {
		fmt.Fprintln(stderr, "operator inbox ack returned an invalid result; acknowledgment not verified:", err)
		return protocol.AckManyResult{}, errReported
	}
	if r.call.asJSON && encodeJSON(stdout, stderr, resp) != 0 {
		return protocol.AckManyResult{}, errReported
	}
	return *resp.AckMany, nil
}

// addPage accumulates a page into the run's totals; the cursor is the page's.
func addPage(total, page protocol.AckManyResult) protocol.AckManyResult {
	total.Acknowledged += page.Acknowledged
	total.AlreadyAcknowledged += page.AlreadyAcknowledged
	total.Skipped += page.Skipped
	total.NextAfterSeq, total.More = page.NextAfterSeq, page.More
	return total
}
