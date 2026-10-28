package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorReviewUsage = `usage: pairtask operator review request --reviewer AGENT [--scope TEXT] [--no-publish] [--no-formal] [--message-id ID] TARGET
       pairtask operator review (list | status REVIEW_ID | cancel REVIEW_ID [--reason TEXT])
  TARGET is a pull request URL (https://github.com/OWNER/REPO/pull/N) or OWNER/REPO#N,
  or --forge NAME --repository HOST/PATH --change REF for another forge.
  Common flags: [--socket PATH] [--json] [--timeout 5s]`

// reviewVerbs are the review operations and their positional arguments.
var reviewVerbs = map[string]struct {
	op   string
	args int
}{
	"request": {protocol.ReviewOpRequest, -1},
	"list":    {protocol.ReviewOpList, 0},
	"status":  {protocol.ReviewOpStatus, 1},
	"cancel":  {protocol.ReviewOpCancel, 1},
}

// reviewFlags are request's and cancel's own flags.
type reviewFlags struct {
	reviewer, scope, reason   string
	forge, repository, change string
	noPublish, noFormal       bool
	messageID                 string
}

// runOperatorReview asks an agent to review a change (#201), or reads or
// cancels a review request. The broker records it; the reviewer's side
// pins the revisions and publishes.
func runOperatorReview(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseOperatorReview(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorReviewResult, "operator review"}, stderr)
	if err != nil {
		return 1
	}
	if resp.Review == nil && resp.ReviewPage == nil {
		fmt.Fprintln(stderr, "operator review: the broker answered without a review")
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeReviewResult(stdout, resp)
	return 0
}

func parseOperatorReview(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorReviewUsage)
		return call, protocol.Message{}, 2
	}
	verb, ok := reviewVerbs[args[0]]
	if !ok {
		fmt.Fprintln(stderr, operatorReviewUsage)
		return call, protocol.Message{}, 2
	}
	req := protocol.Message{Type: protocol.TypeOperatorReview, MessageID: uuid.NewString(), ReviewOp: verb.op}
	var rf reviewFlags
	flags := flag.NewFlagSet("operator review", flag.ContinueOnError)
	call.register(flags)
	rf.register(flags, verb.op)
	code := parseFlags(flags, args[1:], operatorReviewUsage, stderr, func(rest []string) error {
		if err := fillReview(&req, rf, rest, verb.args); err != nil {
			return err
		}
		return call.validate()
	})
	return call, req, code
}

func (rf *reviewFlags) register(flags *flag.FlagSet, op string) {
	switch op {
	case protocol.ReviewOpRequest:
		flags.StringVar(&rf.reviewer, "reviewer", "", "the agent to review it")
		flags.StringVar(&rf.scope, "scope", "", "what to focus on, at most 1 KiB")
		flags.BoolVar(&rf.noPublish, "no-publish", false, "keep the verdict in chat; publish nothing to the forge")
		flags.BoolVar(&rf.noFormal, "no-formal", false, "never publish a formal approval or change request")
		flags.StringVar(&rf.forge, "forge", "", "with --repository and --change: the forge, for one without a URL form")
		flags.StringVar(&rf.repository, "repository", "", "HOST/PATH of the repository")
		flags.StringVar(&rf.change, "change", "", "the forge's change reference")
		flags.StringVar(&rf.messageID, "message-id", "", "the request's durable ID; repeat it to retry after a lost answer (default: a new one)")
	case protocol.ReviewOpCancel:
		flags.StringVar(&rf.reason, "reason", "", "why, for the record")
	}
}

// fillReview completes the request from the flags and positional arguments.
func fillReview(req *protocol.Message, rf reviewFlags, rest []string, want int) error {
	if req.ReviewOp == protocol.ReviewOpRequest {
		if rf.messageID != "" {
			req.MessageID = rf.messageID
		}
		spec, err := reviewSpec(rf, rest)
		req.ReviewSpec = &spec
		return errors.Join(err, protocol.ValidateReviewID(req.MessageID))
	}
	if len(rest) != want {
		return fmt.Errorf("review %s takes %d arguments", req.ReviewOp, want)
	}
	if want == 1 {
		req.ReviewID, req.ReviewReason = rest[0], rf.reason
		return protocol.ValidateReviewID(req.ReviewID)
	}
	return nil
}

func reviewSpec(rf reviewFlags, rest []string) (protocol.ReviewSpec, error) {
	target, err := reviewTarget(rf, rest)
	if err != nil {
		return protocol.ReviewSpec{}, err
	}
	if rf.reviewer == "" {
		return protocol.ReviewSpec{}, errors.New("review request needs --reviewer AGENT")
	}
	spec := protocol.ReviewSpec{ReviewTarget: target, Reviewer: rf.reviewer, Scope: rf.scope}
	if rf.noPublish || rf.noFormal {
		spec.Policy = &protocol.ReviewPolicy{Publish: !rf.noPublish, Formal: !rf.noFormal && !rf.noPublish}
	}
	return spec, protocol.ValidateReviewSpec(spec)
}

var shortTarget = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([0-9]+)$`)

// reviewTarget reads TARGET, or --forge, --repository and --change.
func reviewTarget(rf reviewFlags, rest []string) (protocol.ReviewTarget, error) {
	if rf.forge+rf.repository+rf.change != "" {
		if len(rest) != 0 {
			return protocol.ReviewTarget{}, errors.New("give TARGET or --forge, --repository and --change, not both")
		}
		return protocol.ReviewTarget{Forge: rf.forge, Repository: rf.repository, Change: rf.change}, nil
	}
	if len(rest) != 1 {
		return protocol.ReviewTarget{}, errors.New("review request takes one TARGET")
	}
	if m := shortTarget.FindStringSubmatch(rest[0]); m != nil {
		return protocol.ReviewTarget{Forge: "github", Repository: "github.com/" + m[1] + "/" + m[2], Change: m[3]}, nil
	}
	return pullRequestURL(rest[0])
}

// pullRequestURL reads https://github.com/OWNER/REPO/pull/N.
func pullRequestURL(raw string) (protocol.ReviewTarget, error) {
	parts, ok := githubPullPath(raw)
	if !ok {
		return protocol.ReviewTarget{}, fmt.Errorf("TARGET %q is neither a GitHub pull request URL nor OWNER/REPO#N", raw)
	}
	return protocol.ReviewTarget{Forge: "github", Repository: "github.com/" + parts[0] + "/" + parts[1], Change: parts[3]}, nil
}

// githubPullPath is OWNER, REPO, "pull" and N from a pull request URL.
func githubPullPath(raw string) ([]string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme+"://"+u.Host != "https://github.com" {
		return nil, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 {
		return nil, false
	}
	return parts, parts[2] == "pull"
}

// writeReviewResult prints one review or a list, every peer-chosen string
// quoted.
func writeReviewResult(w io.Writer, resp protocol.Message) {
	if resp.Review != nil {
		writeReview(w, *resp.Review)
		writeReviewDetail(w, *resp.Review)
		return
	}
	p := resp.ReviewPage
	fmt.Fprintf(w, "Reviews (%d):\n", p.Total)
	for _, r := range p.Reviews {
		writeReview(w, r)
	}
	if p.Truncated {
		fmt.Fprintf(w, "(list is partial: %d more not shown)\n", p.Total-len(p.Reviews))
	}
}

func writeReview(w io.Writer, r protocol.ReviewView) {
	fmt.Fprintf(w, "%q  %s#%s  reviewer %q  %s", r.ID, r.Spec.Repository, r.Spec.Change, r.Spec.Reviewer, r.State)
	if r.Revisions != nil {
		fmt.Fprintf(w, "  at %s", short(r.Revisions.Head))
	}
	if r.Verdict != nil {
		fmt.Fprintf(w, "  verdict %s", r.Verdict.Outcome)
	}
	if r.Stale != nil {
		fmt.Fprint(w, "  (stale: the change moved)")
	}
	fmt.Fprintf(w, "  publication %s", r.Publication.State)
	if r.Publication.Receipt != nil && r.Publication.Receipt.URL != "" {
		fmt.Fprintf(w, " %q", r.Publication.Receipt.URL)
	}
	fmt.Fprintln(w)
}

// writeReviewDetail prints what one review holds: the exact pins, the
// verdict with its findings, and the publication's reason and receipt.
func writeReviewDetail(w io.Writer, r protocol.ReviewView) {
	writePins(w, r)
	if v := r.Verdict; v != nil {
		writeVerdict(w, *v, r.Attempt)
	}
	writePublication(w, r.Publication)
}

func writePins(w io.Writer, r protocol.ReviewView) {
	if r.Revisions != nil {
		fmt.Fprintf(w, "  pinned base %s head %s\n", r.Revisions.Base, r.Revisions.Head)
	}
	if r.Stale != nil {
		fmt.Fprintf(w, "  stale: now base %s head %s\n", r.Stale.Base, r.Stale.Head)
	}
	if r.Reason != "" {
		fmt.Fprintf(w, "  reason %q\n", r.Reason)
	}
}

func writeVerdict(w io.Writer, v protocol.Verdict, attempt int) {
	fmt.Fprintf(w, "  verdict %s (attempt %d): %q\n", v.Outcome, attempt, v.Summary)
	for _, f := range v.Findings {
		fmt.Fprintf(w, "    %q:%d %q\n", f.Path, f.Line, f.Summary)
	}
}

func writePublication(w io.Writer, p protocol.Publication) {
	fmt.Fprintf(w, "  publication %s", p.State)
	if p.Reason != "" {
		fmt.Fprintf(w, " (%s)", p.Reason)
	}
	if rc := p.Receipt; rc != nil {
		fmt.Fprintf(w, " as %q at %s, review %q recorded as %s", rc.Identity, short(rc.Revision), rc.ReviewID, recordedEvent(*rc))
	}
	fmt.Fprintln(w)
}

// recordedEvent is what the forge recorded the review as.
func recordedEvent(rc protocol.PublicationReceipt) string {
	if rc.Event != "" {
		return rc.Event
	}
	if rc.Formal {
		return "a formal review"
	}
	return "a comment"
}

func short(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
