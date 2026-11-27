package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const exePath = "/Applications/Pairtask.app/Contents/MacOS/pairtask-desktop"

func testEnv(t *testing.T) clientEnv {
	t.Helper()
	return clientEnv{goos: "darwin", home: t.TempDir(), exe: exePath, dir: t.TempDir()}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func connect(t *testing.T, env clientEnv, id string, undo bool) string {
	t.Helper()
	var out bytes.Buffer
	act := add
	if undo {
		act = remove
	}
	if err := runConnect([]string{id}, env, &out, act); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

const claudeConfig = `{
  "globalShortcut": "Cmd+Space",
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": [
        "-y",
        "@modelcontextprotocol/server-filesystem"
      ]
    }
  },
  "zeta": true
}
`

func TestConnectAddsOnlyPairtaskToAJSONConfig(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	writeFile(t, path, claudeConfig)
	connect(t, env, "claude-desktop", false)
	got := readFile(t, path)
	want := strings.Replace(claudeConfig, `      ]
    }
  },`, `      ]
    },
    "pairtask": {
      "command": "`+exePath+`",
      "args": [
        "mcp",
        "--agent-id",
        "claude-desktop"
      ]
    }
  },`, 1)
	if got != want {
		t.Fatalf("config after connect:\n%s\nwant:\n%s", got, want)
	}
	if readFile(t, path+".bak") != claudeConfig {
		t.Fatal("the previous config wasn't kept as .bak")
	}
	if out := connect(t, env, "claude-desktop", false); !strings.Contains(out, "already connected") {
		t.Fatalf("connecting again said %q", out)
	}
	connect(t, env, "claude-desktop", true)
	if got := readFile(t, path); got != claudeConfig {
		t.Fatalf("config after disconnect:\n%s\nwant the original:\n%s", got, claudeConfig)
	}
}

func TestConnectCreatesAMissingConfig(t *testing.T) {
	env := testEnv(t)
	connect(t, env, "cursor", false)
	var cfg struct {
		McpServers map[string]struct {
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(env.home, ".cursor", "mcp.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	if s := cfg.McpServers["pairtask"]; !runsPairtask(s.Command, s.Args, "cursor") {
		t.Fatalf("server entry %+v", cfg.McpServers)
	}
}

// runsPairtask: an entry runs this pairtask-desktop's MCP server as agent.
func runsPairtask(command string, args []string, agent string) bool {
	return command == exePath && strings.Join(args, " ") == "mcp --agent-id "+agent
}

func TestConnectRefusesAConfigItCantRead(t *testing.T) {
	for client, c := range map[string]struct{ file, text string }{
		"claude-code": {".claude.json", `{"mcpServers": {`},
		"codex":       {filepath.Join(".codex", "config.toml"), "model = [\n"},
	} {
		env := testEnv(t)
		path := filepath.Join(env.home, c.file)
		writeFile(t, path, c.text)
		if err := runConnect([]string{client}, env, &bytes.Buffer{}, add); err == nil {
			t.Errorf("%s: an unreadable config was edited", client)
		}
		if got := readFile(t, path); got != c.text {
			t.Errorf("%s: an unreadable config was changed to %q", client, got)
		}
	}
}

const codexSample = `model = "gpt-5"

[mcp_servers.docs]
command = "docs-mcp"

[profiles.fast]
model = "gpt-5-mini"
`

func TestConnectEditsOnlyPairtaskTablesInCodex(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.home, ".codex", "config.toml")
	writeFile(t, path, codexSample)
	connect(t, env, "codex", false)
	want := codexSample + "\n[mcp_servers.pairtask]\ncommand = \"" + exePath + "\"\nargs = [\"mcp\", \"--agent-id\", \"codex\"]\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("config after connect:\n%s\nwant:\n%s", got, want)
	}
	if out := connect(t, env, "codex", false); !strings.Contains(out, "already connected") {
		t.Fatalf("connecting again said %q", out)
	}
	// A subtable of ours goes with it; everything else stays.
	writeFile(t, path, readFile(t, path)+"\n[mcp_servers.pairtask.env]\nX = \"1\"\n")
	connect(t, env, "codex", true)
	if got := readFile(t, path); got != codexSample+"\n" {
		t.Fatalf("config after disconnect:\n%q\nwant:\n%q", got, codexSample+"\n")
	}
}

// replacedOnce: one pairtask table, this one, and the other server kept.
func replacedOnce(config string) bool {
	tables := strings.Count(config, "[mcp_servers")
	return tables == 2 && !strings.Contains(config, `"pairtask"]`) && strings.Contains(config, exePath)
}

func TestConnectRefusesInlineCodexServers(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.home, ".codex", "config.toml")
	inline := "mcp_servers = { docs = { command = \"docs\" } }\n"
	writeFile(t, path, inline)
	if err := runConnect([]string{"codex"}, env, &bytes.Buffer{}, add); err == nil {
		t.Fatal("an inline mcp_servers config was edited")
	}
	if got := readFile(t, path); got != inline {
		t.Fatalf("an inline config was changed to %q", got)
	}
}

func TestClientsReportsEachClient(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.home, ".cursor", "mcp.json"), `{"mcpServers":{"pairtask":{"command":"/old/pairtask-desktop","args":["mcp"]}}}`)
	writeFile(t, filepath.Join(env.home, ".codex", "config.toml"), codexSample)
	connect(t, env, "codex", false)
	var out bytes.Buffer
	if err := runClients([]string{"--json"}, env, &out); err != nil {
		t.Fatal(err)
	}
	var all []clientStatus
	if err := json.Unmarshal(out.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range all {
		got[s.ID] = describe(s)
	}
	want := map[string]string{"codex": "connected as agent codex", "cursor": "connected to another copy of pairtask", "claude-desktop": "not installed", "claude-code": "not installed"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
}

func TestConnectRejectsAnUnknownClient(t *testing.T) {
	if err := runConnect([]string{"vim"}, testEnv(t), &bytes.Buffer{}, add); err == nil {
		t.Fatal("an unknown client was accepted")
	}
}

func TestConnectDryRunWritesNothing(t *testing.T) {
	env := testEnv(t)
	var out bytes.Buffer
	if err := runConnect([]string{"--dry-run", "cursor"}, env, &out, add); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), exePath) {
		t.Fatalf("dry run printed %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(env.home, ".cursor", "mcp.json")); err == nil {
		t.Fatal("a dry run wrote the config")
	}
}

func TestConnectKeepsEveryOtherByteOfAJSONConfig(t *testing.T) {
	for name, original := range map[string]string{
		"compact":  `{"theme":"dark","mcpServers":{"a":{"command":"x","args":[]},"b":{"command":"y"}},"n":1e3,"s":"é<"}`,
		"four":     "{\n    \"mcpServers\": {\n        \"a\": {\"command\": \"x\"}\n    },\n    \"list\": [1,2,  3]\n}\n",
		"tabs":     "{\n\t\"mcpServers\": {},\n\t\"keep\": \"me\"\n}",
		"noserver": "{\n  \"projects\": {\"/a\": {\"allowedTools\": []}}\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			path := filepath.Join(env.home, ".claude.json")
			writeFile(t, path, original)
			connect(t, env, "claude-code", false)
			after := readFile(t, path)
			var v map[string]any
			if err := json.Unmarshal([]byte(after), &v); err != nil {
				t.Fatalf("connect wrote invalid JSON: %v\n%s", err, after)
			}
			connect(t, env, "claude-code", true)
			got := readFile(t, path)
			if name == "noserver" {
				original = strings.Replace(original, "\n}\n", ",\n  \"mcpServers\": {}\n}\n", 1)
			}
			if got != original {
				t.Fatalf("connect then disconnect:\n%q\nwant:\n%q", got, original)
			}
		})
	}
}

func TestDisconnectWithoutAConfigWritesNothing(t *testing.T) {
	env := testEnv(t)
	if out := connect(t, env, "cursor", true); !strings.Contains(out, "already disconnected") {
		t.Fatalf("disconnecting a missing config said %q", out)
	}
	if _, err := os.Stat(filepath.Join(env.home, ".cursor", "mcp.json")); err == nil {
		t.Fatal("disconnect created a config")
	}
}

func TestConnectRecognisesAQuotedCodexTable(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.home, ".codex", "config.toml")
	writeFile(t, path, "[mcp_servers.\"pairtask\"]\ncommand = \"/old\"\n\n[mcp_servers.docs]\ncommand = \"docs\"\n")
	connect(t, env, "codex", false)
	got := readFile(t, path)
	if !replacedOnce(got) {
		t.Fatalf("a quoted table wasn't replaced:\n%s", got)
	}
}

func TestClientsSaysWhenEachClientLastStarted(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.dir, "clients-seen.json"), `{"claude-code":"2026-11-26T10:00:00Z","Codex-MCP-Client":"2026-11-26T11:00:00Z"}`)
	var out bytes.Buffer
	if err := runClients([]string{"--json"}, env, &out); err != nil {
		t.Fatal(err)
	}
	var all []clientStatus
	if err := json.Unmarshal(out.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range all {
		if s.LastSeen != nil {
			got[s.ID] = s.LastSeen.Format("15:04")
		}
	}
	if want := map[string]string{"claude-code": "10:00", "codex": "11:00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("last seen %v, want %v", got, want)
	}
}

func TestAnOwnerModeEntryIsShownAndConnectGivesTheAppItsOwnAgent(t *testing.T) {
	env := testEnv(t)
	path := filepath.Join(env.home, ".cursor", "mcp.json")
	writeFile(t, path, `{"mcpServers":{"pairtask":{"command":"`+exePath+`","args":["mcp"]}}}`)
	if got := describe(env.status(cursorClient(t, env))); !strings.Contains(got, "owner mode") {
		t.Fatalf("a v0.3 entry reads %q, want owner mode", got)
	}
	if out := connect(t, env, "cursor", false); strings.Contains(out, "already connected") {
		t.Fatalf("connecting an owner-mode entry again changed nothing: %q", out)
	}
	if got := describe(env.status(cursorClient(t, env))); got != "connected as agent cursor" {
		t.Fatalf("after connecting again it reads %q", got)
	}
}

func cursorClient(t *testing.T, env clientEnv) client {
	t.Helper()
	c, err := env.client("cursor")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnEntrysAgentIsReadEitherWay(t *testing.T) {
	for args, want := range map[string]string{"mcp": "", "mcp --agent-id codex": "codex", "mcp --agent-id=codex": "codex", "mcp --agent-id": ""} {
		if got := (serverEntry{Args: strings.Fields(args)}).agent(); got != want {
			t.Errorf("%q: agent %q, want %q", args, got, want)
		}
	}
}
