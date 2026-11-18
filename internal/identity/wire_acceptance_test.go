package identity_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/signing"
)

type wireVector struct {
	Name      string
	Kind      string
	Signer    string `json:"signer_fixture"`
	TBS       string `json:"tbs_hex"`
	Signature string `json:"signature_hex"`
	Wire      string `json:"wire_hex"`
	Digest    string `json:"tbs_digest_hex"`
}

type vectorKey struct {
	Seed   string `json:"public_test_seed_hex"`
	Public string `json:"public_key_hex"`
}

type wirePacket struct {
	Fixtures map[string]vectorKey
	Vectors  []wireVector
}

func loadWirePacket(t *testing.T) wirePacket {
	t.Helper()
	raw, err := os.ReadFile("testdata/wire-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var p wirePacket
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func unhex(t *testing.T, text string) []byte {
	t.Helper()
	b, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func vectorNamed(t *testing.T, p wirePacket, name string) wireVector {
	t.Helper()
	for _, v := range p.Vectors {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("fixture %q missing", name)
	return wireVector{}
}

func parsedVector(t *testing.T, p wirePacket, name string) identity.Statement {
	t.Helper()
	v := vectorNamed(t, p, name)
	st, err := identity.Parse(unhex(t, v.Wire))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func fixturePrivate(t *testing.T, p wirePacket, name string) ed25519.PrivateKey {
	t.Helper()
	return ed25519.NewKeyFromSeed(unhex(t, p.Fixtures[name].Seed))
}

func checkStatement(st identity.Statement, pub ed25519.PublicKey) error {
	if a, ok := st.(identity.Anchor); ok {
		return a.Check()
	}
	return st.(interface{ Check(ed25519.PublicKey) error }).Check(pub)
}

func TestIdentityWireAcceptanceFrozenAnswers(t *testing.T) {
	p := loadWirePacket(t)
	for _, v := range p.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			wire := unhex(t, v.Wire)
			pub := ed25519.PublicKey(unhex(t, p.Fixtures[v.Signer].Public))
			if v.Kind == "DeviceCertificate" {
				cert, err := signing.ParseCert(wire)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(cert.Marshal(), wire) {
					t.Fatal("existing certificate encoding changed")
				}
				return
			}
			st, err := identity.Parse(wire)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(st.TBS(), unhex(t, v.TBS)) {
				t.Fatal("signed bytes differ from independent frozen fixture")
			}
			if !bytes.Equal(st.Marshal(), wire) {
				t.Fatal("wire bytes differ from independent frozen fixture")
			}
			if !bytes.Equal(st.Signature(), unhex(t, v.Signature)) {
				t.Fatal("signature changed during parse")
			}
			if got := identity.DigestOf(st.TBS()).String(); got != v.Digest {
				t.Fatalf("statement digest %s, want %s", got, v.Digest)
			}
			if err := checkStatement(st, pub); err != nil {
				t.Fatalf("independent signature refused: %v", err)
			}
		})
	}
}

func TestIdentityWireAcceptanceSignatureAndEnvelopeMutations(t *testing.T) {
	p := loadWirePacket(t)
	for _, v := range p.Vectors {
		if v.Kind == "DeviceCertificate" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			wire := unhex(t, v.Wire)
			for _, malformed := range [][]byte{append(bytes.Clone(wire), 0), wire[:len(wire)-1]} {
				if _, err := identity.Parse(malformed); err == nil {
					t.Fatal("malformed envelope accepted")
				}
			}
			wire[len(wire)-1] ^= 1
			st, err := identity.Parse(wire)
			if err != nil {
				t.Fatalf("signature bit does not change envelope shape: %v", err)
			}
			pub := ed25519.PublicKey(unhex(t, p.Fixtures[v.Signer].Public))
			if err := checkStatement(st, pub); !errors.Is(err, identity.ErrBadSignature) {
				t.Fatalf("tampered signature result = %v", err)
			}
		})
	}
}

func TestIdentityWireAcceptanceStrictAnchorFlagAndTime(t *testing.T) {
	p := loadWirePacket(t)
	flag := unhex(t, vectorNamed(t, p, "anchor-no-recovery").Wire)
	flagOffset := 4 + 4 + len("pairtask-id-anchor-v1") + 4 + ed25519.PublicKeySize
	binary.BigEndian.PutUint64(flag[flagOffset:flagOffset+8], 2)
	if _, err := identity.ParseAnchor(flag); err == nil {
		t.Error("has_recovery=2 accepted as no recovery")
	}
	a := parsedVector(t, p, "anchor").(identity.Anchor)
	a.InitAt = time.UnixMilli(-1)
	a = a.Signed(fixturePrivate(t, p, "P0"))
	if _, err := identity.ParseAnchor(a.Marshal()); err == nil {
		t.Error("out-of-range unsigned timestamp wrapped to a negative time")
	}
}

func TestIdentityWireAcceptanceCanonicalCollections(t *testing.T) {
	p := loadWirePacket(t)
	person := fixturePrivate(t, p, "P0")
	record := parsedVector(t, p, "record").(identity.Record)
	record.Bindings = append(record.Bindings, record.Bindings[0])
	inv := parsedVector(t, p, "inventory").(identity.Inventory)
	inv.Devices = append(inv.Devices, inv.Devices[0])
	res := parsedVector(t, p, "local-resolution").(identity.Resolution)
	res.Rejected = append(res.Rejected, res.Rejected[0])
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"duplicate binding", record.Signed(person).Marshal()},
		{"duplicate inventory device", inv.Signed(person).Marshal()},
		{"duplicate rejected head", res.Signed(fixturePrivate(t, p, "W0")).Marshal()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := identity.Parse(tc.wire); err == nil {
				t.Fatal("noncanonical duplicate array accepted")
			}
		})
	}
}

func TestIdentityWireAcceptanceBoundedLabelsAndDomains(t *testing.T) {
	p := loadWirePacket(t)
	inv := parsedVector(t, p, "inventory").(identity.Inventory)
	for _, label := range []string{string(bytes.Repeat([]byte{'x'}, identity.MaxLabel+1)), string([]byte{0xff})} {
		inv.Devices[0].Label = label
		inv = inv.Signed(fixturePrivate(t, p, "P0"))
		if _, err := identity.ParseInventory(inv.Marshal()); err == nil {
			t.Errorf("invalid UTF-8 or oversized label accepted: %x", label)
		}
	}
	notice := unhex(t, vectorNamed(t, p, "notice").Wire)
	if _, err := identity.ParseRevocation(notice); err == nil {
		t.Fatal("notice accepted under revocation domain")
	}
}

func TestIdentityWireAcceptanceRejectsDescendingArrays(t *testing.T) {
	p := loadWirePacket(t)
	person := fixturePrivate(t, p, "P0")
	record := parsedVector(t, p, "record").(identity.Record)
	one, two := record.Bindings[0], record.Bindings[0]
	workspace := fixturePrivate(t, p, "P1")
	two.Claim.Workspace = identity.KeyID(workspace.Public().(ed25519.PublicKey))
	two.Acceptance.Workspace = two.Claim.Workspace
	two.Claim = two.Claim.Signed(person)
	two.Acceptance = two.Acceptance.Signed(workspace)
	if bytes.Compare(one.Claim.Marshal(), two.Claim.Marshal()) < 0 {
		one, two = two, one
	}
	record.Bindings = []identity.Binding{one, two}
	if _, err := identity.ParseRecord(record.Signed(person).Marshal()); err == nil {
		t.Error("descending binding array accepted")
	}
	inv := parsedVector(t, p, "inventory").(identity.Inventory)
	d1, d2 := inv.Devices[0], inv.Devices[0]
	d2.ID = identity.KeyID(fixturePrivate(t, p, "D1").Public().(ed25519.PublicKey))
	if d1.ID < d2.ID {
		d1, d2 = d2, d1
	}
	inv.Devices = []identity.Device{d1, d2}
	if _, err := identity.ParseInventory(inv.Signed(person).Marshal()); err == nil {
		t.Error("descending inventory array accepted")
	}
	res := parsedVector(t, p, "local-resolution").(identity.Resolution)
	r1, r2 := res.Rejected[0], identity.DigestOf([]byte("distinct rejected fixture"))
	if bytes.Compare(r1[:], r2[:]) < 0 {
		r1, r2 = r2, r1
	}
	res.Rejected = []identity.Digest{r1, r2}
	if _, err := identity.ParseResolution(res.Signed(fixturePrivate(t, p, "W0")).Marshal()); err == nil {
		t.Error("descending rejected-head array accepted")
	}
}
