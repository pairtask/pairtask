package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorContactUsage = "usage: pairtask operator contact send KEY TEXT [--to AGENT]... [--message-id ID] | list KEY [--history] | sync KEY | share KEY AGENT...|* | unshare KEY AGENT...|* | reviews | review | decide | discovery | candidates HOST:PORT... | address CONTACT HOST:PORT [--socket PATH] [--json] [--timeout 5s]; reviews: pairtask operator contact reviews --help"

// contactArgs is how many positional arguments each contact operation
// takes: the contact's full key, and the text for send; share and unshare
// take at least one agent.
var contactArgs = map[string]int{
	protocol.ContactOpSend:    2,
	protocol.ContactOpList:    1,
	protocol.ContactOpSync:    1,
	protocol.ContactOpShare:   2,
	protocol.ContactOpUnshare: 2,
}

// contactAgentOps take their agents as the arguments after the key.
var contactAgentOps = map[string]bool{protocol.ContactOpShare: true, protocol.ContactOpUnshare: true}

// agentList is a repeatable flag: each use adds one agent.
type agentList []string

func (l *agentList) String() string     { return strings.Join(*l, ",") }
func (l *agentList) Set(v string) error { *l = append(*l, v); return nil }

// runOperatorContact sends to, lists or syncs a paired workspace's messages
// from the operator socket, as the chat's ⇄ tab does (#210).
func runOperatorContact(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	parse := parseOperatorContact
	switch {
	case len(args) == 0:
	case contactReviewOps[args[0]] != "":
		parse = parseContactReview
	case contactMobilityOps[args[0]]:
		parse = parseContactMobility
	case args[0] == protocol.ContactOpAddress:
		return runContactAddress(ctx, args[1:], stdout, stderr)
	}
	call, req, code := parse(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorContactResult, "operator contact " + req.ContactOp}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeContact(stdout, resp)
	writeMobility(stdout, resp)
	return 0
}

func parseOperatorContact(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	if len(args) == 0 || contactArgs[args[0]] == 0 {
		fmt.Fprintln(stderr, operatorContactUsage)
		return call, protocol.Message{}, 2
	}
	op := args[0]
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: op}
	flags := flag.NewFlagSet("operator contact", flag.ContinueOnError)
	call.register(flags)
	messageID := flags.String("message-id", "", "a stable ID, so a retry sends once")
	var history bool
	if op == protocol.ContactOpList {
		flags.BoolVar(&history, "history", false, "include messages from every pairing with this workspace")
	}
	var to agentList
	flags.Var(&to, "to", "an agent of the contact to send to; repeat for more (none: its operator)")
	leading, rest := splitLeading(args[1:])
	code := parseFlags(flags, rest, operatorContactUsage, stderr, func(more []string) error {
		positional := append(leading, more...)
		if !contactArity(op, len(positional)) {
			return fmt.Errorf("contact %s: wrong number of arguments", op)
		}
		req.ContactKey = positional[0]
		setContactPayload(&req, positional, to, *messageID)
		return call.validate()
	})
	if history {
		req.ProtocolVersion = protocol.Version
	}
	return call, req, code
}

// setContactPayload fills the operation-specific fields after its arguments are validated.
func setContactPayload(req *protocol.Message, positional []string, to []string, messageID string) {
	if contactAgentOps[req.ContactOp] {
		req.ContactAgents = positional[1:]
	}
	if req.ContactOp == protocol.ContactOpSend {
		req.ContactText, req.ContactTo = positional[1], to
		if messageID != "" {
			req.MessageID = messageID
		}
	}
}

// contactArity: exactly the operation's arguments, or at least them for
// share and unshare.
func contactArity(op string, n int) bool {
	if contactAgentOps[op] {
		return n >= contactArgs[op]
	}
	return n == contactArgs[op]
}

// writeContact prints what an operation answered: the sent message, the
// page newest first with what each side shares, or the sync request.
// Everything the other workspace sent is quoted, so its control bytes stay
// inert, as in operator history.
func writeContact(w io.Writer, resp protocol.Message) {
	if m := resp.ContactMessage; m != nil {
		fmt.Fprintf(w, "id=%q %s: %s\n", m.ID, m.Direction, m.State)
	}
	if p := resp.ContactPage; p != nil {
		if p.Unread != nil {
			fmt.Fprintf(w, "unread by operator: %d\n", *p.Unread)
		}
		for _, m := range p.Messages {
			fmt.Fprintf(w, "%s %s %s id=%q%s %q\n", m.CreatedAt.Local().Format("15:04"), m.Direction, stateNote(m), m.ID, routeNote(m), m.Text)
		}
		fmt.Fprintf(w, "shared here: %q; they share: %q\n", p.Shared, p.Directory)
		if len(p.ReviewGrants) > 0 {
			fmt.Fprintf(w, "they may ask for reviews from:%s\n", grantNote(p.ReviewGrants))
		}
	}
	if s := resp.ContactSync; s != nil {
		fmt.Fprintf(w, "sync requested; live: %t\n", s.Live)
	}
}

// routeNote is a message's sender and recipients, quoted, when an agent
// sent it or it is to agents.
func routeNote(m protocol.ContactMessageView) string {
	note := ""
	if m.PairingEpoch > 0 {
		note = fmt.Sprintf(" pairing=%d", m.PairingEpoch)
	}
	if m.From != "" {
		note += fmt.Sprintf(" from=%q", m.From)
	}
	if len(m.To) > 0 {
		note += fmt.Sprintf(" to=%q", m.To)
	}
	if len(m.AgentAcks) > 0 {
		note += fmt.Sprintf(" acked=%q", m.AgentAcks)
	}
	return note + reviewNote(m)
}

// reviewNote is a review request's change, pins and where it is.
func reviewNote(m protocol.ContactMessageView) string {
	a := m.ReviewAsk
	if a == nil {
		return ""
	}
	note := fmt.Sprintf(" review=%q pinned=%q..%q", a.Repository+"#"+a.Change, a.Base, a.Head)
	if m.ReviewState != "" {
		note += fmt.Sprintf(" request=%s", m.ReviewState)
	}
	if m.ReviewReason != "" {
		note += fmt.Sprintf(" reason=%q", m.ReviewReason)
	}
	return note + outcomeNote(m.ReviewOutcome)
}

// outcomeNote is where a review request this side sent stands, quoted, as
// the other workspace gave it.
func outcomeNote(o *protocol.ReviewOutcomeView) string {
	if o == nil {
		return ""
	}
	note := " outcome=" + o.State
	if o.Verdict != "" {
		note += fmt.Sprintf(" verdict=%q", o.Verdict)
	}
	if o.URL != "" {
		note += fmt.Sprintf(" url=%q", o.URL)
	}
	if o.Reason != "" {
		note += fmt.Sprintf(" why=%q", o.Reason)
	}
	return note
}

// stateNote is a message's state, with why when one is given.
func stateNote(m protocol.ContactMessageView) string {
	if m.RefusalReason == "" {
		return m.State
	}
	return fmt.Sprintf("%s(%s)", m.State, m.RefusalReason)
}
