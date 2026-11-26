package main

import (
	"errors"
	"regexp"
	"strings"
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

// table is the table a header line opens, if it is one.
func (l tomlLine) table() (string, bool) {
	m := tableHeader.FindStringSubmatch(string(l))
	if m == nil {
		return "", false
	}
	return m[1], true
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

// check refuses a config that sets mcp_servers outside table headers (an
// inline table or dotted keys), which a line edit could break.
func (c codexConfig) check() error {
	if inlineServers.MatchString(c.text) {
		return errInlineServers
	}
	return nil
}

// without is the config less pairtask's tables.
func (c codexConfig) without() (codexConfig, error) {
	if err := c.check(); err != nil {
		return c, err
	}
	var kept strings.Builder
	skipping := false
	for _, line := range c.lines() {
		if table, ok := line.table(); ok {
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
	rest, err := c.without()
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
		if table, ok := line.table(); ok {
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
