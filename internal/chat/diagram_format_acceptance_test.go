package chat

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Model.View has a minimum terminal size. Exercise the underlying transcript
// formatter directly as well, so a narrow diagram cannot overflow its column.
func TestDiagramFormattingNarrowWidthsKeepSourceSlotsAndBytes(t *testing.T) {
	for _, width := range []int{1, 10, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			b := lineBuilder{id: "diagram", src: 17}
			b.formatBody("```mermaid\nABCDEFGHIJKLMN\n```\nafter", width)
			var labelRows, codeRows int
			var source strings.Builder
			for _, row := range b.lines {
				plain := ansi.Strip(row.text)
				if (row.src == 17 || row.src == 18) && ansi.StringWidth(plain) > width {
					t.Errorf("width %d overflows: %q", width, plain)
				}
				switch row.src {
				case 17:
					labelRows++
					if strings.Contains(plain, "/diagram") {
						t.Error("sequence-zero label offers an unusable export command")
					}
				case 18:
					codeRows++
					source.WriteString(strings.TrimLeft(plain, " "))
				case 19:
					t.Error("closing fence must stay hidden")
				case 20:
				default:
					t.Errorf("source slot shifted to %d", row.src)
				}
			}
			if labelRows != 1 {
				t.Errorf("label occupies %d rows, want exactly one", labelRows)
			}
			if codeRows == 0 || source.String() != "ABCDEFGHIJKLMN" {
				t.Errorf("source lost: %q", source.String())
			}
			if b.src != 21 {
				t.Errorf("source accounting = %d, want 21", b.src)
			}
		})
	}
}

func TestDiagramFormattingOnlyClosedRecognizedFencesGetLabels(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"``` Mermaid title\nA\n```\n```PLANTUML\nB\n```", 2},
		{"```mermaid\nA", 0},
		{"```mermaidx\nA\n```", 0},
		{"```go\nA\n```", 0},
		{strings.Repeat("```mermaid\nA\n```\n", 17), 16},
	} {
		b := lineBuilder{id: "diagram"}
		b.formatBody(tc.text, 80)
		labels := 0
		for _, row := range b.lines {
			if strings.Contains(ansi.Strip(row.text), "◇") {
				labels++
			}
		}
		if labels != tc.want {
			t.Errorf("labels=%d want=%d for %q", labels, tc.want, tc.text)
		}
	}
}

func TestDiagramExportDoesNotImportRenderersOrNetwork(t *testing.T) {
	paths, err := filepath.Glob("diagram*.go")
	if err != nil {
		t.Fatal(err)
	}
	production := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		production++
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if name == "os/exec" || name == "net" || strings.HasPrefix(name, "net/") || name == "plugin" {
				t.Errorf("export path imports an external execution/transport package: %s: %s", path, name)
			}
		}
	}
	if production == 0 {
		t.Fatal("diagram export implementation is missing")
	}
}
