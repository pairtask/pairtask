package main

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Codex keeps its servers in TOML tables, [mcp_servers.NAME]. pairtask
// edits only its own table (and any [mcp_servers.pairtask.*] subtable) as
// lines; every other line stays exactly as it was.

// codexConfig is the text of a Codex config.
type codexConfig struct{ text string }

// tomlLine is one line of it.
type tomlLine string

// tableHeader matches a TOML table header line, capturing its name.
var tableHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// inlineServers matches servers set outside table headers.
var inlineServers = regexp.MustCompile(`(?m)^\s*mcp_servers\s*[.=]`)

// commandLine matches a `command = "..."` line.
var commandLine = regexp.MustCompile(`^\s*command\s*=\s*"((?:[^"\\]|\\.)*)"`)

// errInlineServers: the servers are written in a form this can't edit
// line by line.
var errInlineServers = errors.New("the Codex config defines mcp_servers inline; add the pairtask server by hand")

// ourTable is pairtask's table name.
const ourTable = "mcp_servers." + serverName

// table is the table a header line opens, its name normalised (dotted
// keys trimmed and unquoted, so [mcp_servers."pairtask"] is
// mcp_servers.pairtask), if it is one; a header it can't read is an error.
func (l tomlLine) table() (string, bool, error) {
	m := tableHeader.FindStringSubmatch(string(l))
	if m == nil {
		return "", false, nil
	}
	name, err := tomlKey(m[1]).normalized()
	return name, true, err
}

// tomlKey is a dotted TOML key as written.
type tomlKey string

// errTomlKey: a table name this can't read.
var errTomlKey = errors.New("a table name in the Codex config can't be read; add the pairtask server by hand")

// normalized is the key's parts, each unquoted, joined with dots.
func (k tomlKey) normalized() (string, error) {
	var parts []string
	rest := strings.TrimSpace(string(k))
	for rest != "" {
		part, after, err := keyPart(rest)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
		rest = strings.TrimSpace(after)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ".") {
			return "", errTomlKey
		}
		rest = strings.TrimSpace(rest[1:])
	}
	return strings.Join(parts, "."), nil
}

// keyPart reads one part of a dotted key: bare, "basic" or 'literal'.
func keyPart(s string) (string, string, error) {
	switch s[0] {
	case '"':
		return quotedPart(s, '"')
	case '\'':
		return quotedPart(s, '\'')
	}
	end := strings.IndexAny(s, ". \t")
	if end < 0 {
		end = len(s)
	}
	if end == 0 || strings.ContainsAny(s[:end], `"'`) {
		return "", "", errTomlKey
	}
	return s[:end], s[end:], nil
}

func quotedPart(s string, quote byte) (string, string, error) {
	end := closingQuote(s, quote)
	if end < 0 {
		return "", "", errTomlKey
	}
	part := s[1:end]
	if quote == '"' {
		part = tomlUnquoter.Replace(part)
	}
	return part, s[end+1:], nil
}

// closingQuote is the index of the quote that ends the string opening s,
// or -1; basic strings may escape a quote with a backslash.
func closingQuote(s string, quote byte) int {
	escapes := quote == '"'
	for i := 1; i < len(s); i++ {
		switch {
		case escapes && s[i] == '\\':
			i++
		case s[i] == quote:
			return i
		}
	}
	return -1
}

// ours: a table name that belongs to pairtask's server.
func ours(table string) bool {
	return table == ourTable || strings.HasPrefix(table, ourTable+".")
}

func (c codexConfig) lines() []tomlLine {
	var out []tomlLine
	for _, l := range strings.SplitAfter(c.text, "\n") {
		out = append(out, tomlLine(l))
	}
	return out
}

// check refuses a config this can't edit line by line: mcp_servers set
// outside table headers (an inline table or dotted keys), a table header
// it can't read, or servers written as an array of tables.
func (c codexConfig) check() error {
	if _, err := c.decoded(); err != nil {
		return err
	}
	if inlineServers.MatchString(c.text) {
		return errInlineServers
	}
	for _, line := range c.lines() {
		if err := line.check(); err != nil {
			return err
		}
	}
	return nil
}

// check refuses an unreadable header, and an array of mcp_servers tables.
func (l tomlLine) check() error {
	table, isHeader, err := l.table()
	if !isHeader {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(string(l)), "[[") && strings.HasPrefix(table, "mcp_servers") {
		return errInlineServers
	}
	return nil
}

// without is the config less pairtask's tables; want is what a check
// expects of pairtask's server afterwards (none).
func (c codexConfig) without() (codexConfig, map[string]any, error) {
	out, err := c.withoutTables()
	return out, nil, err
}

// withoutTables is the config less pairtask's tables.
func (c codexConfig) withoutTables() (codexConfig, error) {
	if err := c.check(); err != nil {
		return c, err
	}
	var kept strings.Builder
	skipping := false
	for _, line := range c.lines() {
		table, isHeader, _ := line.table() // check already refused unreadable headers
		if isHeader {
			skipping = ours(table)
		}
		if !skipping {
			kept.WriteString(string(line))
		}
	}
	return codexConfig{kept.String()}, nil
}

// with is the config with pairtask's table set to run entry, at the end.
func (c codexConfig) with(entry serverEntry) (codexConfig, error) {
	rest, err := c.withoutTables()
	if err != nil {
		return c, err
	}
	return codexConfig{rest.separated() + entry.toml()}, nil
}

// separated is the text ending in a blank line, if it isn't empty.
func (c codexConfig) separated() string {
	text := c.text
	if text == "" {
		return ""
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if !strings.HasSuffix(text, "\n\n") {
		text += "\n"
	}
	return text
}

// command is the command in pairtask's table, and whether the table is
// there.
func (c codexConfig) command() (string, bool) {
	found, inside, command := false, false, ""
	for _, line := range c.lines() {
		if table, isHeader, _ := line.table(); isHeader {
			inside = table == ourTable
			found = found || inside
			continue
		}
		if m := commandLine.FindStringSubmatch(string(line)); inside && m != nil {
			command = tomlUnquoter.Replace(m[1])
		}
	}
	return command, found
}

// toml is the entry as pairtask's Codex table.
func (e serverEntry) toml() string {
	return "[" + ourTable + "]\n" +
		"command = \"" + tomlQuoter.Replace(e.Command) + "\"\n" +
		"args = [\"mcp\"]\n"
}

// tomlQuoter escapes a TOML basic string; tomlUnquoter undoes it.
var (
	tomlQuoter   = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	tomlUnquoter = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\r`, "\r", `\t`, "\t")
)

// decoded is the config as TOML values; a config that isn't valid TOML is
// an error, so it is never edited.
func (c codexConfig) decoded() (map[string]any, error) {
	v := map[string]any{}
	if _, err := toml.Decode(c.text, &v); err != nil {
		return nil, fmt.Errorf("the Codex config isn't valid TOML: %w", err)
	}
	return v, nil
}

// withoutOurs is v less pairtask's server, and that server.
func withoutOurs(v map[string]any) (map[string]any, any) {
	servers, ok := v["mcp_servers"].(map[string]any)
	if !ok {
		return v, nil
	}
	ours := servers[serverName]
	delete(servers, serverName)
	if len(servers) == 0 {
		delete(v, "mcp_servers")
	}
	return v, ours
}

// checkEdit: after is valid TOML that equals before apart from pairtask's
// server, and that server is want (nil: gone).
func checkEdit(before, after codexConfig, want map[string]any) error {
	b, err := before.decoded()
	if err != nil {
		return err
	}
	a, err := after.decoded()
	if err != nil {
		return fmt.Errorf("the edit would break the Codex config: %w", err)
	}
	b, _ = withoutOurs(b)
	a, ours := withoutOurs(a)
	if !reflect.DeepEqual(a, b) {
		return errors.New("the edit would change more of the Codex config than the pairtask server")
	}
	if want == nil && ours == nil {
		return nil
	}
	if !reflect.DeepEqual(ours, want) {
		return errors.New("the pairtask server in the Codex config isn't what was written")
	}
	return nil
}

// tomlValue is the entry as decoded TOML.
func (e serverEntry) tomlValue() map[string]any {
	return map[string]any{"command": e.Command, "args": []any{"mcp"}}
}
