package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
)

// The agent clients pairtask can be added to: each keeps a config file
// listing its MCP servers. connect adds a server named "pairtask" that
// runs this program in MCP mode; disconnect removes it. Nothing else in
// the file changes, and the previous file is kept beside it as .bak.

const clientsUsage = `usage: pairtask-desktop clients [--json]
       pairtask-desktop connect CLIENT [--dry-run]
       pairtask-desktop disconnect CLIENT [--dry-run]
CLIENT is one of: ` + "claude-desktop, claude-code, codex, cursor"

// action is what a client command does to pairtask's entry.
type action int

const (
	add    action = iota // connect
	remove               // disconnect
)

// clientID names a client on the command line.
type clientID string

// clientName is the name a client gives when it connects.
type clientName string

// serverName is the entry pairtask owns in each client's config.
const serverName = "pairtask"

// client is one agent client and where it keeps its servers.
type client struct {
	ID    string // as given on the command line
	Label string
	// Config is the file listing its servers; Home is a folder whose
	// presence means the client is installed.
	Config, Home string
	// JSONKey is the servers object in a JSON config; "" means TOML.
	JSONKey string
	// Names are fragments of the name the client gives when it connects.
	Names []string
}

// serverEntry is the server pairtask adds to a JSON config, in the order
// these clients write it.
type serverEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// clientEnv is what the clients need from the host; tests replace it.
type clientEnv struct {
	goos, home, exe string
	dir             string // this node's data folder, for the edit lock and the clients seen
}

func hostClientEnv() clientEnv {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, _ := dataDir("")
	return clientEnv{goos: runtime.GOOS, home: home, exe: exe, dir: dir}
}

// clients is every supported client on this system.
func (e clientEnv) clients() []client {
	claudeDir := filepath.Join(e.home, ".config", "Claude")
	if e.goos == "darwin" {
		claudeDir = filepath.Join(e.home, "Library", "Application Support", "Claude")
	}
	return []client{
		{ID: "claude-desktop", Label: "Claude Desktop", Config: filepath.Join(claudeDir, "claude_desktop_config.json"), Home: claudeDir, JSONKey: "mcpServers", Names: []string{"claude-ai", "claude desktop", "claude-desktop"}},
		{ID: "claude-code", Label: "Claude Code", Config: filepath.Join(e.home, ".claude.json"), Home: filepath.Join(e.home, ".claude"), JSONKey: "mcpServers", Names: []string{"claude-code"}},
		{ID: "codex", Label: "Codex", Config: filepath.Join(e.home, ".codex", "config.toml"), Home: filepath.Join(e.home, ".codex"), Names: []string{"codex"}},
		{ID: "cursor", Label: "Cursor", Config: filepath.Join(e.home, ".cursor", "mcp.json"), Home: filepath.Join(e.home, ".cursor"), JSONKey: "mcpServers", Names: []string{"cursor"}},
	}
}

func (e clientEnv) client(id clientID) (client, error) {
	for _, c := range e.clients() {
		if c.ID == string(id) {
			return c, nil
		}
	}
	return client{}, fmt.Errorf("unknown client %q\n%s", id, clientsUsage)
}

// clientStatus is one client's state, for the app and for people.
type clientStatus struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Installed bool   `json:"installed"`
	Connected bool   `json:"connected"`
	// Other: connected, but to another copy of pairtask-desktop.
	Other  bool   `json:"other,omitempty"`
	Config string `json:"config"`
	Error  string `json:"error,omitempty"`
	// LastSeen is when the client last started this node's MCP server:
	// configured but never seen means it hasn't been restarted yet.
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// runClients lists the clients and whether pairtask is in each.
func runClients(args []string, env clientEnv, out io.Writer) error {
	fs := flag.NewFlagSet("clients", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errors.New(clientsUsage)
	}
	var all []clientStatus
	seen, _ := desktop.ReadSeenClients(env.dir)
	for _, c := range env.clients() {
		s := env.status(c)
		s.LastSeen = c.lastSeen(seen)
		all = append(all, s)
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(all)
	}
	for _, s := range all {
		fmt.Fprintf(out, "%-15s %s\n", s.Label+":", describe(s))
	}
	return nil
}

func describe(s clientStatus) string {
	switch {
	case s.Error != "":
		return "can't read its config: " + s.Error
	case s.Connected && s.Other:
		return "connected to another copy of pairtask"
	case s.Connected:
		return "connected"
	case s.Installed:
		return "not connected"
	}
	return "not installed"
}

// lastSeen is when a name of this client last connected, if one did.
func (c client) lastSeen(seen map[string]time.Time) *time.Time {
	var latest *time.Time
	for name, at := range seen {
		if c.named(clientName(name)) && later(at, latest) {
			at := at
			latest = &at
		}
	}
	return latest
}

// later: at is after the latest so far, or the first.
func later(at time.Time, latest *time.Time) bool {
	return latest == nil || at.After(*latest)
}

func (c client) named(name clientName) bool {
	lower := strings.ToLower(string(name))
	for _, n := range c.Names {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// status reads one client's config.
func (e clientEnv) status(c client) clientStatus {
	s := clientStatus{ID: c.ID, Label: c.Label, Config: c.Config, Installed: c.installed()}
	data, err := c.read()
	if data == nil || err != nil {
		s.Error = errText(err)
		return s
	}
	command, found, err := configured(c, data)
	if err != nil {
		s.Error = err.Error()
	}
	s.Connected, s.Other = found, found && command != e.exe
	return s
}

// configured is the command the client's pairtask entry runs, if any.
func configured(c client, data []byte) (string, bool, error) {
	if c.JSONKey == "" {
		cfg := codexConfig{string(data)}
		command, found := cfg.command()
		return command, found, cfg.check()
	}
	doc := jsonDoc(orEmpty(data))
	top, err := doc.top()
	if err != nil {
		return "", false, err
	}
	servers, ok, err := doc.servers(top, jsonKey(c.JSONKey))
	if err != nil || !ok {
		return "", false, err
	}
	entry, ok := servers.find(serverName)
	if !ok {
		return "", false, nil
	}
	var e struct{ Command string }
	_ = json.Unmarshal(doc[entry.valAt:entry.valEnd], &e)
	return e.Command, true, nil
}

func orEmpty(data []byte) []byte {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}")
	}
	return data
}

// runConnect adds (or with remove, takes out) pairtask in one client.
func runConnect(args []string, env clientEnv, out io.Writer, act action) error {
	id, dry, err := connectArgs(args)
	if err != nil {
		return err
	}
	c, err := env.client(id)
	if err != nil {
		return err
	}
	unlock, err := env.lockClients()
	if err != nil {
		return err
	}
	defer unlock()
	if dry {
		edit, err := env.prepare(c, act)
		return dryRun(edit, err, out)
	}
	return env.change(c, act, out)
}

// connectArgs is the client named and whether only to print the result.
func connectArgs(args []string) (clientID, bool, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print the new config instead of writing it")
	ids, flags := splitArgs(args)
	if err := fs.Parse(flags); err != nil || len(ids) != 1 {
		return "", false, errors.New(clientsUsage)
	}
	return clientID(ids[0]), *dry, nil
}

// change edits the client's config; if something else changed it between
// reading and writing, once more from what is there now.
func (e clientEnv) change(c client, act action, out io.Writer) error {
	for attempt := 0; ; attempt++ {
		edit, err := e.prepare(c, act)
		if err != nil {
			return err
		}
		err = edit.apply(out)
		if !errors.Is(err, errChanged) || attempt > 0 {
			return err
		}
	}
}

// prepare reads the client's config and works out the edit.
func (e clientEnv) prepare(c client, act action) (configEdit, error) {
	before, err := c.read()
	if err != nil {
		return configEdit{}, err
	}
	edit := configEdit{client: c, before: before, after: before, act: act}
	if act == remove && before == nil {
		return edit, nil // nothing to take out of a config that isn't there
	}
	edit.after, err = e.edited(c, before, act)
	if err != nil {
		return edit, fmt.Errorf("%s: %s: %w", c.Label, c.Config, err)
	}
	return edit, nil
}

func dryRun(edit configEdit, err error, out io.Writer) error {
	if err != nil {
		return err
	}
	_, err = out.Write(edit.after)
	return err
}

// splitArgs separates the client from the flags, in any order.
func splitArgs(args []string) (ids, flags []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			ids = append(ids, a)
		}
	}
	return ids, flags
}

// read is the client's config, nil when it has none yet.
func (c client) read() ([]byte, error) {
	data, err := os.ReadFile(c.Config)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// installed: the client's folder or config is there.
func (c client) installed() bool {
	for _, path := range []string{c.Home, c.Config} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// edited is the config with pairtask's entry set, or removed.
func (e clientEnv) edited(c client, data []byte, act action) ([]byte, error) {
	if c.JSONKey == "" {
		return e.editedTOML(string(data), act)
	}
	before := jsonDoc(orEmpty(data))
	entry := serverEntry{Command: e.exe, Args: []string{"mcp"}}
	var after jsonDoc
	var err error
	if act == remove {
		after, err = before.disconnected(jsonKey(c.JSONKey))
	} else {
		after, err = before.connected(jsonKey(c.JSONKey), entry)
	}
	if err != nil {
		return nil, err
	}
	if err := sameExcept(before, after, jsonKey(c.JSONKey)); err != nil {
		return nil, err
	}
	if data == nil {
		after = append(after, '\n') // a new file ends its line
	}
	return after, nil
}

func (e clientEnv) editedTOML(data string, act action) ([]byte, error) {
	before := codexConfig{data}
	entry := serverEntry{Command: e.exe}
	after, want, err := before.without()
	if act == add && err == nil {
		after, err = before.with(entry)
		want = entry.tomlValue()
	}
	if err != nil {
		return nil, err
	}
	if err := checkEdit(before, after, want); err != nil {
		return nil, err
	}
	return []byte(after.text), nil
}

// errChanged: the config changed between reading it and writing it.
var errChanged = errors.New("the config changed while it was being edited; nothing was written")

// configEdit is one change to a client's config.
type configEdit struct {
	client        client
	before, after []byte
	act           action
}

// apply writes the new config, keeping the old one as .bak, and says what
// it did.
func (e configEdit) apply(out io.Writer) error {
	c := e.client
	verb := map[action]string{add: "Connected", remove: "Disconnected"}[e.act]
	if bytes.Equal(e.before, e.after) {
		fmt.Fprintf(out, "%s: already %s.\n", c.Label, strings.ToLower(verb))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.Config), 0o700); err != nil {
		return err
	}
	if now, err := c.read(); err != nil || !bytes.Equal(now, e.before) {
		return errors.Join(errChanged, err)
	}
	if e.before != nil {
		if err := os.WriteFile(c.Config+".bak", e.before, 0o600); err != nil {
			return fmt.Errorf("keeping the old config: %w", err)
		}
	}
	if err := c.replace(e.after, e.before); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s (%s). Restart %s to use it.\n", verb, c.Label, c.Config, c.Label)
	return nil
}

// replace writes data in place of the config through a temporary file
// beside it, keeping the old file's permissions, as long as the config is
// still was: it is read again just before the rename. That narrows, but
// can't close, the window for a program that doesn't take our lock.
func (c client) replace(data, was []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(c.Config); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.Config), filepath.Base(c.Config)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if now, err := c.read(); err != nil || !bytes.Equal(now, was) {
		return errors.Join(errChanged, err)
	}
	return os.Rename(tmp.Name(), c.Config)
}

// lockClients holds a lock in this node's data folder while one copy of
// pairtask edits a client's config, so two never overwrite each other.
func (e clientEnv) lockClients() (func(), error) {
	if e.dir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(e.dir, "clients.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
