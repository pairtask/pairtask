package desktop

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

type desktopSpaceDirectory struct {
	space.Directory
	joined  space.Invite
	name    string
	members []pairbook.Member
}

func (f *desktopSpaceDirectory) Join(_ context.Context, inv space.Invite, name string) (space.Pin, error) {
	f.joined = inv
	f.name = name
	return space.Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base, JoinedAt: time.Now().UTC()}, nil
}
func (f *desktopSpaceDirectory) Members(context.Context, space.Pin) ([]pairbook.Member, error) {
	return f.members, nil
}
func desktopSpaceMCP(t *testing.T, n *Node) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a, b := mcp.NewInMemoryTransports()
	ss, err := NewServer(n, "test").Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "space-acceptance"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
func desktopSpaceTool(t *testing.T, c *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %+v", name, result.Content)
	}
	return result
}
func TestDesktopSpaceAcceptanceToolsJoinAndListWithoutContact(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "desktop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &desktopSpaceDirectory{}
	h := hub.New(st, 0, slog.New(slog.DiscardHandler))
	h.SetSpaces(hub.SpaceConfig{Directory: func(string, crypto.Signer) (space.Directory, error) { return f, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done() })
	n := &Node{own: &owner{hub: h}}
	client := desktopSpaceMCP(t, n)
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		seen[tool.Name] = true
	}
	if !seen["space_join"] || !seen["space_members"] {
		t.Fatal("space tools not advertised")
	}
	inv := space.Invite{Base: "https://directory.example", Space: "0123456789abcdef0123456789abcdef", Owner: "sha256:" + strings.Repeat("a", 64)}
	out := desktopSpaceTool(t, client, "space_join", map[string]any{"link": inv.String(), "name": "My Desktop"})
	encoded, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var joined SpaceJoinResult
	if err := json.Unmarshal(encoded, &joined); err != nil {
		t.Fatal(err)
	}
	if f.joined != inv || f.name != "My Desktop" || joined.Space.Space != inv.Space || joined.Space.Owner != inv.Owner {
		t.Fatalf("tool lost join arguments/pin: %+v", joined)
	}
	if strings.Contains(string(encoded), "token=") {
		t.Fatal("tool result echoed invite secret")
	}
	seed := sha256.Sum256([]byte("desktop space member"))
	key := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	f.members = []pairbook.Member{{Space: inv.Space, Node: key, Name: "Alice", Role: pairbook.RoleMember, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}
	result := desktopSpaceTool(t, client, "space_members", map[string]any{"space": inv.Space})
	data, _ := json.Marshal(result.StructuredContent)
	var members SpaceMembersResult
	if err := json.Unmarshal(data, &members); err != nil {
		t.Fatal(err)
	}
	if len(members.Members) != 1 || members.Members[0].Node != pairbook.KeyID(key) {
		t.Fatalf("tool lost verified member: %+v", members)
	}
	contacts, err := st.Contacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 0 {
		t.Fatal("joining a space automatically created contacts")
	}
}
