package forge

import (
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func contractTarget() Target {
	return Target{Forge: "fake", Repository: "fake.example/space/repo", Change: "CR:7~revision"}
}

func contractPins() Revisions {
	return Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
}

func contractSubmission() Submission {
	return Submission{Target: contractTarget(), Pinned: contractPins(), Event: EventApprove, Summary: "Reviewed pinned changes", Identity: "fake-reviewer", Marker: Marker("request/7", 1, 1)}
}

func contractExpected() Expected {
	s := contractSubmission()
	return Expected{Marker: s.Marker, Identity: s.Identity, Revision: s.Pinned.Head, Event: s.Event}
}

func contractReceipt() Receipt {
	return Receipt{PublicationReceipt: protocol.PublicationReceipt{ReviewID: "fk-existing", Identity: "fake-reviewer", Revision: contractPins().Head, Formal: true}, Event: EventApprove}
}

func TestReviewForgeAcceptanceLostResponseReconcilesWithoutSecondWrite(t *testing.T) {
	f := NewFake()
	f.SetRevisions(contractTarget(), contractPins())
	f.LandThenFail(Errorf(ClassUncertain, "response lost"))
	_, err := f.Publish(context.Background(), contractSubmission())
	contractEqual(t, "lost response class", ClassOf(err), ClassUncertain)
	r, found, err := f.Find(context.Background(), contractTarget(), contractExpected())
	contractNoError(t, err)
	contractEqual(t, "saved review found", found, true)
	contractEqual(t, "receipt identity", r.Identity, "fake-reviewer")
	contractEqual(t, "receipt event", r.Event, EventApprove)
	contractEqual(t, "receipt revision", r.Revision, contractPins().Head)
	contractEqual(t, "provider-specific review ID", strings.HasPrefix(r.ReviewID, "fk-"), true)
	contractEqual(t, "single write attempt", len(f.Attempts()), 1)
	contractEqual(t, "single provider review", len(f.Reviews(contractTarget())), 1)
}

func TestReviewForgeAcceptanceCopiedMarkerCannotProvePublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Receipt)
	}{
		{"other-publisher", func(r *Receipt) { r.Identity = "change-author" }},
		{"other-revision", func(r *Receipt) { r.Revision = strings.Repeat("c", 40) }},
		{"wrong-event", func(r *Receipt) { r.Event = EventComment }},
		{"not-formal", func(r *Receipt) { r.Formal = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFake()
			r := contractReceipt()
			tc.change(&r)
			f.AddReview(contractTarget(), r, contractExpected().Marker)
			_, found, err := f.Find(context.Background(), contractTarget(), contractExpected())
			contractAmbiguous(t, found, err)
		})
	}
}

func TestReviewForgeAcceptanceMultipleMatchesRemainAmbiguous(t *testing.T) {
	f := NewFake()
	a, b := contractReceipt(), contractReceipt()
	a.ReviewID, b.ReviewID = "fk-one", "fk-two"
	f.AddReview(contractTarget(), a, contractExpected().Marker)
	f.AddReview(contractTarget(), b, contractExpected().Marker)
	_, found, err := f.Find(context.Background(), contractTarget(), contractExpected())
	contractAmbiguous(t, found, err)
}

func TestReviewForgeAcceptanceMarkerSafelyEncodesRequestIdentity(t *testing.T) {
	ids := []string{"plain", "x --> injected <!--", "same:1:2", "same/1/2", "日本語"}
	seen := map[string]bool{}
	for _, id := range ids {
		for _, tuple := range [][2]int{{1, 1}, {1, 2}, {2, 1}} {
			marker := Marker(id, tuple[0], tuple[1])
			if seen[marker] {
				t.Fatal("different review/attempt/intent shared marker")
			}
			seen[marker] = true
			contractEqual(t, "one comment opening", strings.Count(marker, "<!--"), 1)
			contractEqual(t, "one comment closing", strings.Count(marker, "-->"), 1)
			contractEqual(t, "closing delimiter retained", strings.HasSuffix(marker, " -->"), true)
			if marker != Marker(id, tuple[0], tuple[1]) {
				t.Fatal("marker not stable for recovery")
			}
		}
	}
}

func TestReviewForgeAcceptancePinnedDiffRemainsAvailableAfterHeadMoves(t *testing.T) {
	f := NewFake()
	pins := contractPins()
	f.SetRevisions(contractTarget(), pins)
	f.SetChanges(contractTarget(), []FileChange{{Path: "old.go", Lines: []LineRange{{From: 4, To: 7}}}})
	moved := pins
	moved.Head = strings.Repeat("c", 40)
	f.SetRevisions(contractTarget(), moved)
	f.SetChanges(contractTarget(), []FileChange{{Path: "new.go", Lines: []LineRange{{From: 1, To: 2}}}})
	files, err := f.Changes(context.Background(), contractTarget(), pins)
	contractNoError(t, err)
	contractEqual(t, "pinned diff file count", len(files), 1)
	contractEqual(t, "original diff path retained", files[0].Path, "old.go")
	current, err := f.Resolve(context.Background(), contractTarget())
	contractNoError(t, err)
	contractEqual(t, "current revisions independent of pinned diff", current, moved)
}

func contractEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func contractNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func contractAmbiguous(t *testing.T, found bool, err error) {
	t.Helper()
	contractEqual(t, "ambiguous review not adopted", found, false)
	contractEqual(t, "ambiguity reported as error", err != nil, true)
	contractEqual(t, "ambiguity class", ClassOf(err), ClassAmbiguous)
}
