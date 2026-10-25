// Package resume remembers which Codex conversation each managed agent ID
// owns, so `pairtask start codex --id X --resume` can continue it, and
// suggests saved conversations when there is no record. It never decides
// whether a conversation exists; Codex does.
package resume

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrNoBinding      = errors.New("no recorded Codex conversation")
	ErrCorruptBinding = errors.New("the recorded Codex conversation is unreadable")
	ErrAgentMismatch  = errors.New("the recorded Codex conversation belongs to another agent ID")
)

// Binding is an agent ID's Codex conversation and where it ran.
type Binding struct {
	AgentID   string    `json:"agent_id"`
	ThreadID  string    `json:"thread_id"`
	Cwd       string    `json:"cwd"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store keeps one private binding file per agent ID; the ID itself never
// appears in the path.
type Store struct {
	Dir string
}

func (s Store) path(agent string) string {
	sum := sha256.Sum256([]byte(agent))
	return filepath.Join(s.Dir, "codex-"+hex.EncodeToString(sum[:])[:16]+".json")
}

// Save replaces the agent's binding atomically; a failed save leaves the
// previous one in place.
func (s Store) Save(b Binding) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".codex-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(b.AgentID))
}

// Load reads the agent's binding: ErrNoBinding when there is none,
// ErrCorruptBinding or ErrAgentMismatch when it can't be trusted.
func (s Store) Load(agent string) (Binding, error) {
	data, err := os.ReadFile(s.path(agent))
	if errors.Is(err, fs.ErrNotExist) {
		return Binding{}, ErrNoBinding
	}
	if err != nil {
		return Binding{}, err
	}
	var b Binding
	if err := json.Unmarshal(data, &b); err != nil || b.ThreadID == "" {
		return Binding{}, fmt.Errorf("%w: %s", ErrCorruptBinding, s.path(agent))
	}
	if b.AgentID != agent {
		return Binding{}, ErrAgentMismatch
	}
	return b, nil
}

// Candidate is a saved Codex conversation, a suggestion only.
type Candidate struct {
	ThreadID string
	Cwd      string
	Modified time.Time
	Bytes    int64
}

// Candidates lists saved conversations under codexHome/sessions, those that
// ran in cwd first, then newest, at most limit. It reads only each file's
// first line, the session metadata.
func Candidates(codexHome, cwd string, limit int) ([]Candidate, error) {
	var out []Candidate
	root := filepath.Join(codexHome, "sessions")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return skipMissing(err)
		}
		if d.IsDir() || !rollout(d.Name()) {
			return nil
		}
		if c, ok := candidate(path); ok {
			out = append(out, c)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Cwd == cwd) != (out[j].Cwd == cwd) {
			return out[i].Cwd == cwd
		}
		return out[i].Modified.After(out[j].Modified)
	})
	return out[:min(len(out), limit)], err
}

func skipMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fs.SkipAll
	}
	return err
}

func rollout(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

// candidate reads a rollout's session metadata line.
func candidate(path string) (Candidate, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Candidate{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Candidate{}, false
	}
	line, _ := bufio.NewReaderSize(f, metaLineMax).ReadSlice('\n')
	id, cwd := sessionMeta(line)
	if id == "" {
		return Candidate{}, false
	}
	return Candidate{ThreadID: id, Cwd: cwd, Modified: info.ModTime(), Bytes: info.Size()}, true
}

// sessionMeta reads a rollout's first line; an empty ID means it isn't the
// session metadata.
func sessionMeta(line []byte) (id, cwd string) {
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &meta); err != nil {
		return "", ""
	}
	if meta.Type != "session_meta" {
		return "", ""
	}
	return meta.Payload.ID, meta.Payload.Cwd
}

// metaLineMax bounds the metadata line read from a rollout; real ones are
// about 20 KiB, mostly instructions.
const metaLineMax = 1 << 20
