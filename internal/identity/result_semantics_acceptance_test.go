package identity_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/signing"
)

func TestIdentityResultSemanticsAcceptanceDeferredEquivocationReportsFreeze(t *testing.T) {
	p := loadWirePacket(t)
	h := approvedAnchor(t, p)
	s := parsedVector(t, p, "succession-1").(identity.Succession)
	a := identity.Record{Lineage: h.Lineage, Epoch: 2, Version: 7, Person: identity.KeyID(s.NextPerson), Recovery: h.Recovery,
		Counter: h.Counter, At: time.UnixMilli(1700000000040)}.Signed(fixturePrivate(t, p, "P1"))
	b := a
	b.At = b.At.Add(time.Millisecond)
	b = b.Signed(fixturePrivate(t, p, "P1"))
	h = applyEffect(t, h, a, identity.Parked)
	h = applyEffect(t, h, b, identity.Parked)
	next, effect, err := h.Apply(s, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if next.Trust != identity.Frozen {
		t.Fatal("deferred record equivocation did not freeze")
	}
	if effect.Kind != identity.FrozenEffect {
		t.Fatalf("caller received %s despite frozen trust", effect.Kind)
	}
	if len(effect.Branches) != 2 {
		t.Fatal("freeze effect lost conflicting branch evidence")
	}
}

func TestIdentityResultSemanticsAcceptanceReceiptExpiryPrecedesApproval(t *testing.T) {
	p := loadWirePacket(t)
	for _, trust := range []identity.Trust{identity.Unapproved, identity.Frozen} {
		t.Run(string(trust), func(t *testing.T) {
			c := newAdmissionCase(t, p)
			c.h.Trust = trust
			c.now = c.seal.Chain[0].NotAfter
			result, evidence := identity.Admit(c.seal, c.frame, c.h, identity.Clock{Now: func() time.Time { return c.now }})
			if result.State != signing.Invalid {
				t.Fatalf("expired current-epoch certificate classified %s because lineage is %s", result.State, trust)
			}
			if evidence.State != signing.Invalid {
				t.Fatal("evidence recorded a weaker trust decision than receipt validity")
			}
		})
	}
}
