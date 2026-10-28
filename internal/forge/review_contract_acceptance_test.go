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
	if ClassOf(err) != ClassUncertain {
		t.Fatalf("lost response class: %v", err)
	}
	r, found, err := f.Find(context.Background(), contractTarget(), contractExpected())
	if err != nil || !found {
		t.Fatalf("saved review not found: found=%v err=%v", found, err)
	}
	if r.Identity != "fake-reviewer" || r.Event != EventApprove || r.Revision != contractPins().Head || !strings.HasPrefix(r.ReviewID, "fk-") {
		t.Fatalf("provider receipt lost facts: %+v", r)
	}
	if len(f.Attempts()) != 1 || len(f.Reviews(contractTarget())) != 1 {
		t.Fatal("reconciliation made another write")
	}
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
			if found || err == nil || ClassOf(err) != ClassAmbiguous {
				t.Fatalf("copied marker accepted: found=%v err=%v", found, err)
			}
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
	if found || err == nil || ClassOf(err) != ClassAmbiguous {
		t.Fatalf("multiple provider writes collapsed into one: found=%v err=%v", found, err)
	}
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
			if strings.Count(marker, "<!--") != 1 || strings.Count(marker, "-->") != 1 || !strings.HasSuffix(marker, " -->") {
				t.Fatalf("request ID escaped comment: %q", marker)
			}
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
	if err != nil || len(files) != 1 || files[0].Path != "old.go" {
		t.Fatalf("pinned diff silently replaced or lost: %+v err=%v", files, err)
	}
	current, err := f.Resolve(context.Background(), contractTarget())
	if err != nil || current != moved {
		t.Fatalf("current revisions not independently available: %+v %v", current, err)
	}
}
