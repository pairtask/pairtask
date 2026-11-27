package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

const (
	operatorReceiveUsage = "usage: pairtask operator receive [--socket PATH] [--after SEQ] [--upper SEQ] [--limit N] [--json] [--timeout 5s]"
	operatorAckUsage     = "usage: pairtask operator ack [--socket PATH] [--json] [--timeout 5s] MESSAGE_ID"
	legacyHint           = "if the broker predates protocol_version 5, upgrade the sandbox"
)

// errReported marks a failure already written to stderr.
var errReported = errors.New("reported")

// operatorCall holds options shared by one-shot operator requests.
type operatorCall struct {
	socket  string
	asJSON  bool
	timeout time.Duration
}

func (c *operatorCall) register(flags *flag.FlagSet) {
	flags.StringVar(&c.socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.BoolVar(&c.asJSON, "json", false, "emit the broker response as JSON")
	flags.DurationVar(&c.timeout, "timeout", 5*time.Second, "timeout per request, greater than zero and at most 30s")
}

func (c operatorCall) validate() error {
	if c.socket == "" {
		return errors.New("a socket path is required")
	}
	if c.timeout <= 0 || c.timeout > 30*time.Second {
		return errors.New("timeout must be greater than zero and at most 30s")
	}
	return nil
}

func (c operatorCall) send(ctx context.Context, req protocol.Message) (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return sendOperator(ctx, c.socket, req)
}

// expectation names the response type a request must produce, and the action for errors.
type expectation struct {
	want, action string
}

// reportFailure reports a transport failure or an unexpected response type.
func reportFailure(stderr io.Writer, exp expectation, resp protocol.Message, err error) bool {
	if err != nil {
		fmt.Fprintf(stderr, "%s failed: %v\n", exp.action, err)
		return true
	}
	if resp.Type != exp.want {
		reportBrokerError(stderr, exp.action+" rejected", resp)
		return true
	}
	return false
}

// expect sends a request and reports transport failures and broker errors.
func (c operatorCall) expect(ctx context.Context, req protocol.Message, exp expectation, stderr io.Writer) (protocol.Message, error) {
	resp, err := c.send(ctx, req)
	if reportFailure(stderr, exp, resp, err) {
		return resp, errReported
	}
	return resp, nil
}

// parseFlags returns -1 to continue, or the exit code for a usage error.
func parseFlags(flags *flag.FlagSet, args []string, usage string, stderr io.Writer, validate func([]string) error) int {
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := validate(flags.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return -1
}

func runOperatorReceive(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, page, code := parseReceive(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorReceive, PageRequest: &page}, expectation{protocol.TypeOperatorPage, "operator receive"}, stderr)
	if err != nil {
		return 1
	}
	if err := validatePage(page, resp.Page); err != nil {
		fmt.Fprintln(stderr, "operator receive returned an invalid page; nothing was confirmed:", err)
		return 1
	}
	// Only a page that was fully written is confirmed as received.
	if err := writePage(stdout, resp, call.asJSON); err != nil {
		fmt.Fprintln(stderr, "operator receive output failed; nothing was confirmed:", err)
		return 1
	}
	return confirmReceived(ctx, call, resp.Page, stderr)
}

func parseReceive(args []string, stderr io.Writer) (operatorCall, protocol.PageRequest, int) {
	var call operatorCall
	var page protocol.PageRequest
	flags := flag.NewFlagSet("operator receive", flag.ContinueOnError)
	call.register(flags)
	flags.Uint64Var(&page.AfterSeq, "after", 0, "return messages after this sequence")
	flags.Uint64Var(&page.UpperSeq, "upper", 0, "upper_seq from the previous page; 0 starts a new scan")
	flags.IntVar(&page.Limit, "limit", protocol.OperatorPageDefault, "messages per page, 1 to 32")
	code := parseFlags(flags, args, operatorReceiveUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("receive takes no positional arguments")
		}
		if page.Limit < 1 || page.Limit > protocol.OperatorPageMax {
			return errors.New("limit must be 1 to 32")
		}
		return call.validate()
	})
	return call, page, code
}

// pageChecks reject a response that doesn't match the requested scan.
var pageChecks = []func(protocol.PageRequest, *protocol.OperatorPage) error{
	checkPageUpper,
	checkPageCursor,
	checkPageSize,
	checkPageMessages,
}

func validatePage(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if page == nil {
		return errors.New("missing page")
	}
	for _, check := range pageChecks {
		if err := check(req, page); err != nil {
			return err
		}
	}
	return nil
}

func checkPageUpper(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if req.UpperSeq != 0 && page.UpperSeq != req.UpperSeq {
		return errors.New("upper_seq differs from the request")
	}
	return nil
}

func checkPageCursor(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if max(req.AfterSeq, page.NextAfterSeq) > page.UpperSeq {
		return errors.New("cursor lies outside the scan")
	}
	return nil
}

func checkPageSize(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if len(page.Messages) > req.Limit {
		return errors.New("page holds more messages than requested")
	}
	if len(page.Messages) == 0 && page.More {
		return errors.New("empty page claims more pending messages")
	}
	return nil
}

// checkPageMessages requires unique operator-inbox messages in ascending
// sequence within the scan, ending at next_after_seq.
func checkPageMessages(req protocol.PageRequest, page *protocol.OperatorPage) error {
	seen := make(map[string]bool, len(page.Messages))
	last := req.AfterSeq
	for _, m := range page.Messages {
		if !isOperatorMessage(m) {
			return fmt.Errorf("sequence %d is not a message addressed to the operator or a broadcast with a message ID", m.Seq)
		}
		if seen[m.MessageID] {
			return fmt.Errorf("duplicate message ID %q at sequence %d", m.MessageID, m.Seq)
		}
		if !inScan(m.Seq, last, page.UpperSeq) {
			return errors.New("page messages are out of order or outside the scan")
		}
		seen[m.MessageID] = true
		last = m.Seq
	}
	if page.NextAfterSeq != last {
		return errors.New("next_after_seq doesn't match the last returned message")
	}
	return nil
}

func isOperatorMessage(m protocol.Message) bool {
	return m.Type == protocol.TypeMsg && (m.To == protocol.OperatorID || m.To == "*") && m.MessageID != ""
}

func inScan(seq, after, upper uint64) bool {
	return seq > after && seq <= upper
}

func writePage(w io.Writer, resp protocol.Message, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(resp)
	}
	page := resp.Page
	var b strings.Builder
	for _, m := range page.Messages {
		writeInboxMessage(&b, m)
	}
	fmt.Fprintf(&b, "next: --after %d --upper %d (more: %t)\n", page.NextAfterSeq, page.UpperSeq, page.More)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeInboxMessage quotes peer-controlled text so terminal controls stay inert.
func writeInboxMessage(b *strings.Builder, m protocol.Message) {
	fmt.Fprintf(b, "seq=%d id=%q from=%q origin=%q", m.Seq, m.MessageID, m.From, protocol.DisplayOrigin(m.Origin))
	if m.InReplyTo != "" {
		fmt.Fprintf(b, " in_reply_to=%q (correlation claim)", m.InReplyTo)
	}
	fmt.Fprintf(b, "\n  %s\n", payloadText(m.Payload))
}

func payloadText(payload json.RawMessage) string {
	var body struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Text != nil {
		return fmt.Sprintf("%q", *body.Text)
	}
	return fmt.Sprintf("%q", string(payload))
}

func confirmReceived(ctx context.Context, call operatorCall, page *protocol.OperatorPage, stderr io.Writer) int {
	if len(page.Messages) == 0 {
		return 0
	}
	ids := make([]string, len(page.Messages))
	for i, m := range page.Messages {
		ids[i] = m.MessageID
	}
	resp, err := call.send(ctx, protocol.Message{Type: protocol.TypeOperatorReceived, MessageIDs: ids})
	if err == nil && resp.Code == protocol.ErrOwnerBusy {
		fmt.Fprintln(stderr, "a live operator session owns the inbox; the messages were printed but not confirmed")
		return 1
	}
	if reportFailure(stderr, expectation{protocol.TypeAck, "receipt confirmation (the messages will be returned again)"}, resp, err) {
		return 1
	}
	if !receiptsCover(resp, ids) {
		fmt.Fprintln(stderr, "receipt confirmation incomplete; the messages will be returned again")
		return 1
	}
	return 0
}

// receiptsCover requires a received (or already acknowledged) stage for every ID.
func receiptsCover(resp protocol.Message, ids []string) bool {
	if resp.Stage != protocol.StageAdapterReceived {
		return false
	}
	stages := make(map[string]string, len(resp.Receipts))
	for _, r := range resp.Receipts {
		stages[r.MessageID] = r.Stage
	}
	for _, id := range ids {
		if !receivedStage(stages[id]) {
			return false
		}
	}
	return true
}

func receivedStage(stage string) bool {
	return stage == protocol.StageAdapterReceived || stage == protocol.StageAgentAcknowledged
}

func runOperatorAck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, id, code := parseAck(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: id}, expectation{protocol.TypeAck, "operator ack"}, stderr)
	if err != nil {
		return 1
	}
	if !confirmsAck(resp, id) {
		fmt.Fprintln(stderr, "operator ack response doesn't confirm this message; acknowledgment not verified")
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	fmt.Fprintf(stdout, "%q acknowledged by operator (received, not approval)\n", resp.MessageID)
	return 0
}

func parseAck(args []string, stderr io.Writer) (operatorCall, string, int) {
	var call operatorCall
	var id string
	flags := flag.NewFlagSet("operator ack", flag.ContinueOnError)
	call.register(flags)
	code := parseFlags(flags, args, operatorAckUsage, stderr, func(rest []string) error {
		if len(rest) != 1 || !validID(rest[0]) {
			return errors.New("exactly one MESSAGE_ID of 1 to 128 bytes is required")
		}
		id = rest[0]
		return call.validate()
	})
	return call, id, code
}

func validID(id string) bool {
	return id != "" && len(id) <= 128
}

func confirmsAck(resp protocol.Message, id string) bool {
	return resp.MessageID == id && resp.Stage == protocol.StageAgentAcknowledged
}

// reportBrokerError keeps the broker's reason visible. A malformed_frame may
// come from an older broker, so it adds a hint without asserting that.
func reportBrokerError(stderr io.Writer, prefix string, resp protocol.Message) {
	fmt.Fprintf(stderr, "%s: %q: %q\n", prefix, resp.Code, resp.Detail)
	if resp.Code == protocol.ErrMalformedFrame {
		fmt.Fprintln(stderr, legacyHint)
	}
}

func encodeJSON(stdout, stderr io.Writer, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
