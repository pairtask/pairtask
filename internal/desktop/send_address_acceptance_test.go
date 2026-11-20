package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// This broker boundary records the actual outgoing send, not a resolver helper.
type addressBroker struct {
	contacts    []protocol.PairingContactView
	directories map[string][]string
	mu          sync.Mutex
	sent        []protocol.Message
	refuse      bool
}

func addressNode(t *testing.T, b *addressBroker) *Node {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var req protocol.Message
			if dec.Decode(&req) != nil {
				return
			}
			out := protocol.Message{Type: protocol.TypeOperatorContactResult}
			switch {
			case req.Type == protocol.TypeOperatorPairing:
				out.Type = protocol.TypeOperatorPairingResult
				out.PairingResult = &protocol.PairingSnapshot{Contacts: b.contacts}
			case req.ContactOp == protocol.ContactOpList:
				out.ContactPage = &protocol.ContactMessagePage{Directory: b.directories[req.ContactKey]}
			case req.ContactOp == protocol.ContactOpSend:
				b.mu.Lock()
				b.sent = append(b.sent, req)
				b.mu.Unlock()
				out.ContactMessage = &protocol.ContactMessageView{ID: "sent-id", Text: req.ContactText}
				if b.refuse {
					out = protocol.Message{Type: protocol.TypeError, Detail: "not shared with this agent"}
				}
			default:
				out = protocol.Message{Type: protocol.TypeError, Detail: "unexpected operation in address fixture"}
			}
			if enc.Encode(out) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = client.Close(); <-done })
	return &Node{cfg: Config{Dir: t.TempDir()}, fwd: &forwarder{conn: client, in: bufio.NewScanner(client)}}
}

func addressContacts() []protocol.PairingContactView {
	return []protocol.PairingContactView{
		{IdentityKey: "key-z", Name: "Zulu", LocalName: "Work", Confirmed: true},
		{IdentityKey: "key-a", Name: "Alpha", Confirmed: true},
	}
}

func addressDefaults(contacts []protocol.PairingContactView, dirs map[string][]string) *addressBroker {
	if contacts == nil {
		contacts = addressContacts()
	}
	if dirs == nil {
		dirs = map[string][]string{"key-z": {"alice"}}
	}
	return &addressBroker{contacts: contacts, directories: dirs}
}

func (b *addressBroker) one(t *testing.T, key string, agents []string) protocol.Message {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 {
		t.Fatalf("sends = %d", len(b.sent))
	}
	got := b.sent[0]
	if got.ContactKey != key {
		t.Fatalf("wrong workspace: %+v", got)
	}
	if !reflect.DeepEqual(got.ContactTo, agents) {
		t.Fatalf("wrong agents: %+v", got)
	}
	return got
}

func addressErrorWords(t *testing.T, err error, words []string) {
	t.Helper()
	if err == nil {
		t.Fatal("address was accepted")
	}
	last := -1
	for _, word := range words {
		at := strings.Index(err.Error(), word)
		if at < 0 {
			t.Fatalf("missing %q in %q", word, err)
		}
		if at < last {
			t.Fatalf("expected ordered %q in %q", words, err)
		}
		last = at
	}
}

func TestSendAddressAcceptanceRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, to, key string
		agents, want  []string
	}{
		{name: "legacy local name", to: " Work ", key: "key-z"},
		{name: "legacy remote name", to: "Zulu", key: "key-z", agents: []string{"alice"}, want: []string{"alice"}},
		{name: "legacy key", to: "key-a", key: "key-a"},
		{name: "qualified local", to: " alice @ Work ", key: "key-z", want: []string{"alice"}},
		{name: "qualified remote", to: "alice@Zulu", key: "key-z", want: []string{"alice"}},
		{name: "qualified key", to: "alice@key-z", key: "key-z", want: []string{"alice"}},
		{name: "matching explicit agent", to: "alice@Work", key: "key-z", agents: []string{"alice"}, want: []string{"alice"}},
		{name: "unique bare agent", to: "alice", key: "key-z", want: []string{"alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &addressBroker{contacts: addressContacts(), directories: map[string][]string{"key-z": {"alice"}, "key-a": {"bob"}}}
			n := addressNode(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := n.send(ctx, SendArgs{To: tc.to, Agents: tc.agents, Text: "literal $HOME; unchanged"})
			if err != nil {
				t.Fatal(err)
			}
			got := b.one(t, tc.key, tc.want)
			if got.ContactText != "literal $HOME; unchanged" {
				t.Fatalf("text changed: %q", got.ContactText)
			}
			if result.ID != "sent-id" {
				t.Fatalf("result changed: %+v", result)
			}

		})
	}
}

func TestSendAddressAcceptanceRefusesWithoutSending(t *testing.T) {
	for _, tc := range []struct {
		name, to string
		agents   []string
		contacts []protocol.PairingContactView
		dirs     map[string][]string
		words    []string
	}{
		{name: "unknown lists sorted workspace names", to: "missing", words: []string{"Alpha", "Work"}},
		{name: "qualified absent agent lists sorted directory", to: "missing@Work", dirs: map[string][]string{"key-z": {"zoe", "amy"}}, words: []string{"amy", "zoe"}},
		{name: "conflicting agents", to: "alice@Work", agents: []string{"bob"}, words: []string{"leave agents empty"}},
		{name: "multiple agents", to: "alice@Work", agents: []string{"alice", "bob"}, words: []string{"leave agents empty"}},
		{name: "empty", to: " "}, {name: "missing agent", to: "@Work"}, {name: "missing workspace", to: "alice@"},
		{name: "ambiguous bare", to: "alice", dirs: map[string][]string{"key-z": {"alice"}, "key-a": {"alice"}}, words: []string{"alice@Alpha", "alice@Work"}},
		{name: "ambiguous workspace", to: "Same", contacts: []protocol.PairingContactView{{IdentityKey: "key-z", Name: "Same", Confirmed: true}, {IdentityKey: "key-a", Name: "Same", Confirmed: true}}, words: []string{"key-a", "key-z"}},
		{name: "ambiguous bare duplicate labels", to: "alice", contacts: []protocol.PairingContactView{{IdentityKey: "key-z", Name: "Same", Confirmed: true}, {IdentityKey: "key-a", Name: "Same", Confirmed: true}}, dirs: map[string][]string{"key-z": {"alice"}, "key-a": {"alice"}}, words: []string{"alice@key-a", "alice@key-z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := addressDefaults(tc.contacts, tc.dirs)
			n := addressNode(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := n.send(ctx, SendArgs{To: tc.to, Agents: tc.agents, Text: "never send"})
			addressErrorWords(t, err, tc.words)
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.sent) != 0 {
				t.Fatalf("refusal emitted %d sends", len(b.sent))
			}
		})
	}
}

func TestSendAddressAcceptanceExactWorkspaceWinsIncludingAt(t *testing.T) {
	for _, name := range []string{"alice", "alice@Work"} {
		t.Run(name, func(t *testing.T) {
			b := &addressBroker{contacts: append(addressContacts(), protocol.PairingContactView{IdentityKey: "literal", Name: name, Confirmed: true}), directories: map[string][]string{"key-z": {"alice"}}}
			n := addressNode(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := n.send(ctx, SendArgs{To: name, Text: "operator"}); err != nil {
				t.Fatal(err)
			}
			b.one(t, "literal", nil)
		})
	}
}

func TestSendAddressAcceptanceBareAgentExcludesUnconfirmedAndUnpaired(t *testing.T) {
	now := time.Now()
	contacts := addressContacts()
	contacts = append(contacts, protocol.PairingContactView{IdentityKey: "pending", Name: "Pending"}, protocol.PairingContactView{IdentityKey: "removed", Name: "Removed", Confirmed: true, UnpairedAt: &now})
	b := &addressBroker{contacts: contacts, directories: map[string][]string{"key-z": {"alice"}, "pending": {"alice"}, "removed": {"alice"}}}
	n := addressNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := n.send(ctx, SendArgs{To: "alice", Text: "one eligible"}); err != nil {
		t.Fatal(err)
	}
	b.one(t, "key-z", []string{"alice"})
}

func TestSendAddressAcceptanceEmptyDirectoryDefersAuthorityToBroker(t *testing.T) {
	b := &addressBroker{contacts: addressContacts(), refuse: true}
	n := addressNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := n.send(ctx, SendArgs{To: "alice@Work", Text: "broker decides"})
	addressErrorWords(t, err, []string{"not shared with this agent"})
	b.one(t, "key-z", []string{"alice"})
}

func TestSendAddressAcceptanceAmbiguousSuggestionsResolveAgainstEveryWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contacts []protocol.PairingContactView
		choices  []string
	}{
		{name: "nonsharing duplicate", contacts: []protocol.PairingContactView{
			{IdentityKey: "key-a", Name: "Same", Confirmed: true},
			{IdentityKey: "key-b", Name: "Other", Confirmed: true},
			{IdentityKey: "key-c", Name: "Same", Confirmed: true},
		}, choices: []string{"alice@Other", "alice@key-a"}},
		{name: "at in display name", contacts: []protocol.PairingContactView{
			{IdentityKey: "key-a", Name: "East@Work", Confirmed: true},
			{IdentityKey: "key-b", Name: "West", Confirmed: true},
		}, choices: []string{"alice@West", "alice@key-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := addressDefaults(tc.contacts, map[string][]string{"key-a": {"alice"}, "key-b": {"alice"}, "key-c": {"bob"}})
			n := addressNode(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := n.send(ctx, SendArgs{To: "alice", Text: "ambiguous"})
			addressErrorWords(t, err, tc.choices)
			for _, choice := range tc.choices {
				if _, err := n.send(ctx, SendArgs{To: choice, Text: "chosen"}); err != nil {
					t.Fatalf("suggestion %q cannot be used: %v", choice, err)
				}
			}
		})
	}
}
