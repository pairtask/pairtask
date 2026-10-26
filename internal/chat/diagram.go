package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"
)

// A closed fenced block whose info string starts with a diagram language is a
// diagram: it renders as code under a label, and /diagram exports its source
// to a file. Nothing renders or runs it, and nothing leaves the host.

const (
	maxDiagrams     = 16        // per message; later blocks stay plain code
	maxDiagramBytes = 256 << 10 // source, before the trailing newline
)

var diagramExt = map[string]string{"mermaid": "mmd", "plantuml": "puml"}

type diagram struct {
	open   int // line index of the opening fence
	lang   string
	source string
}

// isFence is the formatter's fence toggle.
func isFence(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "```") }

// fenceLang is the lowercased first word of a fence's info string.
func fenceLang(line string) string {
	fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(line), "`"))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

// findDiagrams lists a message's diagrams in source order, from lines already
// split on normalized newlines.
func findDiagrams(lines []string) []diagram {
	var out []diagram
	open := -1
	for i, line := range lines {
		switch {
		case !isFence(line):
		case open < 0:
			open = i
		default:
			if _, ok := diagramExt[fenceLang(lines[open])]; ok && len(out) < maxDiagrams {
				out = append(out, diagram{open: open, lang: fenceLang(lines[open]), source: strings.Join(lines[open+1:i], "\n")})
			}
			open = -1
		}
	}
	return out
}

func bodyLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

// diagramRef names diagram k (1-based; 0 for "the only one") of message seq.
type diagramRef struct {
	seq uint64
	k   int
}

// command is the /diagram call shown in a label; a message without a number
// can't be targeted, so it gets none.
func (r diagramRef) command(n int) string {
	switch {
	case r.seq == 0:
		return ""
	case n > 1:
		return fmt.Sprintf(" · /diagram #%d %d", r.seq, r.k)
	}
	return fmt.Sprintf(" · /diagram #%d", r.seq)
}

// diagramLabels maps each diagram's opening fence line to the row shown in
// its place.
func (b *lineBuilder) diagramLabels(lines []string) map[int]string {
	ds := findDiagrams(lines)
	labels := make(map[int]string, len(ds))
	for i, d := range ds {
		labels[d.open] = "◇ " + d.lang + diagramRef{seq: b.seq, k: i + 1}.command(len(ds))
	}
	return labels
}

// diagramArg is "#n" with an optional diagram number K.
func diagramArg(a argument) error {
	_, err := a.diagramTarget()
	return err
}

func (a argument) diagramTarget() (diagramRef, error) {
	head, rest, _ := strings.Cut(strings.TrimSpace(string(a)), " ")
	seq, err := argument(head).target()
	if err != nil {
		return diagramRef{}, err
	}
	if rest = strings.TrimSpace(rest); rest == "" {
		return diagramRef{seq: seq}, nil
	}
	k, err := strconv.Atoi(rest)
	if err != nil || k < 1 {
		return diagramRef{}, errors.New("usage: /diagram #n [K]")
	}
	return diagramRef{seq: seq, k: k}, nil
}

func (m *Model) diagramCommand(arg argument) tea.Cmd {
	ref, _ := arg.diagramTarget() // diagramArg already checked it
	e, reason := m.entryForSeq(ref.seq)
	if e != nil && e.omitted {
		e, reason = nil, "that message's text isn't available"
	}
	if e == nil {
		m.status = reason
		return nil
	}
	d, reason := ref.pick(findDiagrams(bodyLines(payloadText(e.msg.Payload))))
	if reason != "" {
		m.status = reason
		return nil
	}
	m.status = fmt.Sprintf("saving the diagram of #%d…", ref.seq)
	x := diagramExport{dir: diagramDir(), ref: ref, d: d}
	return func() tea.Msg {
		path, err := x.save()
		return diagramSavedMsg{path: path, err: err}
	}
}

// diagramSavedMsg reports an export done outside Update, so disk latency
// never stalls input.
type diagramSavedMsg struct {
	path string
	err  error
}

func (m *Model) diagramSaved(msg diagramSavedMsg) {
	if msg.err != nil {
		m.status = "diagram not saved: " + safe(msg.err.Error())
		return
	}
	m.status = "diagram saved: " + safe(msg.path)
}

// pick selects diagram k of ds; k 0 means the only one.
func (r diagramRef) pick(ds []diagram) (diagram, string) {
	seq, k := r.seq, r.k
	switch {
	case len(ds) == 0:
		return diagram{}, fmt.Sprintf("#%d has no diagram", seq)
	case k == 0 && len(ds) > 1:
		return diagram{}, fmt.Sprintf("#%d has %d diagrams: /diagram #%d K", seq, len(ds), seq)
	case k == 0:
		return ds[0], ""
	case k > len(ds):
		return diagram{}, fmt.Sprintf("#%d has %d diagrams", seq, len(ds))
	}
	return ds[k-1], ""
}

// diagramDir is $XDG_STATE_HOME/pairtask/diagrams, by default under
// ~/.local/state; it is made absolute so the reported path stays usable.
func diagramDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	return filepath.Join(base, "pairtask", "diagrams")
}

// diagramExport is one diagram to save as dir/<seq>-<k>-<hash>.<ext>; no
// byte of the name comes from the message text itself.
type diagramExport struct {
	dir string
	ref diagramRef
	d   diagram
}

func (x diagramExport) name() string {
	sum := sha256.Sum256([]byte(x.d.source))
	return fmt.Sprintf("%d-%d-%s.%s", x.ref.seq, max(1, x.ref.k), hex.EncodeToString(sum[:])[:12], diagramExt[x.d.lang])
}

// save writes the source plus one newline; it never replaces or follows
// anything, and an identical existing file is reused.
func (x diagramExport) save() (string, error) {
	if len(x.d.source) > maxDiagramBytes {
		return "", fmt.Errorf("diagram too large (over %d KiB)", maxDiagramBytes>>10)
	}
	dir, err := openDiagramDir(x.dir)
	if err != nil {
		return "", err
	}
	defer dir.close()
	name := x.name()
	return filepath.Join(x.dir, name), dir.writeOnce(name, []byte(x.d.source+"\n"))
}

// dirFD is an open directory; everything below it is reached relative to it.
type dirFD int

func (d dirFD) close() { _ = unix.Close(int(d)) }

// openDiagramDir creates the base as needed, then opens its pairtask and
// diagrams components relative to their parent without following links, so
// a component can't be swapped for a link between the check and the use.
func openDiagramDir(path string) (dirFD, error) {
	base := filepath.Dir(filepath.Dir(path))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return -1, err
	}
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: base, Err: err}
	}
	dir := dirFD(fd)
	for _, name := range []string{filepath.Base(filepath.Dir(path)), filepath.Base(path)} {
		next, err := dir.child(name)
		dir.close()
		if err != nil {
			return -1, err
		}
		dir = next
	}
	return dir, nil
}

// child creates (0700) and opens a subdirectory, refusing a link.
func (d dirFD) child(name string) (dirFD, error) {
	if err := unix.Mkdirat(int(d), name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create %s: %w", name, err)
	}
	fd, err := unix.Openat(int(d), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return -1, fmt.Errorf("unsafe directory: %s is a link or not a directory", name)
	}
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", name, err)
	}
	return dirFD(fd), nil
}

// writeOnce creates name exclusively; when it exists, it must be a regular
// file holding exactly content, and it is left untouched either way.
func (d dirFD) writeOnce(name string, content []byte) error {
	fd, err := unix.Openat(int(d), name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return d.sameFile(name, content)
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	_, err = f.Write(content)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = unix.Unlinkat(int(d), name, 0)
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// sameFile compares an existing file without following a link or blocking
// on a FIFO, reading at most one byte past content.
func (d dirFD) sameFile(name string, content []byte) error {
	fd, err := unix.Openat(int(d), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("unsafe file: %s exists and can't be read safely", name)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe file: %s exists and isn't a regular file", name)
	}
	have, err := io.ReadAll(io.LimitReader(f, int64(len(content))+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if !bytes.Equal(have, content) {
		return fmt.Errorf("%s exists and differs; nothing was changed", name)
	}
	return nil
}
