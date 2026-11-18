package identity_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/signing"
)

func TestIdentityEpochBoundaryAcceptanceRecoveryCannotReopenLegacyScope(t *testing.T) {
	p := loadWirePacket(t)
	c := newAdmissionCase(t, p)
	h := applyEffect(t, c.h, parsedVector(t, p, "succession-1"), identity.Applied)
	cert := c.seal.Chain[0]
	cert.Workspace = c.seal.Header.Workspace
	cert = signing.Issue(fixturePrivate(t, p, "P1"), cert)
	root := ed25519.PublicKey(unhex(t, p.Fixtures["P1"].Public))
	seal := signing.Sign(fixturePrivate(t, p, "D0"), []signing.Cert{cert}, root, c.seal.Header)
	result, _ := identity.Admit(seal, c.frame, h, identity.Clock{Now: func() time.Time { return c.now }})
	if result.State != signing.Invalid {
		t.Fatalf("recovery reopened legacy fallback without a current binding: %+v", result)
	}
}

func TestIdentityEpochBoundaryAcceptanceNewThirdBranchInvalidatesDisplay(t *testing.T) {
	p := loadWirePacket(t)
	h := displayedConflict(t, p)
	r := parsedVector(t, p, "local-resolution").(identity.Resolution)
	third := parsedVector(t, p, "fork-at-anchor").(identity.Succession)
	third.NextPerson = ed25519.PublicKey(unhex(t, p.Fixtures["PLegacy"].Public))
	third.At = third.At.Add(time.Millisecond)
	third = third.Signed(fixturePrivate(t, p, "R0"))
	next, _, err := h.Apply(third, time.UnixMilli(1700000010000))
	if err != nil {
		t.Fatal(err)
	}
	if next.Trust != identity.Frozen {
		t.Fatal("third branch advanced frozen trust")
	}
	_, err = identity.Resolve(next, r, localPublic(t, p))
	if err == nil {
		t.Fatal("confirmation for two branches accepted after a third valid branch arrived")
	}
}
