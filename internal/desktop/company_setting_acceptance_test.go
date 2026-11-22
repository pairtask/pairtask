package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/pairtask/pairtask/internal/space"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompanyJoinGuidanceLeavesIdentityUncreated(t *testing.T) {
	dir := t.TempDir()
	n := &Node{cfg: Config{Dir: dir, Pairbook: "https://company.example/"}}
	client := desktopSpaceMCP(t, n)
	res := desktopSpaceTool(t, client, "pairbook_join", map[string]any{})
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out PairbookJoinResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Guidance, "https://company.example/join") {
		t.Fatal(out.Guidance)
	}
	other := space.OrgLink{Base: "https://other.example", Space: strings.Repeat("a", 32), Owner: "sha256:" + strings.Repeat("b", 64)}
	if _, err := n.pairbookJoin(context.Background(), PairbookJoinArgs{Link: other.String()}); err == nil {
		t.Fatal("accepted a link for a different configured company")
	}
	_, err = os.Stat(filepath.Join(dir, "person"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected person directory: %v", err)
	}
}

func TestCompanyPersonIsReusedAfterDesktopRestart(t *testing.T) {
	cfg := Config{Dir: t.TempDir()}
	first, err := (&Node{cfg: cfg}).person()
	if err != nil {
		t.Fatal(err)
	}
	next, err := (&Node{cfg: cfg}).person()
	if err != nil {
		t.Fatal(err)
	}
	if first.KeyID() != next.KeyID() || first.Lineage != next.Lineage {
		t.Fatal("restart changed the enrolled person")
	}
	info, err := os.Stat(filepath.Join(cfg.Dir, "person"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("person directory permissions: %v", info.Mode())
	}
}
