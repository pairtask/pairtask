package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/store"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestConsentSessionAcceptanceReusableAuthorityIsBoundToScope(t *testing.T) {
	c := newConsentSessionCase(t)
	c.approve(t, consentMessage("first-X", 1, "X"))
	c.admitted(t, consentMessage("second-X", 2, "X"))
	consentHeld(t, c.f, consentMessage("different-agent", 3, "Y"), messageAt)
	consentHeld(t, c.f, consentScopeMessage("different-operation", 4, true, "X"), messageAt)
}
func TestConsentSessionAcceptancePublicationCeilingIsBounded(t *testing.T) {
	for _, formal := range []bool{false, true} {
		name := "comment-only"
		if formal {
			name = "formal"
		}
		t.Run(name, func(t *testing.T) {
			c := newConsentSessionCase(t)
			pairOK(t, c.f.db.ResetConsentScope(context.Background(), store.ConsentReset{Contact: c.f.key, Agent: "X", Operation: protocol.ConsentPublish, AuditID: 0, Now: messageAt}))
			first := consentPublishAsk("publish-first", formal)
			first.To = []string{"X"}
			c.approve(t, first)
			lower := consentPublishAsk("publish-comment", false)
			lower.N, lower.To = 2, []string{"X"}
			c.admitted(t, lower)
			higher := consentPublishAsk("publish-formal", true)
			higher.N, higher.To = 3, []string{"X"}
			if formal {
				c.admitted(t, higher)
			} else {
				consentHeld(t, c.f, higher, messageAt)
			}
		})
	}
}
func TestConsentSessionAcceptanceOwnerFailureSuspendsAllWrappers(t *testing.T) {
	for _, action := range []string{"end", "replace"} {
		t.Run(action, func(t *testing.T) {
			c := newConsentSessionCase(t)
			c.approve(t, consentMessage("first-X", 1, "X"))
			c.admitted(t, consentMessage("before-failure", 2, "X"))
			c.failWrites(t, true)
			var err error
			if action == "replace" {
				err = c.owner.SetConsentOwner(context.Background(), c.epoch, c.gen+1)
			} else {
				err = c.owner.EndConsentOwner(context.Background(), c.epoch, c.gen)
			}
			if err == nil {
				t.Fatal("injected owner write unexpectedly succeeded")
			}
			c.oldOwnerRemains(t)
			consentHeld(t, c.f, consentMessage("after-failed-write", 3, "X"), messageAt)
			c.failWrites(t, false)
			c.gen++
			pairOK(t, c.owner.SetConsentOwner(context.Background(), c.epoch, c.gen))
			consentHeld(t, c.f, consentMessage("old-grant-invalid", 4, "X"), messageAt)
			c.approve(t, consentMessage("new-owner-approval", 5, "X"))
			c.admitted(t, consentMessage("new-owner-reuse", 6, "X"))
		})
	}
}
func TestConsentSessionAcceptanceOwnerLifetimeEndsReusableAuthority(t *testing.T) {
	for _, change := range []string{"end", "generation", "epoch", "restart"} {
		t.Run(change, func(t *testing.T) {
			c := newConsentSessionCase(t)
			c.approve(t, consentMessage("first-X", 1, "X"))
			switch change {
			case "end":
				pairOK(t, c.owner.EndConsentOwner(context.Background(), c.epoch, c.gen))
			case "generation":
				pairOK(t, c.owner.SetConsentOwner(context.Background(), c.epoch, c.gen+1))
			case "epoch":
				pairOK(t, c.owner.SetConsentOwner(context.Background(), "new-epoch", c.gen))
			case "restart":
				c.f = consentStoreReopen(t, c.f)
			}
			consentHeld(t, c.f, consentMessage("after-owner-ended", 2, "X"), messageAt)
		})
	}
}

func TestConsentSessionAcceptanceStaleOwnerEndCannotRevokeReplacement(t *testing.T) {
	c := newConsentSessionCase(t)
	c.gen++
	pairOK(t, c.owner.SetConsentOwner(context.Background(), c.epoch, c.gen))
	c.approve(t, consentMessage("replacement-approval", 1, "X"))
	pairOK(t, c.owner.EndConsentOwner(context.Background(), c.epoch, c.gen-1))
	c.admitted(t, consentMessage("replacement-survives-stale-end", 2, "X"))
}
