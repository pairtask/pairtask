package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// Review requests with a paired workspace (#235), from the operator socket:
// what the contact may ask of agents here, a request for one of its
// agents, and the decision on one of its requests.

const contactReviewsUsage = "usage: pairtask operator contact reviews KEY AGENT... --mode ask|auto|off [--publish] [--formal]" +
	" | review KEY AGENT TARGET --base SHA --head SHA [--scope TEXT] [--note TEXT] [--publish] [--formal] [--message-id ID]" +
	" | decide KEY ID approve|decline [--socket PATH] [--json] [--timeout 5s]"

// contactReviewOps are the review words of contact and their operations.
var contactReviewOps = map[string]string{
	"reviews": protocol.ContactOpReviewGrant,
	"review":  protocol.ContactOpReviewAsk,
	"decide":  protocol.ContactOpReviewDecide,
}

// contactReviewFlags are the flags of the review words.
type contactReviewFlags struct {
	mode, base, head, scope, note, messageID string
	publish, formal                          bool
}

func (f *contactReviewFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&f.mode, "mode", "", "reviews: ask, auto or off")
	flags.StringVar(&f.base, "base", "", "review: the base commit the review is pinned to")
	flags.StringVar(&f.head, "head", "", "review: the head commit the review is pinned to")
	flags.StringVar(&f.scope, "scope", "", "review: what to focus on")
	flags.StringVar(&f.note, "note", "", "review: a note with the request")
	flags.StringVar(&f.messageID, "message-id", "", "a stable ID, so a retry asks once")
	flags.BoolVar(&f.publish, "publish", false, "the verdict may be published, or is asked to be")
	flags.BoolVar(&f.formal, "formal", false, "with --publish: as a formal approval or change request")
}

// parseContactReview reads one review word into its request.
func parseContactReview(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	op := contactReviewOps[args[0]]
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: op}
	flags := flag.NewFlagSet("operator contact "+args[0], flag.ContinueOnError)
	call.register(flags)
	var f contactReviewFlags
	f.register(flags)
	leading, rest := splitLeading(args[1:])
	code := parseFlags(flags, rest, contactReviewsUsage, stderr, func(more []string) error {
		if err := contactReviewFields(&req, append(leading, more...), f); err != nil {
			return err
		}
		return call.validate()
	})
	return call, req, code
}

// contactReviewFields fills the request from its arguments and flags.
func contactReviewFields(req *protocol.Message, positional []string, f contactReviewFlags) error {
	if len(positional) < 2 {
		return errors.New("contact " + req.ContactOp + ": wrong number of arguments")
	}
	req.ContactKey = positional[0]
	if f.messageID != "" {
		req.MessageID = f.messageID
	}
	switch req.ContactOp {
	case protocol.ContactOpReviewGrant:
		req.ContactAgents = positional[1:]
		req.ContactReviewGrant = &protocol.ContactReviewGrant{Mode: f.mode, Publish: f.publish, Formal: f.formal}
		return nil
	case protocol.ContactOpReviewAsk:
		return reviewAskFields(req, positional, f)
	}
	return decideFields(req, positional)
}

func reviewAskFields(req *protocol.Message, positional []string, f contactReviewFlags) error {
	if len(positional) != 3 {
		return errors.New("contact review takes KEY AGENT TARGET")
	}
	target, err := review.ParseTarget(positional[2])
	if err != nil {
		return err
	}
	req.ContactTo, req.ContactText = positional[1:2], f.note
	req.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: target, Scope: f.scope, Base: f.base, Head: f.head, Publish: f.publish, Formal: f.formal && f.publish}
	return nil
}

func decideFields(req *protocol.Message, positional []string) error {
	if len(positional) != 3 || positional[2] != "approve" && positional[2] != "decline" {
		return errors.New("contact decide takes KEY ID approve|decline")
	}
	approve := positional[2] == "approve"
	req.ContactRead, req.ContactApprove = positional[1:2], &approve
	return nil
}

// grantNote is a contact's review grants, for the page.
func grantNote(grants []protocol.ReviewGrantView) string {
	note := ""
	for _, g := range grants {
		note += fmt.Sprintf(" %q:%s", g.Agent, g.Mode)
		if g.Formal {
			note += "+formal"
		} else if g.Publish {
			note += "+publish"
		}
	}
	return note
}
