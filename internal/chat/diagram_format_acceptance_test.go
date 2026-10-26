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
			var observed diagramLayoutObservation
			for _, row := range b.lines {
				observed.add(t, row, width)
			}
			observed.verify(t)
			if b.src != 21 {
				t.Errorf("source accounting = %d, want 21", b.src)
			}
		})
	}
}

type diagramLayoutObservation struct {
	labelRows, codeRows int
	source              strings.Builder
}

func diagramRowFits(t *testing.T, plain string, width int) {
	t.Helper()
	if ansi.StringWidth(plain) > width {
		t.Errorf("width %d overflows: %q", width, plain)
	}
}

func (o *diagramLayoutObservation) add(t *testing.T, row renderLine, width int) {
	t.Helper()
	plain := ansi.Strip(row.text)
	switch row.src {
	case 17:
		o.labelRows++
		diagramRowFits(t, plain, width)
		if strings.Contains(plain, "/diagram") {
			t.Error("sequence-zero label offers an unusable export command")
		}
	case 18:
		o.codeRows++
		diagramRowFits(t, plain, width)
		o.source.WriteString(strings.TrimLeft(plain, " "))
	case 19:
		t.Error("closing fence must stay hidden")
	case 20:
	default:
		t.Errorf("source slot shifted to %d", row.src)
	}
}

func (o *diagramLayoutObservation) verify(t *testing.T) {
	t.Helper()
	if o.labelRows != 1 {
		t.Errorf("label occupies %d rows, want exactly one", o.labelRows)
	}
	if o.codeRows == 0 {
		t.Error("no source rows")
	}
	if o.source.String() != "ABCDEFGHIJKLMN" {
		t.Errorf("source lost: %q", o.source.String())
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
		diagramCheckImports(t, path)
	}
	if production == 0 {
		t.Fatal("diagram export implementation is missing")
	}
}

func diagramCheckImports(t *testing.T, path string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		name, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if diagramExternalImport(name) {
			t.Errorf("export path imports an external execution/transport package: %s: %s", path, name)
		}
	}
}

func diagramExternalImport(name string) bool {
	switch strings.Split(name, "/")[0] {
	case "net", "plugin":
		return true
	case "os":
		return name == "os/exec"
	default:
		return false
	}
}
