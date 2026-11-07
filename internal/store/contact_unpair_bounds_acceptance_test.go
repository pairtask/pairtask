package store_test

import (
	"context"
	"math"
	"reflect"
	"testing"
)

func TestContactUnpairBoundsAcceptanceCountersNeverWrapOrPartiallyRevoke(t *testing.T) {
	for _, counter := range []string{"generation", "mark"} {
		t.Run(counter, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			query := "UPDATE peer_contacts SET generation=? WHERE identity_key=?"
			args := []any{int64(math.MaxInt64), f.contact}
			if counter == "mark" {
				query = "UPDATE pairing_marks SET mark=? WHERE id=1"
				args = args[:1]
			}
			_, err := f.raw.Exec(query, args...)
			pairOK(t, err)
			r := unpairRequest(t, f, "exhausted-"+counter)
			before := unpairSnapshot(t, f)
			_, _, err = f.db.UnpairContact(context.Background(), r)
			if err == nil {
				t.Fatal("unpair accepted an exhausted authority counter")
			}
			if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
				t.Fatal("counter exhaustion changed authority, audit or queued state")
			}
		})
	}
}
