package desktop

import (
	"context"
	"crypto"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
)

// spacesNode runs a node over the store at path, with f as every space's
// directory, until the test ends.
func spacesNode(t *testing.T, path string, f *desktopSpaceDirectory) *Node {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(st, 0, slog.New(slog.DiscardHandler))
	h.SetSpaces(hub.SpaceConfig{Directory: func(string, crypto.Signer) (space.Directory, error) { return f, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done(); _ = st.Close() })
	return &Node{own: &owner{hub: h}}
}

func listedSpaces(t *testing.T, n *Node) PairbookSpacesResult {
	t.Helper()
	out := desktopSpaceTool(t, desktopSpaceMCP(t, n), "pairbook_spaces", map[string]any{})
	data, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got PairbookSpacesResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPairbookSpacesListsJoinedSpacesAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.db")
	f := &desktopSpaceDirectory{}
	first := spacesNode(t, path, f)
	if got := listedSpaces(t, first); len(got.Spaces) != 0 {
		t.Fatalf("a node in no space lists %+v", got.Spaces)
	}
	inv := space.Invite{Base: "https://directory.example", Space: "0123456789abcdef0123456789abcdef", Owner: "sha256:" + strings.Repeat("a", 64)}
	desktopSpaceTool(t, desktopSpaceMCP(t, first), "space_join", map[string]any{"link": inv.String(), "name": "Desktop"})
	if got := listedSpaces(t, first); len(got.Spaces) != 1 || got.Spaces[0].Space != inv.Space {
		t.Fatalf("after joining, spaces are %+v", got.Spaces)
	}
	// A node started again over the same store, with no join in progress,
	// still lists the space.
	again := spacesNode(t, path, f)
	got := listedSpaces(t, again)
	if len(got.Spaces) != 1 || got.Spaces[0].Space != inv.Space || got.Spaces[0].Directory != inv.Base {
		t.Fatalf("after a restart, spaces are %+v", got.Spaces)
	}
}
