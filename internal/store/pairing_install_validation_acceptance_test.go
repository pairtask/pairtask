package store_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestPairingInstallAcceptanceOutcomeAuthorityInputsAreImmutable(t *testing.T) {
	for _, change := range []string{"mode", "mark", "repair"} {
		t.Run(change, func(t *testing.T) {
			f := installFixture(t)
			original := f.epochOutcome(t, 101)
			_, err := f.db.Commit(context.Background(), original)
			pairOK(t, err)
			before := f.installAuthority(t)
			replay := original
			switch change {
			case "mode":
				replay.Mode = peer.ModeLegacy
			case "mark":
				replay.BegunMark++
			case "repair":
				replay.Repair = f.contact
			}
			_, err = f.db.Commit(context.Background(), replay)
			pairEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("conflicting replay changed an authority input")
			}
		})
	}
}

func TestPairingInstallAcceptanceUnsignedMarkCannotWrapIntoSQLite(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	o.BegunMark = uint64(math.MaxInt64) + 1
	before := f.installAuthority(t)
	_, err := f.db.Commit(context.Background(), o)
	if err == nil {
		t.Fatal("mark above MaxInt64 was accepted")
	}
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("invalid unsigned mark changed authority")
	}
}
