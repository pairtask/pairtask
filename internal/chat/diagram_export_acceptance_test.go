package chat_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func diagramStateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func diagramPath(base, source, ext string, index int) string {
	sum := sha256.Sum256([]byte(source))
	return filepath.Join(base, "pairtask", "diagrams", fmt.Sprintf("7-%d-%x.%s", index, sum[:6], ext))
}

// Run only the returned filesystem command, separately from Update, as the UI does.
func diagramCommand(h *chatHarness, command string) string {
	h.t.Helper()
	if h.m.State().CommandLine != nil {
		h.input(chatKey(tea.KeyEscape, 0))
	}
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, strings.TrimPrefix(command, "/"))
	cmd := h.update(chatKey(tea.KeyEnter, 0))
	if cmd != nil {
		h.input(cmd())
	}
	return h.m.State().Status
}

func diagramRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func diagramWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func diagramMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func diagramSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func diagramNoExport(t *testing.T, base string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(base, "pairtask", "diagrams", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("unexpected exports: %v, %v", files, err)
	}
}

func TestChatDiagramExportRecognitionAndExactSource(t *testing.T) {
	for _, tc := range []struct{ name, text, source, ext string }{
		{"mermaid", "```mermaid\nflowchart LR\n  A --> B\n```", "flowchart LR\n  A --> B", "mmd"},
		{"case and info", "  ```  MeRmAiD title\nA --> B\n  ```", "A --> B", "mmd"},
		{"plantuml crlf", "```PLANTUML\r\n@startuml\r\nAlice -> Bob\r\n@enduml\r\n```", "@startuml\nAlice -> Bob\n@enduml", "puml"},
		{"blank source line", "```mermaid\nA\n\n```", "A\n", "mmd"},
		{"empty", "```mermaid\n```", "", "mmd"},
		{"fence toggles", "```mermaid\nA\n```plantuml\nB\n```", "A", "mmd"},
		{"uninterpreted directives", "```plantuml\n!include /etc/passwd\n!includeurl https://example.invalid/x\n' $(touch marker)\n```", "!include /etc/passwd\n!includeurl https://example.invalid/x\n' $(touch marker)", "puml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := diagramStateHome(t)
			h := formattingHarness(t, tc.text, 80, 30)
			path := diagramPath(base, tc.source, tc.ext, 1)
			chatEqual(t, "saved path", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
			chatEqual(t, "exact source plus one newline", string(diagramRead(t, path)), tc.source+"\n")
			if len(h.requests) != 0 || len(h.wire.writes) != 0 {
				t.Fatal("export sent a broker request")
			}
		})
	}
}

func TestChatDiagramExportRejectsNonDiagrams(t *testing.T) {
	for _, text := range []string{"plain", "```go\nA\n```", "```mermaidx\nA\n```", "```mermaid\nA", "~~~mermaid\nA\n~~~"} {
		t.Run(text, func(t *testing.T) {
			base := diagramStateHome(t)
			h := formattingHarness(t, text, 80, 30)
			chatEqual(t, "not a diagram", diagramCommand(h, "/diagram #7"), "#7 has no diagram")
			diagramNoExport(t, base)
		})
	}
}

func TestChatDiagramExportSelectsBlocksAndCapsAtSixteen(t *testing.T) {
	base := diagramStateHome(t)
	var text strings.Builder
	for i := 1; i <= 17; i++ {
		fmt.Fprintf(&text, "```mermaid\nblock%d\n```\n", i)
	}
	h := formattingHarness(t, text.String(), 80, 100)
	chatEqual(t, "index required", diagramCommand(h, "/diagram #7"), "#7 has 16 diagrams: /diagram #7 K")
	diagramNoExport(t, base)
	chatEqual(t, "out of range", diagramCommand(h, "/diagram #7 17"), "#7 has 16 diagrams")
	diagramNoExport(t, base)
	path := diagramPath(base, "block16", "mmd", 16)
	chatEqual(t, "select last retained diagram", diagramCommand(h, "/diagram #7 16"), "diagram saved: "+path)
	chatEqual(t, "selected bytes", string(diagramRead(t, path)), "block16\n")
}

func TestChatDiagramExportMixedBlocksUseSourceOrder(t *testing.T) {
	base := diagramStateHome(t)
	h := formattingHarness(t, "```go\nignored\n```\n```mermaid\nfirst\n```\n```plantuml\nsecond\n```", 80, 30)
	path := diagramPath(base, "second", "puml", 2)
	chatEqual(t, "second recognized block", diagramCommand(h, "/diagram #7 2"), "diagram saved: "+path)
	chatEqual(t, "plantuml bytes", string(diagramRead(t, path)), "second\n")
}

func TestChatDiagramExportPermissionsAndIdempotence(t *testing.T) {
	base := diagramStateHome(t)
	h := formattingHarness(t, "```mermaid\nA --> B\n```", 80, 30)
	path := diagramPath(base, "A --> B", "mmd", 1)
	chatEqual(t, "first export", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
	for _, p := range []string{filepath.Join(base, "pairtask"), filepath.Dir(path), path} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0700)
		if p == path {
			want = 0600
		}
		chatEqual(t, "private permissions", info.Mode().Perm(), want)
	}
	before, _ := os.Stat(path)
	chatEqual(t, "repeat export", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
	after, _ := os.Stat(path)
	chatEqual(t, "same inode reused", os.SameFile(before, after), true)
	chatEqual(t, "no timestamp rewrite", before.ModTime(), after.ModTime())
	chatEqual(t, "repeat bytes", string(diagramRead(t, path)), "A --> B\n")
}

func TestChatDiagramExportRunsOutsideUpdate(t *testing.T) {
	base := diagramStateHome(t)
	h := formattingHarness(t, "```mermaid\nA\n```", 80, 30)
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "diagram #7")
	cmd := h.update(chatKey(tea.KeyEnter, 0))
	diagramNoExport(t, base)
	if cmd == nil {
		t.Fatal("diagram export must return a filesystem command")
	}
	h.input(cmd())
	path := diagramPath(base, "A", "mmd", 1)
	chatEqual(t, "command creates export", string(diagramRead(t, path)), "A\n")
}

func diagramExistingObject(t *testing.T, kind, path, target string) {
	t.Helper()
	switch kind {
	case "different file":
		diagramWrite(t, path, "keep me")
	case "large file":
		diagramWrite(t, path, strings.Repeat("x", 300*1024))
	case "symlink":
		diagramSymlink(t, target, path)
	case "directory":
		diagramMkdir(t, path)
	case "fifo":
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func diagramLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestChatDiagramExportNeverOverwritesExistingObjects(t *testing.T) {
	for _, kind := range []string{"different file", "large file", "symlink", "directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			base := diagramStateHome(t)
			path := diagramPath(base, "A", "mmd", 1)
			diagramMkdir(t, filepath.Dir(path))
			target := filepath.Join(base, "target")
			diagramWrite(t, target, "A\n")
			diagramExistingObject(t, kind, path, target)
			before := diagramLstat(t, path)
			h := formattingHarness(t, "```mermaid\nA\n```", 80, 30)
			status := diagramCommand(h, "/diagram #7")
			if !strings.Contains(status, "diagram not saved:") {
				t.Fatalf("unsafe target accepted: %q", status)
			}
			after := diagramLstat(t, path)
			chatEqual(t, "target inode preserved", os.SameFile(before, after), true)
			chatEqual(t, "target mode preserved", before.Mode(), after.Mode())
			chatEqual(t, "target size preserved", before.Size(), after.Size())
			chatEqual(t, "outside target untouched", string(diagramRead(t, target)), "A\n")
			if kind == "different file" {
				chatEqual(t, "existing bytes", string(diagramRead(t, path)), "keep me")
			}
		})
	}
}

func diagramUnsafeComponent(t *testing.T, kind, outside, path string) {
	t.Helper()
	if kind == "symlink" {
		diagramSymlink(t, outside, path)
		return
	}
	diagramWrite(t, path, "unchanged")
}

func diagramEmptyDirectory(t *testing.T, path string) {
	t.Helper()
	files, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("unexpected files through directory symlink: %v", files)
	}
}

func TestChatDiagramExportRefusesUnsafeDirectoryComponents(t *testing.T) {
	for _, component := range []string{"pairtask", "pairtask/diagrams"} {
		for _, kind := range []string{"symlink", "file"} {
			t.Run(component+"/"+kind, func(t *testing.T) {
				base := diagramStateHome(t)
				path := filepath.Join(base, component)
				diagramMkdir(t, filepath.Dir(path))
				outside := t.TempDir()
				diagramUnsafeComponent(t, kind, outside, path)
				h := formattingHarness(t, "```mermaid\nA\n```", 80, 30)
				if status := diagramCommand(h, "/diagram #7"); !strings.Contains(status, "diagram not saved:") {
					t.Fatalf("unsafe directory accepted: %q", status)
				}
				diagramEmptyDirectory(t, outside)
				if kind == "file" {
					chatEqual(t, "blocking file unchanged", string(diagramRead(t, path)), "unchanged")
				}
			})
		}
	}
}

func TestChatDiagramExportSourceSizeBoundary(t *testing.T) {
	for _, size := range []int{256 * 1024, 256*1024 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			base := diagramStateHome(t)
			source := strings.Repeat("x", size)
			h := formattingHarness(t, "```mermaid\n"+source+"\n```", 80, 30)
			status := diagramCommand(h, "/diagram #7")
			if size > 256*1024 {
				if !strings.Contains(status, "diagram too large") {
					t.Fatalf("size limit not enforced: %q", status)
				}
				diagramNoExport(t, base)
			} else {
				path := diagramPath(base, source, "mmd", 1)
				chatEqual(t, "exact limit allowed", status, "diagram saved: "+path)
				chatEqual(t, "limit plus newline", len(diagramRead(t, path)), size+1)
			}
		})
	}
}

func TestChatDiagramExportHomeFallbackAndInvalidTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	h := formattingHarness(t, "```mermaid\nA\n```", 80, 30)
	for _, command := range []string{"/diagram #999", "/diagram nope", "/diagram #7 -1", "/diagram #7 1 extra"} {
		status := diagramCommand(h, command)
		if strings.HasPrefix(status, "diagram saved:") {
			t.Fatalf("invalid target exported: %q", command)
		}
		diagramNoExport(t, filepath.Join(home, ".local", "state"))
	}
	path := diagramPath(filepath.Join(home, ".local", "state"), "A", "mmd", 1)
	chatEqual(t, "HOME fallback", diagramCommand(h, "/diagram #7"), "diagram saved: "+path)
	chatEqual(t, "fallback bytes", string(diagramRead(t, path)), "A\n")
}

func TestChatDiagramExportReportsAnAbsolutePath(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("XDG_STATE_HOME", "state")
	h := formattingHarness(t, "```mermaid\nA\n```", 80, 30)
	status := diagramCommand(h, "/diagram #7")
	if !strings.HasPrefix(status, "diagram saved: ") {
		t.Fatalf("export failed: %q", status)
	}
	path := strings.TrimPrefix(status, "diagram saved: ")
	if !filepath.IsAbs(path) {
		t.Fatalf("operator cannot locate relative export: %q", path)
	}
	chatEqual(t, "reported path resolves to source", string(diagramRead(t, path)), "A\n")
}
