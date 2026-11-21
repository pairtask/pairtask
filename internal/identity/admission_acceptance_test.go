package identity_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/signing"
)

type admissionCase struct {
	h     identity.Held
	seal  signing.Seal
	frame signing.Frame
	now   time.Time
	mark  time.Time
}

func newAdmissionCase(t *testing.T, p wirePacket) admissionCase {
	t.Helper()
	h := approvedAnchor(t, p)
	r := parsedVector(t, p, "record").(identity.Record)
	h.Record, h.RecordDigest, h.Version, h.Bindings = r, identity.DigestOf(r.TBS()), r.Version, r.Bindings
	cert, err := signing.ParseCert(unhex(t, vectorNamed(t, p, "device").Wire))
	if err != nil {
		t.Fatal(err)
	}
	f := signing.Frame{MessageID: "identity-admission", SenderKind: signing.SenderOperator,
		SenderID: signing.OperatorID, Text: "public fixture message", Addresses: []string{"recipient"}}
	head := signing.Header{MessageID: f.MessageID, SenderKind: f.SenderKind, SenderID: f.SenderID,
		Workspace: r.Bindings[0].Claim.Workspace, Recipients: f.Addresses, CreatedAt: cert.NotBefore,
		TextHash: signing.TextHash(f.Text)}
	seal := signing.Sign(fixturePrivate(t, p, "D0"), []signing.Cert{cert}, h.Anchor.Person, head)
	return admissionCase{h: h, seal: seal, frame: f, now: cert.NotBefore}
}

func resignAdmission(t *testing.T, p wirePacket, c *admissionCase) {
	t.Helper()
	c.seal = signing.Sign(fixturePrivate(t, p, "D0"), c.seal.Chain, c.seal.RootKey, c.seal.Header)
}

func TestIdentityAdmissionAcceptanceTimeAndTrust(t *testing.T) {
	p := loadWirePacket(t)
	cases := []struct {
		name   string
		change func(*admissionCase)
		want   signing.State
	}{
		{"not-before-inclusive", func(c *admissionCase) {}, signing.Verified},
		{"last-valid-millisecond", func(c *admissionCase) { c.now = c.seal.Chain[0].NotAfter.Add(-time.Millisecond) }, signing.Verified},
		{"not-after-exclusive", func(c *admissionCase) { c.now = c.seal.Chain[0].NotAfter }, signing.Invalid},
		{"backdating-after-expiry", func(c *admissionCase) { c.now = c.seal.Chain[0].NotAfter.Add(365 * 24 * time.Hour) }, signing.Invalid},
		{"rollback-cannot-revive-expired-device", func(c *admissionCase) { c.mark = c.seal.Chain[0].NotAfter }, signing.Invalid},
		{"receipt-not-before-skew-boundary", func(c *admissionCase) { c.now = c.now.Add(-signing.MaxSkew) }, signing.Verified},
		{"receipt-too-early", func(c *admissionCase) { c.now = c.now.Add(-signing.MaxSkew - time.Millisecond) }, signing.Invalid},
		{"signed-before-certificate-start", func(c *admissionCase) {
			c.seal.Header.CreatedAt = c.seal.Chain[0].NotBefore.Add(-time.Millisecond)
			resignAdmission(t, p, c)
		}, signing.Invalid},
		{"revoked-device", func(c *admissionCase) { c.h.Revoked = []string{c.seal.KeyID} }, signing.Invalid},
		{"frozen-lineage", func(c *admissionCase) { c.h.Trust = identity.Frozen }, signing.Unverified},
		{"unapproved-lineage", func(c *admissionCase) { c.h.Trust = identity.Unapproved }, signing.Unverified},
		{"retired-person", func(c *admissionCase) {
			c.h.Epoch = 2
			c.h.Person = identity.KeyID(ed25519.PublicKey(unhex(t, p.Fixtures["P1"].Public)))
		}, signing.Invalid},
		{"binding-removed", func(c *admissionCase) { c.h.Bindings = nil }, signing.Invalid},
		{"wrong-text", func(c *admissionCase) { c.frame.Text = "changed by transport" }, signing.Invalid},
		{"wrong-recipient", func(c *admissionCase) { c.frame.Addresses = []string{"unaddressed"} }, signing.Invalid},
		{"wrong-room", func(c *admissionCase) { c.frame.Room = "other-room" }, signing.Invalid},
		{"bad-header-signature", func(c *admissionCase) { c.seal.Sig[0] ^= 1 }, signing.Invalid},
		{"bad-certificate-signature", func(c *admissionCase) { c.seal.Chain[0].Sig[0] ^= 1 }, signing.Invalid},
		{"device-cannot-speak-as-agent", func(c *admissionCase) {
			c.seal.Header.SenderKind, c.seal.Header.SenderID = signing.SenderAgent, "agent-1"
			c.frame.SenderKind, c.frame.SenderID = signing.SenderAgent, "agent-1"
			resignAdmission(t, p, c)
		}, signing.Invalid},
		{"legacy-cert-cannot-bypass-removed-binding", func(c *admissionCase) {
			cert := c.seal.Chain[0]
			cert.Workspace = c.seal.Header.Workspace
			c.seal.Chain[0] = signing.Issue(fixturePrivate(t, p, "P0"), cert)
			c.h.Bindings = nil
			resignAdmission(t, p, c)
		}, signing.Invalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newAdmissionCase(t, p)
			tc.change(&c)
			clock := identity.Clock{Now: func() time.Time { return c.now }, Mark: c.mark}
			result, evidence := identity.Admit(c.seal, c.frame, c.h, clock)
			if result.Reason == identity.ErrNotImplemented.Error() {
				t.Fatal("unimplemented admission is not a trust decision")
			}
			if result.State != tc.want {
				t.Fatalf("state=%s want=%s reason=%s", result.State, tc.want, result.Reason)
			}
			if evidence.State != result.State {
				t.Fatal("admission evidence disagrees with result")
			}
			if !evidence.AdmittedAt.Equal(clock.Effective()) {
				t.Fatal("evidence did not capture effective receipt time")
			}
		})
	}
}

func TestIdentityAdmissionAcceptanceVerifiedEvidence(t *testing.T) {
	p := loadWirePacket(t)
	c := newAdmissionCase(t, p)
	result, evidence := identity.Admit(c.seal, c.frame, c.h, identity.Clock{Now: func() time.Time { return c.now }})
	if result.State != signing.Verified {
		t.Fatalf("valid device rejected: %+v", result)
	}
	if result.Signer == nil {
		t.Fatal("verified result lacks signer")
	}
	if result.Signer.Basis != signing.BasisPerson {
		t.Fatal("device lacks person trust basis")
	}
	if result.Signer.Workspace != c.seal.Header.Workspace {
		t.Fatal("signer has wrong workspace")
	}
	if evidence.Device != c.seal.KeyID {
		t.Fatal("evidence lost device ID")
	}
	if evidence.Epoch != c.h.Epoch {
		t.Fatal("evidence lost admission epoch")
	}
	if evidence.RecordDigest != c.h.RecordDigest {
		t.Fatal("evidence lost admission record")
	}
}
