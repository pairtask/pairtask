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

// diagramLabel is the row shown in place of a diagram's opening fence.
func diagramLabel(d diagram, k, n int, seq uint64) string {
	label := "◇ " + d.lang
	if seq != 0 {
		label += " · /diagram #" + strconv.FormatUint(seq, 10)
		if n > 1 {
			label += " " + strconv.Itoa(k)
		}
	}
	return label
}

// diagramLabels maps each diagram's opening fence line to its label.
func (b *lineBuilder) diagramLabels(lines []string) map[int]string {
	ds := findDiagrams(lines)
	labels := make(map[int]string, len(ds))
	for k, d := range ds {
		labels[d.open] = diagramLabel(d, k+1, len(ds), b.seq)
	}
	return labels
}

// diagramArg is "#n" with an optional diagram number K.
func diagramArg(a argument) error {
	_, _, err := a.diagramTarget()
	return err
}

func (a argument) diagramTarget() (seq uint64, k int, err error) {
	head, rest, _ := strings.Cut(strings.TrimSpace(string(a)), " ")
	if seq, err = argument(head).target(); err != nil {
		return 0, 0, err
	}
	if rest = strings.TrimSpace(rest); rest == "" {
		return seq, 0, nil
	}
	k, convErr := strconv.Atoi(rest)
	if convErr != nil || k < 1 {
		return 0, 0, errors.New("usage: /diagram #n [K]")
	}
	return seq, k, nil
}

func (m *Model) diagramCommand(arg argument) tea.Cmd {
	seq, k, _ := arg.diagramTarget() // diagramArg already checked it
	e, reason := m.entryForSeq(seq)
	if e != nil && e.omitted {
		e, reason = nil, "that message's text isn't available"
	}
	if e == nil {
		m.status = reason
		return nil
	}
	d, reason := pickDiagram(findDiagrams(bodyLines(payloadText(e.msg.Payload))), seq, k)
	if reason != "" {
		m.status = reason
		return nil
	}
	m.status = fmt.Sprintf("saving the diagram of #%d…", seq)
	dir := diagramDir()
	return func() tea.Msg {
		path, err := exportDiagram(dir, seq, k, d)
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

// pickDiagram selects diagram k (1-based); k 0 means the only one.
func pickDiagram(ds []diagram, seq uint64, k int) (diagram, string) {
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

// exportDiagram writes the source plus one newline to a file named only from
// the seq, the number and the source hash; it never replaces or follows
// anything. An identical existing file is reused.
func exportDiagram(dir string, seq uint64, k int, d diagram) (string, error) {
	if len(d.source) > maxDiagramBytes {
		return "", fmt.Errorf("diagram too large (over %d KiB)", maxDiagramBytes>>10)
	}
	if k == 0 {
		k = 1
	}
	sum := sha256.Sum256([]byte(d.source))
	name := fmt.Sprintf("%d-%d-%s.%s", seq, k, hex.EncodeToString(sum[:])[:12], diagramExt[d.lang])
	fd, err := openDiagramDir(dir)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	path := filepath.Join(dir, name)
	return path, writeOnce(fd, name, []byte(d.source+"\n"))
}

// openDiagramDir creates the base as needed, then opens its pairtask and
// diagrams components relative to their parent without following links, so
// a component can't be swapped for a link between the check and the use.
func openDiagramDir(dir string) (int, error) {
	parent, rest := filepath.Dir(filepath.Dir(dir)), []string{filepath.Base(filepath.Dir(dir)), filepath.Base(dir)}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return -1, err
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: parent, Err: err}
	}
	for _, name := range rest {
		next, err := openChildDir(fd, name)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

func openChildDir(parent int, name string) (int, error) {
	if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create %s: %w", name, err)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return -1, fmt.Errorf("unsafe directory: %s is a link or not a directory", name)
	}
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", name, err)
	}
	return fd, nil
}

// writeOnce creates name exclusively; when it exists, it must be a regular
// file holding exactly content, and it is left untouched either way.
func writeOnce(dir int, name string, content []byte) error {
	fd, err := unix.Openat(dir, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return sameFile(dir, name, content)
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
		_ = unix.Unlinkat(dir, name, 0)
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// sameFile compares an existing file without following a link or blocking
// on a FIFO, reading at most one byte past content.
func sameFile(dir int, name string, content []byte) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
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
