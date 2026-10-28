package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/pairtask/pairtask/internal/forge"
)

// Resolve is the pull request's current base and head, as full commit IDs.
func (a *Adapter) Resolve(ctx context.Context, t forge.Target) (forge.Revisions, error) {
	path, err := a.pullPath(t, "")
	if err != nil {
		return forge.Revisions{}, err
	}
	var pull struct {
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := a.get(ctx, path, nil, &pull); err != nil {
		return forge.Revisions{}, err
	}
	if !fullRevision(pull.Base.SHA) || !fullRevision(pull.Head.SHA) {
		return forge.Revisions{}, forge.Errorf(forge.ClassInvalid, "GitHub returned no full base and head commit IDs")
	}
	return forge.Revisions{Base: pull.Base.SHA, Head: pull.Head.SHA}, nil
}

// compareFile is one file of a comparison.
type compareFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

// Changes lists the files the pinned comparison changed and the new-side
// lines a finding may name. GitHub lists at most 300 files on one
// comparison; a comparison at that bound can't be shown complete, so it is
// refused rather than partly trusted.
func (a *Adapter) Changes(ctx context.Context, t forge.Target, r forge.Revisions) ([]forge.FileChange, error) {
	repo, err := a.repo(t)
	if err != nil {
		return nil, err
	}
	if !fullRevision(r.Base) || !fullRevision(r.Head) {
		return nil, forge.Errorf(forge.ClassInvalid, "pinned revisions must be full commit IDs")
	}
	var cmp struct {
		Files []compareFile `json:"files"`
	}
	if err := a.get(ctx, "/repos/"+repo+"/compare/"+r.Base+"..."+r.Head, nil, &cmp); err != nil {
		return nil, err
	}
	if len(cmp.Files) >= compareFiles {
		return nil, forge.Errorf(forge.ClassUnavailable, "the comparison lists %d files or more; GitHub can't show it complete", compareFiles)
	}
	changes := make([]forge.FileChange, 0, len(cmp.Files))
	for _, f := range cmp.Files {
		changes = append(changes, forge.FileChange{Path: f.Filename, Lines: newSideLines(f.Patch)})
	}
	return changes, nil
}

// newSideLines are the new-side lines a patch shows: added and context
// lines, counted hunk by hunk against each header's own counts. A hunk that
// doesn't add up, or a patch that isn't one, authorises no lines at all.
func newSideLines(patch string) []forge.LineRange {
	if patch == "" {
		return nil
	}
	var ranges []forge.LineRange
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	for i := 0; i < len(lines); {
		hunk, next, ok := readHunk(lines, i)
		if !ok {
			return nil
		}
		ranges = append(ranges, hunk...)
		i = next
	}
	return ranges
}

// hunkHeader is one "@@ -a,b +c,d @@" header.
type hunkHeader struct {
	oldCount, newStart, newCount int
}

// readHunk reads the hunk at lines[i]: the ranges of new-side lines it
// shows, and where the next hunk starts. The body must hold exactly the
// header's counts.
func readHunk(lines []string, i int) ([]forge.LineRange, int, bool) {
	h, ok := parseHunkHeader(lines[i])
	if !ok {
		return nil, 0, false
	}
	c := hunkCount{header: h}
	j := i + 1
	for ; j < len(lines) && !strings.HasPrefix(lines[j], "@@"); j++ {
		if !c.add(lines[j]) {
			return nil, 0, false
		}
	}
	if !c.complete() {
		return nil, 0, false // truncated or malformed: nothing from it is trusted
	}
	return c.ranges, j, true
}

// lineKind is how far a patch line moves each side, and whether it shows a
// new-side line.
type lineKind struct {
	old, new int
	shown    bool
}

// lineKinds by a patch line's first byte; "\" is "\ No newline at end of
// file", and an empty line is context whose space was trimmed.
var lineKinds = map[byte]lineKind{
	'\\': {},
	'-':  {old: 1},
	'+':  {new: 1, shown: true},
	' ':  {old: 1, new: 1, shown: true},
}

// hunkCount counts one hunk's lines against its header.
type hunkCount struct {
	header           hunkHeader
	oldSeen, newSeen int
	ranges           []forge.LineRange
}

// add counts one line; a line of no known kind fails the hunk.
func (c *hunkCount) add(line string) bool {
	kind, ok := lineKinds[' ']
	if line != "" {
		kind, ok = lineKinds[line[0]]
	}
	if !ok {
		return false
	}
	if kind.shown {
		c.ranges = addLine(c.ranges, c.header.newStart+c.newSeen)
	}
	c.oldSeen, c.newSeen = c.oldSeen+kind.old, c.newSeen+kind.new
	return true
}

func (c *hunkCount) complete() bool {
	return c.oldSeen == c.header.oldCount && c.newSeen == c.header.newCount
}

func addLine(ranges []forge.LineRange, line int) []forge.LineRange {
	if n := len(ranges); n > 0 && ranges[n-1].To == line-1 {
		ranges[n-1].To = line
		return ranges
	}
	return append(ranges, forge.LineRange{From: line, To: line})
}

// parseHunkHeader reads "@@ -a[,b] +c[,d] @@ …"; a missing count is 1.
func parseHunkHeader(line string) (hunkHeader, bool) {
	rest, ok := strings.CutPrefix(line, "@@ -")
	if !ok {
		return hunkHeader{}, false
	}
	spec, _, ok := strings.Cut(rest, " @@")
	if !ok {
		return hunkHeader{}, false
	}
	oldPart, newPart, ok := strings.Cut(spec, " +")
	if !ok {
		return hunkHeader{}, false
	}
	_, oldCount, okOld := startCount(oldPart)
	newStart, newCount, okNew := startCount(newPart)
	if !okOld || !okNew {
		return hunkHeader{}, false
	}
	return hunkHeader{oldCount: oldCount, newStart: newStart, newCount: newCount}, true
}

func startCount(s string) (int, int, bool) {
	startText, countText, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if !hasCount {
		return start, 1, true
	}
	count, err := strconv.Atoi(countText)
	return start, count, err == nil && count >= 0
}
