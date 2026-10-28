package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

// submitReview checks the findings against the pinned diff, saves the
// verdict, then publishes it when the policy says so. Once the verdict is
// saved, a failure on the forge is reported in the result, never as a lost
// verdict.
func submitReview(ctx context.Context, c reviewClient, args submitArgs) (ReviewResult, error) {
	v, f, err := ownReview(ctx, c, args.ReviewID)
	if err != nil {
		return ReviewResult{}, err
	}
	findings := make([]protocol.Finding, 0, len(args.Findings))
	for _, a := range args.Findings {
		findings = append(findings, protocol.Finding{Path: a.Path, Line: a.Line, Summary: a.Summary})
	}
	verdict := protocol.Verdict{Outcome: args.Outcome, Summary: args.Summary, Findings: findings}
	if resubmission(v, args.Attempt, verdict) {
		// The same verdict again: answer what was saved, and carry on any
		// publication it still owes, without touching the diff.
		return finishSubmission(ctx, c, f, protocol.Message{Type: protocol.TypeReviewSubmit, ReviewID: args.ReviewID, Attempt: args.Attempt, Verdict: &verdict})
	}
	if err := runningAttempt(v, args.Attempt); err != nil {
		return ReviewResult{}, err
	}
	files, err := f.Changes(ctx, v.Spec.ReviewTarget, *v.Revisions)
	if err != nil {
		return ReviewResult{}, forgeFailure("diff", err)
	}
	if err := findingsInDiff(findings, files); err != nil {
		return ReviewResult{}, err
	}
	return finishSubmission(ctx, c, f, protocol.Message{Type: protocol.TypeReviewSubmit, ReviewID: args.ReviewID, Attempt: args.Attempt, Verdict: &verdict})
}

// finishSubmission saves the verdict (a replay changes nothing) and then
// takes the publication's next step, if it owes one.
func finishSubmission(ctx context.Context, c reviewClient, f forge.Forge, submit protocol.Message) (ReviewResult, error) {
	saved, err := reviewStep(ctx, c, submit)
	if err != nil {
		return ReviewResult{}, err
	}
	if !owesPublication(saved.Review.Publication.State) {
		return saved, nil
	}
	return publish(ctx, c, f, *saved.Review), nil
}

func owesPublication(state string) bool {
	switch state {
	case protocol.PublicationPending, protocol.PublicationFailed, protocol.PublicationSubmitting, protocol.PublicationUncertain:
		return true
	}
	return false
}

// resubmission: this attempt already completed with this same verdict.
func resubmission(v protocol.ReviewView, attempt int, verdict protocol.Verdict) bool {
	if v.State != protocol.ReviewCompleted || attempt != v.Attempt || v.Verdict == nil {
		return false
	}
	return sameVerdict(*v.Verdict, verdict)
}

func sameVerdict(a, b protocol.Verdict) bool {
	if a.Outcome != b.Outcome || a.Summary != b.Summary || len(a.Findings) != len(b.Findings) {
		return false
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			return false
		}
	}
	return true
}

// runningAttempt refuses, before any forge work, a submission for a review
// that isn't in progress at this attempt.
func runningAttempt(v protocol.ReviewView, attempt int) error {
	if v.State != protocol.ReviewInReview || v.Revisions == nil {
		return fmt.Errorf("review_conflict: the review is %s, not in progress; nothing was submitted", v.State)
	}
	if attempt != v.Attempt {
		return fmt.Errorf("review_conflict: attempt %d is not the current attempt %d; nothing was submitted", attempt, v.Attempt)
	}
	return nil
}

// findingsInDiff accepts findings on files the change modified, at changed
// lines when a line is named.
func findingsInDiff(findings []protocol.Finding, files []forge.FileChange) error {
	changed := map[string][]forge.LineRange{}
	for _, file := range files {
		changed[file.Path] = file.Lines
	}
	var wrong []string
	for i, f := range findings {
		lines, ok := changed[f.Path]
		if !ok || !onChangedLine(f.Line, lines) {
			wrong = append(wrong, fmt.Sprintf("%d (%s:%d)", i+1, f.Path, f.Line))
		}
	}
	if len(wrong) > 0 {
		return fmt.Errorf("findings must name changed files and lines of the pinned diff; not: %s; nothing was submitted", strings.Join(wrong, ", "))
	}
	return nil
}

func onChangedLine(line int, ranges []forge.LineRange) bool {
	if line == 0 {
		return true
	}
	for _, r := range ranges {
		if line >= r.From && line <= r.To {
			return true
		}
	}
	return false
}

// publishReview publishes a saved verdict, or settles a write that was
// interrupted or left uncertain.
func publishReview(ctx context.Context, c reviewClient, id string) (ReviewResult, error) {
	v, f, err := ownReview(ctx, c, id)
	if err != nil {
		return ReviewResult{}, err
	}
	return publish(ctx, c, f, v), nil
}

// publish takes the next step the publication allows: a write under a
// fresh intent, or a lookup of one in flight. It never writes twice.
func publish(ctx context.Context, c reviewClient, f forge.Forge, v protocol.ReviewView) ReviewResult {
	switch v.Publication.State {
	case protocol.PublicationPending, protocol.PublicationFailed:
		return write(ctx, c, f, v)
	case protocol.PublicationSubmitting, protocol.PublicationUncertain:
		return reconcile(ctx, c, f, v)
	}
	return ReviewResult{Review: &v, Forge: &ForgeOutcome{Class: "nothing_to_publish", Note: "publication is " + v.Publication.State}}
}

// event is what to publish the verdict as. A formal outcome the policy
// wants and the adapter can't make is refused, never sent as a comment.
func event(v protocol.ReviewView, caps forge.Caps) (string, error) {
	outcome := v.Verdict.Outcome
	if outcome == protocol.OutcomeComment || v.Spec.Policy == nil || !v.Spec.Policy.Formal {
		return forge.EventComment, nil
	}
	if formalSupported(outcome, caps) {
		return outcome, nil
	}
	return "", forge.Errorf(forge.ClassUnsupported, "this forge adapter can't publish %s formally; the verdict is kept and nothing was written", outcome)
}

func formalSupported(outcome string, caps forge.Caps) bool {
	if outcome == protocol.OutcomeApprove {
		return caps.FormalApprove
	}
	return caps.FormalRequestChanges
}

// write checks the change hasn't moved, takes a write intent, and only then
// publishes once.
func write(ctx context.Context, c reviewClient, f forge.Forge, v protocol.ReviewView) ReviewResult {
	ev, err := event(v, f.Capabilities())
	if err != nil {
		return notWritten(v, err)
	}
	current, err := f.Resolve(ctx, v.Spec.ReviewTarget)
	if err != nil {
		return notWritten(v, err)
	}
	if current != *v.Revisions {
		return markStale(ctx, c, v, current)
	}
	identity, err := f.Identity(ctx)
	if err != nil {
		return notWritten(v, err)
	}
	held, acquired, err := takeIntent(ctx, c, v, identity)
	if err != nil || !acquired {
		return notWritten(v, intentRefusal(err))
	}
	return submitted(ctx, c, held, f.Publish)(forge.Submission{Target: v.Spec.ReviewTarget, Pinned: *v.Revisions, Event: ev,
		Summary: v.Verdict.Summary, Findings: v.Verdict.Findings, Identity: identity,
		Marker: forge.Marker(v.ID, v.Attempt, held.Publication.Intent)})
}

// takeIntent asks the broker for the write intent; only an acquired one may
// write.
func takeIntent(ctx context.Context, c reviewClient, v protocol.ReviewView, identity string) (protocol.ReviewView, bool, error) {
	step := protocol.PublicationUpdate{From: v.Publication.State, To: protocol.PublicationSubmitting, Token: uuid.NewString(), Identity: identity}
	out, err := c.ReviewCall(ctx, protocol.Message{Type: protocol.TypeReviewPublication, ReviewID: v.ID, Attempt: v.Attempt, PublicationStep: &step})
	if err != nil || out.Review == nil {
		return v, false, errors.Join(err, errNoReview(out))
	}
	return *out.Review, out.IntentAcquired != nil && *out.IntentAcquired, nil
}

func errNoReview(out protocol.Message) error {
	if out.Review == nil {
		return errors.New("the broker answered without the review")
	}
	return nil
}

func intentRefusal(err error) error {
	if err != nil {
		return err
	}
	return forge.Errorf("not_acquired", "another publisher holds the write intent; nothing was written")
}

// submitted publishes under the held intent and records the answer: the
// receipt, a failure, or an uncertain outcome to reconcile later.
func submitted(ctx context.Context, c reviewClient, held protocol.ReviewView, publishFn func(context.Context, forge.Submission) (forge.Receipt, error)) func(forge.Submission) ReviewResult {
	return func(s forge.Submission) ReviewResult {
		receipt, err := publishFn(ctx, s)
		step := protocol.PublicationUpdate{From: protocol.PublicationSubmitting, Intent: held.Publication.Intent}
		outcome := &ForgeOutcome{Wrote: true}
		switch {
		case err == nil:
			// The receipt is what the forge recorded; it is kept as it is,
			// and a difference from what was intended is reported.
			step.To, step.Receipt = protocol.PublicationPublished, recorded(receipt)
			outcome.Published, outcome.Event = true, receipt.Event
			if !forge.Matches(receipt, forge.Expected{Identity: s.Identity, Revision: s.Pinned.Head, Event: s.Event}) {
				outcome.Class = "mismatch"
				outcome.Note = fmt.Sprintf("the forge recorded %s as %q on %s, not %s as %q on %s as intended",
					receipt.Event, receipt.Identity, receipt.Revision, s.Event, s.Identity, s.Pinned.Head)
			}
		case definiteRefusal(err):
			step.To, step.Reason = protocol.PublicationFailed, forge.ClassOf(err)
			outcome.Wrote, outcome.Class, outcome.Note = false, forge.ClassOf(err), err.Error()
		default:
			step.To, step.Reason = protocol.PublicationUncertain, forge.ClassUncertain
			outcome.Class = forge.ClassUncertain
			outcome.Note = "the write may have landed (" + err.Error() + "); publish_review will only look for it, never write it again"
		}
		return recordStep(ctx, c, held, step, outcome)
	}
}

// recorded is the receipt as the broker keeps it, the forge's own event
// included, so a later reader sees what was recorded rather than what was
// intended.
func recorded(r forge.Receipt) *protocol.PublicationReceipt {
	p := r.PublicationReceipt
	p.Event = r.Event
	return &p
}

// definiteRefusals are the classes an adapter returns only when the forge
// refused the write for certain. Anything else after a write was sent,
// unclassified errors included, may have landed.
var definiteRefusals = map[string]bool{forge.ClassNotFound: true, forge.ClassForbidden: true, forge.ClassSelfReview: true,
	forge.ClassRateLimited: true, forge.ClassUnsupported: true, forge.ClassInvalid: true}

func definiteRefusal(err error) bool {
	var e *forge.Error
	return errors.As(err, &e) && definiteRefusals[e.Class]
}

// reconcile settles a write in flight by looking for it, with every fact
// it must match. A miss or a partial match never leads to a second write.
func reconcile(ctx context.Context, c reviewClient, f forge.Forge, v protocol.ReviewView) ReviewResult {
	ev, err := event(v, f.Capabilities())
	if err != nil {
		return notWritten(v, err)
	}
	expected := forge.Expected{Marker: forge.Marker(v.ID, v.Attempt, v.Publication.Intent), Identity: v.Publication.Identity,
		Revision: v.Revisions.Head, Event: ev}
	receipt, found, err := f.Find(ctx, v.Spec.ReviewTarget, expected)
	step := protocol.PublicationUpdate{From: v.Publication.State, Intent: v.Publication.Intent}
	outcome := &ForgeOutcome{}
	switch {
	case found:
		step.To, step.Receipt = protocol.PublicationPublished, recorded(receipt)
		outcome.Published = true
	case err != nil && forge.ClassOf(err) != forge.ClassAmbiguous:
		return notWritten(v, err)
	case v.Publication.State == protocol.PublicationUncertain:
		return ReviewResult{Review: &v, Forge: lookupMiss(err)}
	default:
		step.To, step.Reason = protocol.PublicationUncertain, "interrupted"
		outcome = lookupMiss(err)
	}
	return recordStep(ctx, c, v, step, outcome)
}

func lookupMiss(err error) *ForgeOutcome {
	if err != nil {
		return &ForgeOutcome{Class: forge.ClassAmbiguous, Note: err.Error() + "; the publication stays uncertain and nothing is written"}
	}
	return &ForgeOutcome{Class: forge.ClassUncertain, Note: "no matching review found yet; that doesn't prove none landed, so nothing is written"}
}

// markStale records that the change moved before publishing.
func markStale(ctx context.Context, c reviewClient, v protocol.ReviewView, current protocol.Revisions) ReviewResult {
	step := protocol.PublicationUpdate{From: v.Publication.State, To: protocol.PublicationStale, Observed: &current, Reason: "moved"}
	return recordStep(ctx, c, v, step, &ForgeOutcome{Class: "stale", Note: "the change moved since the review; nothing was published from this verdict"})
}

// recordStep tells the broker what happened; if the broker can't be told,
// the result still says what happened on the forge.
func recordStep(ctx context.Context, c reviewClient, v protocol.ReviewView, step protocol.PublicationUpdate, outcome *ForgeOutcome) ReviewResult {
	out, err := c.ReviewCall(ctx, protocol.Message{Type: protocol.TypeReviewPublication, ReviewID: v.ID, Attempt: v.Attempt, PublicationStep: &step})
	if err == nil && out.Review != nil {
		return ReviewResult{Review: out.Review, Forge: outcome}
	}
	outcome.Note = strings.TrimPrefix(outcome.Note+"; the broker was not told: "+fmt.Sprint(err), "; ")
	return ReviewResult{Review: &v, Forge: outcome}
}

func notWritten(v protocol.ReviewView, err error) ReviewResult {
	return ReviewResult{Review: &v, Forge: &ForgeOutcome{Class: forge.ClassOf(err), Note: err.Error()}}
}

// checkReview records that the change moved since the verdict; it never
// touches the publication.
func checkReview(ctx context.Context, c reviewClient, id string) (ReviewResult, error) {
	v, f, err := ownReview(ctx, c, id)
	if err != nil {
		return ReviewResult{}, err
	}
	if v.Verdict == nil || v.Revisions == nil {
		return ReviewResult{}, errors.New("there is no verdict to check yet")
	}
	current, err := f.Resolve(ctx, v.Spec.ReviewTarget)
	if err != nil {
		return ReviewResult{}, forgeFailure("resolve", err)
	}
	if current == *v.Revisions {
		return ReviewResult{Review: &v, Forge: &ForgeOutcome{Note: "the change has not moved"}}, nil
	}
	return reviewStep(ctx, c, protocol.Message{Type: protocol.TypeReviewObserve, ReviewID: id, Attempt: v.Attempt, Revisions: &current})
}
