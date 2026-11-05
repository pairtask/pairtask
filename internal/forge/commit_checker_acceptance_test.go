package forge_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
)

func advisoryPinFixture() (*forge.Fake, forge.Target, forge.Revisions) {
	f := forge.NewFake()
	target := forge.Target{Forge: "fake", Repository: "fake.example/space/repo", Change: "CR-304"}
	pins := forge.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	f.SetRevisions(target, pins)
	f.SetCommit(target, pins.Base, true)
	f.SetCommit(target, pins.Head, true)
	return f, target, pins
}

func advisoryChecks(t *testing.T, got, want []forge.PinCheck) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pin advisory = %+v, want %+v", got, want)
	}
}

func TestCommitCheckerAcceptancePinChecksKeepExistingDivergentBase(t *testing.T) {
	f, target, pins := advisoryPinFixture()
	now := pins
	now.Base = strings.Repeat("c", 40)
	f.SetRevisions(target, now)
	advisoryChecks(t, forge.CheckPins(context.Background(), f, target, pins), nil)
}

func TestCommitCheckerAcceptanceMovedHeadAndMissingPinsHaveDifferentAdvice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		head, base bool
		want       []forge.PinCheck
	}{
		{"moved-existing-head", true, true, []forge.PinCheck{{Code: "head_moved", Current: strings.Repeat("c", 40)}}},
		{"missing-head", false, true, []forge.PinCheck{{Code: "head_missing"}}},
		{"missing-base", true, false, []forge.PinCheck{{Code: "base_missing"}, {Code: "head_moved", Current: strings.Repeat("c", 40)}}},
		{"both-missing", false, false, []forge.PinCheck{{Code: "head_missing"}, {Code: "base_missing"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, target, pins := advisoryPinFixture()
			f.SetRevisions(target, forge.Revisions{Base: pins.Base, Head: strings.Repeat("c", 40)})
			f.SetCommit(target, pins.Head, tc.head)
			f.SetCommit(target, pins.Base, tc.base)
			advisoryChecks(t, forge.CheckPins(context.Background(), f, target, pins), tc.want)
		})
	}
}

func TestCommitCheckerAcceptanceUnreadableChangeNeverChecksCommits(t *testing.T) {
	f, target, pins := advisoryPinFixture()
	f.Fail(forge.OpResolve, forge.Errorf(forge.ClassForbidden, "private change"))
	marker := errors.New("commit lookup must remain unused")
	f.Fail(forge.OpCommit, marker)
	advisoryChecks(t, forge.CheckPins(context.Background(), f, target, pins), []forge.PinCheck{{Code: "not_checked", Class: "forbidden"}})
	_, err := f.CommitExists(context.Background(), target, pins.Head)
	if !errors.Is(err, marker) {
		t.Fatalf("unreadable change consumed a commit lookup: %v", err)
	}
}

func TestCommitCheckerAcceptanceLookupFailureIsNotMissing(t *testing.T) {
	f, target, pins := advisoryPinFixture()
	f.Fail(forge.OpCommit, forge.Errorf(forge.ClassUnavailable, "provider unavailable"))
	advisoryChecks(t, forge.CheckPins(context.Background(), f, target, pins), []forge.PinCheck{{Code: "not_checked", Class: "unavailable"}})
}

type advisoryForgeOnly struct{ forge.Forge }

type advisoryCanceledForge struct{ *forge.Fake }

func (f advisoryCanceledForge) Resolve(ctx context.Context, _ forge.Target) (forge.Revisions, error) {
	return forge.Revisions{}, ctx.Err()
}

func TestCommitCheckerAcceptanceUnsupportedAndCanceledAreExplicit(t *testing.T) {
	f, target, pins := advisoryPinFixture()
	advisoryChecks(t, forge.CheckPins(context.Background(), advisoryForgeOnly{f}, target, pins), []forge.PinCheck{{Code: "not_checked", Class: "unsupported"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	advisoryChecks(t, forge.CheckPins(ctx, advisoryCanceledForge{f}, target, pins), []forge.PinCheck{{Code: "not_checked", Class: "timeout"}})
}

func TestCommitCheckerAcceptanceFakeCommitBelongsToRepository(t *testing.T) {
	f, target, pins := advisoryPinFixture()
	other := target
	other.Repository = "fake.example/other/repo"
	exists, err := f.CommitExists(context.Background(), other, pins.Head)
	if err != nil || exists {
		t.Fatalf("commit leaked into another repository: exists=%v err=%v", exists, err)
	}
	other = target
	other.Change = "CR-another"
	exists, err = f.CommitExists(context.Background(), other, pins.Head)
	if err != nil || !exists {
		t.Fatalf("same repository lost commit across changes: exists=%v err=%v", exists, err)
	}
}
