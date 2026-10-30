package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorContactUsage = "usage: pairtask operator contact send KEY TEXT [--message-id ID] | list KEY | sync KEY [--socket PATH] [--json] [--timeout 5s]"

// contactArgs is how many positional arguments each contact operation
// takes: the contact's full key, and the text for send.
var contactArgs = map[string]int{
	protocol.ContactOpSend: 2,
	protocol.ContactOpList: 1,
	protocol.ContactOpSync: 1,
}

// runOperatorContact sends to, lists or syncs a paired workspace's messages
// from the operator socket, as the chat's ⇄ tab does (#210).
func runOperatorContact(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseOperatorContact(args, stderr)
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
	leading, rest := splitLeading(args[1:])
	code := parseFlags(flags, rest, operatorContactUsage, stderr, func(more []string) error {
		positional := append(leading, more...)
		if len(positional) != contactArgs[op] {
			return fmt.Errorf("contact %s: wrong number of arguments", op)
		}
		req.ContactKey = positional[0]
		if op == protocol.ContactOpSend {
			req.ContactText = positional[1]
			if *messageID != "" {
				req.MessageID = *messageID
			}
		}
		return call.validate()
	})
	return call, req, code
}

// writeContact prints what an operation answered: the sent message, the
// page newest first, or the sync request. Everything the other workspace
// sent is quoted, so its control bytes stay inert, as in operator history.
func writeContact(w io.Writer, resp protocol.Message) {
	if m := resp.ContactMessage; m != nil {
		fmt.Fprintf(w, "id=%q %s: %s\n", m.ID, m.Direction, m.State)
	}
	if p := resp.ContactPage; p != nil {
		for _, m := range p.Messages {
			fmt.Fprintf(w, "%s %s %s id=%q %q\n", m.CreatedAt.Local().Format("15:04"), m.Direction, m.State, m.ID, m.Text)
		}
	}
	if s := resp.ContactSync; s != nil {
		fmt.Fprintf(w, "sync requested; live: %t\n", s.Live)
	}
}
