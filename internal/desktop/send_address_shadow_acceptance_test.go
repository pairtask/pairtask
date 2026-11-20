package desktop

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func shadowBook(extra ...protocol.PairingContactView) *addressBroker {
	contacts := []protocol.PairingContactView{
		{IdentityKey: "key-a", Name: "East@Work", Confirmed: true},
		{IdentityKey: "key-b", Name: "West", Confirmed: true},
		{IdentityKey: "key-c", Name: "alice@key-a", Confirmed: true},
	}
	return addressDefaults(append(contacts, extra...), map[string][]string{"key-a": {"alice"}, "key-b": {"alice"}, "key-c": {"bob"}, "key-d": {"bob"}})
}

func explicitShadowChoice(t *testing.T, err error) SendArgs {
	t.Helper()
	if err == nil {
		t.Fatal("ambiguous address was accepted")
	}
	for _, choice := range regexp.MustCompile(`\{[^{}]*\}`).FindAllString(err.Error(), -1) {
		var args SendArgs
		if json.Unmarshal([]byte(choice), &args) != nil {
			continue
		}
		if args.To == "key-a" {
			return args
		}
	}
	t.Fatalf("no explicit send parameters for shadowed qualified key: %s", err)
	return SendArgs{}
}

func TestSendAddressShadowAcceptanceSuggestedChoiceCannotBeInterceptedByAWorkspaceName(t *testing.T) {
	b := shadowBook()
	n := addressNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := n.send(ctx, SendArgs{To: "alice", Text: "ambiguous"})
	choice := explicitShadowChoice(t, err)
	choice.Text = "chosen"
	if _, err = n.send(ctx, choice); err != nil {
		t.Fatal(err)
	}
	b.one(t, "key-a", []string{"alice"})
}

func TestSendAddressShadowAcceptanceUnaddressableKeySaysToRenameTheCollision(t *testing.T) {
	b := shadowBook(protocol.PairingContactView{IdentityKey: "key-d", Name: "key-a", Confirmed: true})
	n := addressNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := n.send(ctx, SendArgs{To: "alice", Text: "ambiguous"})
	if err == nil {
		t.Fatal("ambiguous address was accepted")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rename") {
		t.Fatalf("unusable identity key offered as a route: %s", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 0 {
		t.Fatal("ambiguity sent a message")
	}
}
