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
)

// The agent clients pairtask can be added to: each keeps a config file
// listing its MCP servers. connect adds a server named "pairtask" that
// runs this program in MCP mode; disconnect removes it. Nothing else in
// the file changes, and the previous file is kept beside it as .bak.

const clientsUsage = `usage: pairtask-desktop clients [--json]
       pairtask-desktop connect CLIENT [--dry-run]
       pairtask-desktop disconnect CLIENT [--dry-run]
CLIENT is one of: ` + "claude-desktop, claude-code, codex, cursor"

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
}

func hostClientEnv() clientEnv {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return clientEnv{goos: runtime.GOOS, home: home, exe: exe}
}

// clients is every supported client on this system.
func (e clientEnv) clients() []client {
	claudeDir := filepath.Join(e.home, ".config", "Claude")
	if e.goos == "darwin" {
		claudeDir = filepath.Join(e.home, "Library", "Application Support", "Claude")
	}
	return []client{
		{ID: "claude-desktop", Label: "Claude Desktop", Config: filepath.Join(claudeDir, "claude_desktop_config.json"), Home: claudeDir, JSONKey: "mcpServers"},
		{ID: "claude-code", Label: "Claude Code", Config: filepath.Join(e.home, ".claude.json"), Home: filepath.Join(e.home, ".claude"), JSONKey: "mcpServers"},
		{ID: "codex", Label: "Codex", Config: filepath.Join(e.home, ".codex", "config.toml"), Home: filepath.Join(e.home, ".codex")},
		{ID: "cursor", Label: "Cursor", Config: filepath.Join(e.home, ".cursor", "mcp.json"), Home: filepath.Join(e.home, ".cursor"), JSONKey: "mcpServers"},
	}
}

func (e clientEnv) client(id string) (client, error) {
	for _, c := range e.clients() {
		if c.ID == id {
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
}

// runClients lists the clients and whether pairtask is in each.
func runClients(args []string, env clientEnv, out io.Writer) error {
	fs := flag.NewFlagSet("clients", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errors.New(clientsUsage)
	}
	var all []clientStatus
	for _, c := range env.clients() {
		all = append(all, env.status(c))
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

// status reads one client's config.
func (e clientEnv) status(c client) clientStatus {
	s := clientStatus{ID: c.ID, Label: c.Label, Config: c.Config, Installed: exists(c.Home) || exists(c.Config)}
	data, err := os.ReadFile(c.Config)
	if errors.Is(err, fs.ErrNotExist) {
		return s
	}
	if err != nil {
		s.Error = err.Error()
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
		command, found := tomlCommand(string(data), serverName)
		return command, found, checkInline(string(data))
	}
	top, err := parseObject(orEmpty(data))
	if err != nil {
		return "", false, err
	}
	raw, ok := top.get(c.JSONKey)
	if !ok {
		return "", false, nil
	}
	servers, err := parseObject(raw)
	if err != nil {
		return "", false, err
	}
	entry, ok := servers.get(serverName)
	if !ok {
		return "", false, nil
	}
	var e struct{ Command string }
	_ = json.Unmarshal(entry, &e)
	return e.Command, true, nil
}

func orEmpty(data []byte) []byte {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}")
	}
	return data
}

// runConnect adds (or with remove, takes out) pairtask in one client.
func runConnect(args []string, env clientEnv, out io.Writer, remove bool) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print the new config instead of writing it")
	ids, flags := splitArgs(args)
	if err := fs.Parse(flags); err != nil || len(ids) != 1 {
		return errors.New(clientsUsage)
	}
	c, err := env.client(ids[0])
	if err != nil {
		return err
	}
	before, err := readConfig(c.Config)
	if err != nil {
		return err
	}
	after, err := env.edited(c, before, remove)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", c.Label, c.Config, err)
	}
	if *dry {
		_, err := out.Write(after)
		return err
	}
	return applyConfig(c, before, after, remove, out)
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

// readConfig is the client's config, empty when it has none yet.
func readConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// edited is the config with pairtask's entry set, or removed.
func (e clientEnv) edited(c client, data []byte, remove bool) ([]byte, error) {
	if c.JSONKey == "" {
		return e.editedTOML(string(data), remove)
	}
	entry, err := json.Marshal(serverEntry{Command: e.exe, Args: []string{"mcp"}})
	if err != nil {
		return nil, err
	}
	return jsonServers(data, c.JSONKey, func(servers object) object {
		if remove {
			return servers.without(serverName)
		}
		return servers.set(serverName, entry)
	})
}

func (e clientEnv) editedTOML(data string, remove bool) ([]byte, error) {
	if remove {
		out, err := tomlWithout(data, serverName)
		return []byte(out), err
	}
	out, err := tomlWith(data, serverName, tomlBlock(serverName, e.exe))
	return []byte(out), err
}

// applyConfig writes the new config, keeping the old one as .bak, and
// says what it did.
func applyConfig(c client, before, after []byte, remove bool, out io.Writer) error {
	verb := map[bool]string{false: "Connected", true: "Disconnected"}[remove]
	if bytes.Equal(before, after) {
		fmt.Fprintf(out, "%s: already %s.\n", c.Label, strings.ToLower(verb))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.Config), 0o700); err != nil {
		return err
	}
	if before != nil {
		if err := os.WriteFile(c.Config+".bak", before, 0o600); err != nil {
			return fmt.Errorf("keeping the old config: %w", err)
		}
	}
	if err := writeAtomic(c.Config, after); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s (%s). Restart %s to use it.\n", verb, c.Label, c.Config, c.Label)
	return nil
}

// writeAtomic replaces path with data through a temporary file beside it,
// keeping the old file's permissions.
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
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
	return os.Rename(tmp.Name(), path)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
