package bridge

import (
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewFreshnessPartialAcceptanceIncompletePinsAreUnknown(t *testing.T) {
	for _, missing := range []string{"base", "head", "both"} {
		t.Run(missing, func(t *testing.T) {
			pins := reviewBridgePins()
			if missing != "head" {
				pins.Base = ""
			}
			if missing != "base" {
				pins.Head = ""
			}
			ask := freshnessAsk()
			ask["requested"] = pins
			f := &freshnessForge{Forge: forge.NewFake(), current: reviewBridgePins()}
			field := freshnessField(t, freshnessRead(t, ask, f, freshnessArgs(true)), "unknown")
			if field["reason"] == nil || field["reason"] == "" {
				t.Fatal("missing pins need an explanation")
			}
			assertEqual(t, "incomplete pins cannot support a comparison", f.calls.Load(), int32(0))
		})
	}
}

func TestReviewFreshnessPartialAcceptanceIncompleteForgeResultIsUnknown(t *testing.T) {
	f := &freshnessForge{Forge: forge.NewFake(), current: protocol.Revisions{}}
	field := freshnessField(t, freshnessRead(t, freshnessAsk(), f, freshnessArgs(true)), "unknown")
	if field["reason"] == nil || field["reason"] == "" {
		t.Fatal("incomplete forge response needs an explanation")
	}
	assertEqual(t, "one bounded attempt", f.calls.Load(), int32(1))
}
