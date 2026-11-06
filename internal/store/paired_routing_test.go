package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func TestHomeRoutingIsTheStoredCanonicalRoute(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	send := func(id string, here []string, operator bool) {
		_, err := f.db.OperatorContactSend(ctx, store.ContactSend{OperationID: id, Contact: f.contact, Text: "question", Here: here, OperatorHere: operator,
			AuditID: f.audit(t, contactAuditInput{"send", id}), Now: time.Now().UTC()})
		pairOK(t, err)
	}
	send("tagged", []string{"home-b", "home-a"}, true)
	send("untagged", nil, false)
	route, err := f.db.HomeRouting(ctx, f.contact, "tagged")
	pairOK(t, err)
	pairEqual(t, slices.Equal(route.Agents, []string{"home-a", "home-b"}), true)
	pairEqual(t, route.Operator, true)
	for _, other := range []struct{ contact, id string }{{f.contact, "untagged"}, {"sha256:elsewhere", "tagged"}} {
		route, err = f.db.HomeRouting(ctx, other.contact, other.id)
		pairOK(t, err)
		pairEqual(t, len(route.Agents), 0)
		pairEqual(t, route.Operator, false)
	}
}
