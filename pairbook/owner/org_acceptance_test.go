package owner_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
	"github.com/pairtask/pairtask/pairbook/owner"
)

func TestServiceOwnerVerifiesItsAnchor(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0)
	anchor, err := owner.NewAnchor(key, at)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(anchor)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := owner.New(key, corrupt); err == nil {
		t.Fatal("accepted damaged anchor")
	}
	me, err := owner.New(key, anchor)
	if err != nil {
		t.Fatal(err)
	}
	m, err := owner.NewSpace(me, owner.Genesis{Space: "0123456789abcdef0123456789abcdef", Name: "Company"}, at)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Verify(m.Space, me.KeyID(), pairbook.Mark{}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.People()) != 1 || len(v.Owner().Workspaces) != 0 {
		t.Fatal("genesis did not have a single owner without a bound workspace")
	}
	for _, lineage := range []pairbook.Lineage{v.Owner().Lineage, {1}} {
		_, err := me.Remove(owner.Current{Map: m, Mark: m.Mark()}, lineage, at)
		if !errors.Is(err, owner.ErrNoPerson) {
			t.Fatalf("removal of owner or absent member returned %v", err)
		}
	}
}
