package chat_test

import (
	"strings"
	"testing"
)

func TestUnpairChatRepairAcceptanceInviteAndRedeemDeclareResolvedIdentity(t *testing.T) {
	for _, op := range []string{"invite", "redeem"} {
		h, c := unpairChatHarness(t, unpairChatOptions{supported: true})
		command := "/pair " + op + " --repair Office"
		target := "open"
		if op == "redeem" {
			command += " 739-281@host:443"
			target = "redeem"
		}
		v2Command(h, command)
		req := pairingChatRequest(h, target)
		chatEqual(t, "repair binds full identity", req.PairingRepair, c.IdentityKey)
		if op == "redeem" {
			chatEqual(t, "repair destination", req.PairingDestination, "host:443")
			chatEqual(t, "repair secret", strings.ReplaceAll(req.PairingCode, "-", ""), "739281")
		}
	}
}
func TestUnpairChatRepairAcceptanceUnsupportedAndAmbiguousNeverMutate(t *testing.T) {
	h, _ := unpairChatHarness(t, unpairChatOptions{})
	v2Command(h, "/pair invite --repair Office")
	unpairChatNoRequest(t, h)
	h, c := unpairChatHarness(t, unpairChatOptions{supported: true})
	other := c
	other.IdentityKey = "sha256:" + strings.Repeat("b", 64)
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	reply := unpairChatSnapshot(t, c)
	reply.PairingResult.Contacts = append(reply.PairingResult.Contacts, other)
	reply.PairingResult.ContactsTotal = 2
	h.reply(req, reply)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, command := range []string{"/pair invite --repair Office", "/pair unpair Office"} {
		v2Command(h, command)
		unpairChatNoRequest(t, h)
	}
}
