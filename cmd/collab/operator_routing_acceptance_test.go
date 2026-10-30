package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorRoutingAcceptanceRepeatedTargetsStayVerbatim(t *testing.T) {
	var stderr bytes.Buffer
	agents := []string{"remote:agent/@\nname", "second"}
	_, req, code := parseOperatorContact([]string{"send", "workspace-key", "question", "--to", agents[0], "--to", agents[1], "--message-id", "stable-id"}, &stderr)
	assertStatusEqual(t, "parsed", code, -1)
	assertStatusEqual(t, "stable ID", req.MessageID, "stable-id")
	assertStatusEqual(t, "operator frame", req.Type, protocol.TypeOperatorContact)
	if !reflect.DeepEqual(req.ContactTo, agents) {
		t.Fatalf("targets changed: %q", req.ContactTo)
	}
}

func TestOperatorRoutingAcceptanceShareAndUnsharePreserveTargets(t *testing.T) {
	for _, op := range []string{"share", "unshare"} {
		for _, agents := range [][]string{{"*"}, {"first", "remote:agent/@\nname"}} {
			var stderr bytes.Buffer
			args := append([]string{op, "workspace-key"}, agents...)
			_, req, code := parseOperatorContact(args, &stderr)
			assertStatusEqual(t, "parsed", code, -1)
			assertStatusEqual(t, "operation", req.ContactOp, op)
			assertStatusEqual(t, "contact", req.ContactKey, "workspace-key")
			if !reflect.DeepEqual(req.ContactAgents, agents) {
				t.Fatalf("%s targets changed: %q", op, req.ContactAgents)
			}
		}
	}
}

func TestOperatorRoutingAcceptanceAllRemoteLabelsAreTerminalSafe(t *testing.T) {
	unsafe := "agent\x1b[2J\rforged\x07\nname"
	var out bytes.Buffer
	writeContact(&out, protocol.Message{ContactPage: &protocol.ContactMessagePage{
		Shared: []string{unsafe}, Directory: []string{unsafe}, Messages: []protocol.ContactMessageView{{From: unsafe, To: []string{unsafe}, AgentAcks: []string{unsafe}}},
	}})
	for _, control := range []rune{'\x1b', '\r', '\x07'} {
		if strings.ContainsRune(out.String(), control) {
			t.Fatalf("remote control %U reached terminal: %q", control, out.String())
		}
	}
	for _, label := range []string{"from=", "to=", "acked=", "shared here:", "they share:"} {
		if !strings.Contains(out.String(), label) {
			t.Fatalf("missing routing display %q", label)
		}
	}
}
