package pairbook_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

var contractNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func contractKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("pairbook acceptance " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}
func public(key ed25519.PrivateKey) ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }
func pinnedID(key ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func good(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func bad(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("untrusted or invalid record was accepted")
	}
}
func memberFor(owner, node ed25519.PrivateKey, space string) pairbook.SignedMember {
	return pairbook.SignMember(owner, pairbook.Member{Space: space, Node: public(node), Name: "Node", Role: pairbook.RoleMember,
		IssuedAt: contractNow.UnixMilli(), ExpiresAt: contractNow.Add(pairbook.MemberValidity).UnixMilli()})
}
