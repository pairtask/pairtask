package bridge

import (
	"context"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
)

func TestConsentPublicationAcceptanceEndedSessionCannotReserve(t *testing.T) {
	f := newConsentPublicationFixture(t, protocol.ConsentSession)
	_, err := reviewStep(f.ctx, f.c, protocol.Message{Type: protocol.TypeReviewSubmit, ReviewID: f.id, Attempt: 1, Verdict: &protocol.Verdict{Outcome: protocol.OutcomeApprove, Summary: "saved verdict"}})
	requireNoError(t, err)
	f.endOwner(t)
	_, err = publishReview(f.ctx, f.c, f.id)
	requireNoError(t, err)
	assertEqual(t, "no forge write after owner replacement", len(f.fake.Attempts()), 0)
	assertEqual(t, "reservation remains pending", f.view(t).Publication.State, protocol.PublicationPending)
}

func TestConsentPublicationAcceptanceFailedRetryRechecksSession(t *testing.T) {
	f := newConsentPublicationFixture(t, protocol.ConsentSession)
	f.fake.Fail(forge.OpPublish, forge.Errorf(forge.ClassForbidden, "definitely refused"))
	_, err := submitReview(f.ctx, f.c, f.verdict())
	requireNoError(t, err)
	assertEqual(t, "first write refused", f.view(t).Publication.State, protocol.PublicationFailed)
	assertEqual(t, "one attempted write", len(f.fake.Attempts()), 1)
	f.endOwner(t)
	_, err = publishReview(f.ctx, f.c, f.id)
	requireNoError(t, err)
	assertEqual(t, "expired session made no retry write", len(f.fake.Attempts()), 1)
	assertEqual(t, "failed reservation unchanged", f.view(t).Publication.State, protocol.PublicationFailed)
}

type consentBlockedForge struct {
	*forge.Fake
	started chan struct{}
	release chan struct{}
}

func (f *consentBlockedForge) Publish(ctx context.Context, s forge.Submission) (forge.Receipt, error) {
	close(f.started)
	select {
	case <-f.release:
		return f.Fake.Publish(ctx, s)
	case <-ctx.Done():
		return forge.Receipt{}, ctx.Err()
	}
}

func TestConsentPublicationAcceptanceAlreadyStartedWriteReportsActualResult(t *testing.T) {
	f := newConsentPublicationFixture(t, protocol.ConsentSession)
	blocked := &consentBlockedForge{Fake: f.fake, started: make(chan struct{}), release: make(chan struct{})}
	f.c.forges["fake"] = blocked
	result := make(chan error, 1)
	go func() { _, err := submitReview(f.ctx, f.c, f.verdict()); result <- err }()
	select {
	case <-blocked.started:
	case <-f.ctx.Done():
		t.Fatal("publication never reached external write")
	}
	f.endOwner(t)
	close(blocked.release)
	requireNoError(t, <-result)
	assertEqual(t, "started external write recorded truthfully", f.view(t).Publication.State, protocol.PublicationPublished)
	assertEqual(t, "exactly one external write", len(f.fake.Attempts()), 1)
}

func TestConsentPublicationAcceptanceOnceSurvivesOwnerReplacement(t *testing.T) {
	f := newConsentPublicationFixture(t, protocol.ConsentOnce)
	f.endOwner(t)
	_, err := submitReview(f.ctx, f.c, f.verdict())
	requireNoError(t, err)
	assertEqual(t, "once retained immutable authority", f.view(t).Publication.State, protocol.PublicationPublished)
	assertEqual(t, "one write", len(f.fake.Attempts()), 1)
}

func TestConsentPublicationAcceptanceUncertainWriteNeverAutomaticallyReplayed(t *testing.T) {
	f := newConsentPublicationFixture(t, protocol.ConsentSession)
	f.fake.LandThenFail(forge.Errorf(forge.ClassUncertain, "reply lost"))
	_, err := submitReview(f.ctx, f.c, f.verdict())
	requireNoError(t, err)
	assertEqual(t, "uncertain publication retained", f.view(t).Publication.State, protocol.PublicationUncertain)
	f.endOwner(t)
	_, err = publishReview(f.ctx, f.c, f.id)
	requireNoError(t, err)
	assertEqual(t, "reconciliation did not repeat write", len(f.fake.Attempts()), 1)
	assertEqual(t, "actual earlier write reconciled", f.view(t).Publication.State, protocol.PublicationPublished)
}
