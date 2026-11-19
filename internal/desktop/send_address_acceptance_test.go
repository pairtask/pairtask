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
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.sent) != 1 {
				t.Fatalf("sends = %d", len(b.sent))
			}
			got := b.sent[0]
			if got.ContactKey != tc.key || !reflect.DeepEqual(got.ContactTo, tc.want) || got.ContactText != "literal $HOME; unchanged" || result.ID != "sent-id" {
				t.Fatalf("wrong routing/result: %+v %+v", got, result)
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
			if tc.contacts == nil {
				tc.contacts = addressContacts()
			}
			if tc.dirs == nil {
				tc.dirs = map[string][]string{"key-z": {"alice"}}
			}
			b := &addressBroker{contacts: tc.contacts, directories: tc.dirs}
			n := addressNode(t, b)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := n.send(ctx, SendArgs{To: tc.to, Agents: tc.agents, Text: "never send"})
			if err == nil {
				t.Fatal("address was accepted")
			}
			last := -1
			for _, word := range tc.words {
				at := strings.Index(err.Error(), word)
				if at < 0 || at < last {
					t.Fatalf("expected ordered %q in %q", tc.words, err)
				}
				last = at
			}
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
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.sent) != 1 || b.sent[0].ContactKey != "literal" || len(b.sent[0].ContactTo) != 0 {
				t.Fatalf("exact workspace lost precedence: %+v", b.sent)
			}
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 || b.sent[0].ContactKey != "key-z" {
		t.Fatalf("ineligible match affected routing: %+v", b.sent)
	}
}

func TestSendAddressAcceptanceEmptyDirectoryDefersAuthorityToBroker(t *testing.T) {
	b := &addressBroker{contacts: addressContacts(), refuse: true}
	n := addressNode(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := n.send(ctx, SendArgs{To: "alice@Work", Text: "broker decides"})
	if err == nil || !strings.Contains(err.Error(), "not shared with this agent") {
		t.Fatalf("broker refusal lost: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 || b.sent[0].ContactKey != "key-z" || !reflect.DeepEqual(b.sent[0].ContactTo, []string{"alice"}) {
		t.Fatalf("wrong request: %+v", b.sent)
	}
}
