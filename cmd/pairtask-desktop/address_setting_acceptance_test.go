//go:build linux

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopAddressSettingAcceptanceSingleWorkspaceMovesAndSkipsUnchanged(t *testing.T) {
	p := newDesktopPair(t)
	p.redeem(t)
	p.exchange(t)
	before := settingContactView(t, p)
	destination := p.b.alternateAddress(t)
	cfg, err := parseFlags([]string{"--dir", p.dir, "--name", "Desktop A", "--address", destination})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != destination {
		t.Fatalf("Desktop did not accept the human address setting: %+v %v", cfg, err)
	}
	outcomes := p.a.ApplyAddressSetting(context.Background(), cfg.Address)
	requireSettingOutcome(t, outcomes, "moved")
	requireStatusOutcome(t, p, "moved")
	requireSettingContact(t, p, before.IdentityKey, destination)
	history := p.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
		ContactOp: protocol.ContactOpList, ContactKey: p.bIdentity})
	if history.ContactPage == nil || !containsDesktopHistory(history.ContactPage.Messages, "hello from Desktop") {
		t.Fatalf("address setting lost prior messages: %+v", history)
	}
	p.b.stop() // a second application must not probe a contact already at this address
	outcomes = p.a.ApplyAddressSetting(context.Background(), cfg.Address)
	requireSettingOutcome(t, outcomes, "unchanged")
	requireStatusOutcome(t, p, "unchanged")
}

func TestDesktopAddressSettingAcceptanceNamedAndRefusedEntriesKeepContact(t *testing.T) {
	p := newDesktopPair(t)
	p.redeem(t)
	named := p.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
		ContactOp: protocol.ContactOpName, ContactKey: p.bIdentity, ContactText: "Office"})
	if named.Type == protocol.TypeError {
		t.Fatalf("name contact: %s", named.Detail)
	}
	before := settingContactView(t, p)
	if before.LocalName != "Office" {
		t.Fatal("fixture needs a human contact name")
	}
	destination := p.b.alternateAddress(t)
	outcomes := p.a.ApplyAddressSetting(context.Background(), before.LocalName+"="+destination)
	requireSettingOutcome(t, outcomes, "moved")
	other := startFullPeerFixture(t)
	for _, address := range []string{"Unknown=" + other.destination, before.LocalName + "=" + other.destination} {
		refusal := p.a.ApplyAddressSetting(context.Background(), address)
		requireSettingOutcome(t, refusal, "refused")
		requireStatusOutcome(t, p, "refused")
		if refusal[0].Reason == "" {
			t.Fatalf("unmatched or wrong-identity address was not explained: %+v", refusal)
		}
		requireSettingContact(t, p, before.IdentityKey, destination)
	}
}

func TestDesktopAddressSettingAcceptanceForwarderCanApplyChangedSetting(t *testing.T) {
	p := newDesktopPair(t)
	p.redeem(t)
	destination := p.b.alternateAddress(t)
	copy, err := desktop.Start(context.Background(), desktop.Config{Dir: p.dir, Name: "Desktop A", Address: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if copy.Owner() {
		t.Fatal("second Desktop surface unexpectedly owns the node")
	}
	outcomes := copy.ApplyAddressSetting(context.Background(), destination)
	requireSettingOutcome(t, outcomes, "moved")
	requireSettingContact(t, p, p.bIdentity, destination)
}

func TestDesktopAddressSettingAcceptanceParsesSeveralNamedWorkspaces(t *testing.T) {
	entries, err := desktop.ParseAddressSetting("Office=10.10.53.202:7445, Home=home.example:7445")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("named setting was not parsed: %+v %v", entries, err)
	}
	if entries[0].Contact != "Office" || entries[1].Destination != "home.example:7445" {
		t.Fatalf("named entries changed: %+v", entries)
	}
	for _, raw := range []string{"Office=bad-address", "Office=home.example:7445,Office=home.example:7446", "home.example:7445,Office=home.example:7446"} {
		if _, err := desktop.ParseAddressSetting(raw); err == nil {
			t.Fatalf("invalid or ambiguous address setting was accepted: %q", raw)
		}
	}
}

func requireSettingOutcome(t *testing.T, outcomes []desktop.AddressOutcome, want string) {
	t.Helper()
	if len(outcomes) != 1 {
		t.Fatalf("want one %s outcome, got %+v", want, outcomes)
	}
	if outcomes[0].Result != want {
		t.Fatalf("want address setting %s, got %+v", want, outcomes)
	}
}

func requireSettingContact(t *testing.T, p *desktopPair, identity, destination string) {
	t.Helper()
	got := settingContactView(t, p)
	if got.IdentityKey != identity {
		t.Fatalf("address setting changed the paired identity: %+v", got)
	}
	if got.Destination != destination || !got.Confirmed {
		t.Fatalf("address setting changed the contact unexpectedly: %+v", got)
	}
}

func requireStatusOutcome(t *testing.T, p *desktopPair, want string) {
	t.Helper()
	var status desktop.StatusResult
	decodeDesktopMCP(t, desktopTool(t, connectDesktopMCP(t, p.a), "pair_status", map[string]any{}), &status)
	requireSettingOutcome(t, status.AddressSetting, want)
}

func settingContactView(t *testing.T, p *desktopPair) protocol.PairingContactView {
	t.Helper()
	for _, c := range desktopPairStatus(t, p.a).Contacts {
		if c.IdentityKey == p.bIdentity {
			return c
		}
	}
	t.Fatalf("paired workspace %s disappeared", strings.TrimSpace(p.bIdentity))
	return protocol.PairingContactView{}
}
