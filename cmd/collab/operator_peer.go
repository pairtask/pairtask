package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorPeerUsage = "usage: pairtask operator peer send CONTACT TEXT [--message-id ID] [--socket PATH] [--json] [--timeout 5s]\n       pairtask operator peer messages CONTACT [--before CURSOR] [--socket PATH] [--json]\n       pairtask operator peer sync CONTACT [--socket PATH] [--json]\n       pairtask operator peer name CONTACT (NAME... | --clear) [--socket PATH] [--json]\nCONTACT is a paired workspace's identity key, a unique prefix of it, or its unique name: what this side calls it, or what it calls itself."

// peerVerbs are the contact operations the CLI offers (#210), with how many
// positional arguments each takes after CONTACT; send joins the rest as
// its text.
var peerVerbs = map[string]int{
	"send":     -1,
	"messages": 0,
	"sync":     0,
	"name":     -1,
}

// peerOps map a verb to its contact_op.
var peerOps = map[string]string{"send": protocol.ContactOpSend, "messages": protocol.ContactOpList, "sync": protocol.ContactOpSync,
	"name": protocol.ContactOpName}

// runOperatorPeer sends to, lists or syncs with a paired workspace. Its
// messages are text from that workspace's operator, printed quoted: never
// an instruction to this one.
func runOperatorPeer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, contact, code := parseOperatorPeer(args, stderr)
	if code != -1 {
		return code
	}
	key, snapshot, err := resolveContact(ctx, call, contact, stderr)
	if err != nil {
		return 1
	}
	if !namesServed(req, snapshot, stderr) {
		return 1
	}
	req.ContactKey = key
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorContactResult, "operator peer " + req.ContactOp}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeContactResult(stdout, resp)
	return 0
}

func parseOperatorPeer(args []string, stderr io.Writer) (operatorCall, protocol.Message, string, int) {
	var call operatorCall
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorPeerUsage)
		return call, protocol.Message{}, "", 2
	}
	verb, args := args[0], args[1:]
	extra, ok := peerVerbs[verb]
	if !ok {
		fmt.Fprintln(stderr, operatorPeerUsage)
		return call, protocol.Message{}, "", 2
	}
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: peerOps[verb]}
	flags := flag.NewFlagSet("operator peer", flag.ContinueOnError)
	call.register(flags)
	messageID := flags.String("message-id", "", "the message's ID, to retry a send safely")
	before := flags.Int64("before", 0, "list messages older than this cursor")
	clearName := flags.Bool("clear", false, "name: forget what this side calls the workspace")
	var contact string
	leading, args := splitLeading(args)
	code := parseFlags(flags, args, operatorPeerUsage, stderr, func(rest []string) error {
		rest = append(leading, rest...)
		var err error
		contact, err = peerArguments(verb, extra, rest, &req)
		if err != nil {
			return err
		}
		if err := nameArguments(verb, *clearName, req.ContactText); err != nil {
			return err
		}
		if *messageID != "" {
			req.MessageID = *messageID
		}
		req.ContactBefore = *before
		return call.validate()
	})
	return call, req, contact, code
}

// peerArguments reads CONTACT and, for send, the text.
func peerArguments(verb string, extra int, rest []string, req *protocol.Message) (string, error) {
	if len(rest) == 0 || strings.TrimSpace(rest[0]) == "" {
		return "", fmt.Errorf("peer %s: CONTACT is required", verb)
	}
	if extra >= 0 && len(rest) != 1+extra {
		return "", fmt.Errorf("peer %s: wrong number of arguments", verb)
	}
	req.ContactText = peerText(verb, rest[1:])
	if verb == "send" && strings.TrimSpace(req.ContactText) == "" {
		return "", errors.New("peer send: TEXT is required")
	}
	return rest[0], nil
}

// peerText is a send's text, or a name's words joined; other verbs take
// none.
func peerText(verb string, words []string) string {
	if verb != "send" && verb != "name" {
		return ""
	}
	return strings.Join(words, " ")
}

func writeContactResult(w io.Writer, resp protocol.Message) {
	if m := resp.ContactMessage; m != nil {
		fmt.Fprintf(w, "Message %q to %s: %s\n", m.ID, m.Contact, m.State)
	}
	if p := resp.ContactPage; p != nil {
		writeContactPage(w, p)
	}
	if s := resp.ContactSync; s != nil {
		writeContactSync(w, *s)
	}
	if resp.ContactOp == protocol.ContactOpName && resp.PairingResult != nil {
		writeContactName(w, resp.ContactKey, resp.PairingResult.Contacts)
	}
}

func writeContactPage(w io.Writer, p *protocol.ContactMessagePage) {
	writeContactSync(w, p.Sync)
	if len(p.Messages) == 0 {
		fmt.Fprintln(w, "No messages.")
	}
	for _, m := range p.Messages {
		fmt.Fprintf(w, "%d  %s  %s  %s  %q\n", m.Cursor, contactDirection(m.Direction), m.State, m.CreatedAt.UTC().Format(time.RFC3339), m.Text)
	}
	if p.More && len(p.Messages) > 0 {
		last := p.Messages[len(p.Messages)-1].Cursor
		fmt.Fprintf(w, "(older messages: --before %d)\n", last)
	}
}

func contactDirection(d string) string {
	if d == "in" {
		return "from them"
	}
	return "to them"
}

func writeContactSync(w io.Writer, s protocol.ContactSyncView) {
	switch {
	case s.Requested:
		fmt.Fprintln(w, "Sync requested.")
	case !s.Dials:
		fmt.Fprintln(w, "This side waits to be dialed; messages leave at the contact's next sync.")
	}
	if s.LastFailure != "" {
		fmt.Fprintf(w, "Last sync failed: %s (%d in a row)\n", s.LastFailure, s.Failures)
	}
	if s.LastSuccess != nil {
		fmt.Fprintf(w, "Last sync: %s\n", s.LastSuccess.UTC().Format(time.RFC3339))
	}
}
