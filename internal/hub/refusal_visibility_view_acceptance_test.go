package hub

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func refusalViewAsk() *messaging.ReviewAsk {
	return &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "259", Base: "base-pin", Head: "head-pin", Scope: "check boundaries", Publish: true, Formal: true}
}

func assertRefusalView(t *testing.T, v protocol.ContactMessageView) {
	t.Helper()
	messageSyncEqual(t, v.Direction, "in")
	messageSyncEqual(t, v.State, "refused")
	messageSyncEqual(t, v.RefusalReason, "not_shared")
	messageSyncEqual(t, v.RefusalClass, "terminal")
	messageSyncEqual(t, v.Text, "")
	messageSyncEqual(t, v.ReviewState, "refused")
	messageSyncEqual(t, v.ReviewReason, "not_shared")
	messageSyncEqual(t, v.ReviewID, "")
	if v.ReviewAsk == nil {
		t.Fatal("refusal lost pinned ask")
	}
	want := protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"}, Base: "base-pin", Head: "head-pin", Scope: "check boundaries", Publish: true, Formal: true}
	messageSyncEqual(t, *v.ReviewAsk, want)
}

func TestRefusalVisibilityViewAcceptanceRefusedRowNeedsNoImport(t *testing.T) {
	h, key := remoteHubFixture(t)
	r := messaging.Row{Contact: key, Direction: messaging.DirIn, State: messaging.StateRefused,
		Refusal: messaging.Refuse(messaging.ReasonNotShared),
		Message: messaging.Message{ID: "refused-view", N: 1, From: "sender", To: []string{"reviewer"}, Review: refusalViewAsk()}}
	v := contactMessageView(r)
	messageWorkerOK(t, h.reviewView(context.Background(), &v, r))
	assertRefusalView(t, v)
}

func TestRefusalVisibilityViewAcceptanceContactPageAndRead(t *testing.T) {
	h, key := remoteHubFixture(t)
	ctx, at := context.Background(), time.Now().UTC()
	m := messaging.Message{ID: "refused-page", N: 1, CreatedAt: at, Text: "must not be retained", From: "sender", To: []string{"reviewer"}, Review: refusalViewAsk()}
	a, err := h.store.PeerMessages().Accept(ctx, key, m, at)
	messageWorkerOK(t, err)
	messageSyncEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	for _, read := range []bool{false, true} {
		if read {
			n, err := h.store.PeerMessages().MarkRead(ctx, key, []string{m.ID}, at)
			messageWorkerOK(t, err)
			messageSyncEqual(t, n, 1)
		}
		out := h.contactList(ctx, protocol.Message{ContactKey: key, ContactOp: protocol.ContactOpList})
		if out.ContactPage == nil || len(out.ContactPage.Messages) != 1 {
			t.Fatalf("missing refused ask in page: %+v", out)
		}
		v := out.ContactPage.Messages[0]
		assertRefusalView(t, v)
		messageSyncEqual(t, v.From, m.From)
		if !reflect.DeepEqual(v.To, m.To) {
			t.Fatalf("recipient changed: %v", v.To)
		}
		messageSyncEqual(t, v.ReadAt != nil, read)
		messageSyncEqual(t, v.Receipt, "")
	}
}
