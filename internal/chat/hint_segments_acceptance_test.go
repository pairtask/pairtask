package chat

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHintSegmentsAcceptanceTruncationPreservesVisibleEllipsis(t *testing.T) {
	for _, texts := range [][]string{{"to Office · ", "→ reviewer", " · later"}, {"to ⇄ 工作 · ", "→ 🧪reviewer", " · later"}} {
		segs := []hintSegment{{texts[0], titleStyle}, {texts[1], everyoneStyle}, {texts[2], mutedStyle}}
		plain := plainSegments(segs)
		for width := 0; width <= ansi.StringWidth(plain)+1; width++ {
			got := ansi.Strip(renderSegments(segs, width))
			want := ansi.Truncate(plain, width, "…")
			if got != want {
				t.Fatalf("audience at width %d = %q, want %q", width, got, want)
			}
			if ansi.StringWidth(got) > width {
				t.Fatalf("audience overflows %d cells: %q", width, got)
			}
		}
	}
}
