package main

import (
	"errors"
	"regexp"
	"strings"
)

// Codex keeps its servers in TOML tables, [mcp_servers.NAME]. pairtask
// edits only its own table (and any [mcp_servers.pairtask.*] subtable) as
// lines; every other line stays exactly as it was.

// tableHeader matches a TOML table header line, capturing its name.
var tableHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// errInlineServers: the servers are written in a form this can't edit
// line by line.
var errInlineServers = errors.New("the Codex config defines mcp_servers inline; add the pairtask server by hand")

// tomlBlock is the [mcp_servers.<name>] table running exe in MCP mode.
func tomlBlock(name, exe string) string {
	return "[mcp_servers." + name + "]\n" +
		"command = " + tomlString(exe) + "\n" +
		"args = [\"mcp\"]\n"
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// ownsTable: a table header line belongs to the named server.
func ownsTable(table, name string) bool {
	own := "mcp_servers." + name
	return table == own || strings.HasPrefix(table, own+".")
}

// tomlWithout is data less the named server's tables.
func tomlWithout(data, name string) (string, error) {
	if err := checkInline(data); err != nil {
		return "", err
	}
	var kept []string
	skipping := false
	for _, line := range strings.SplitAfter(data, "\n") {
		if m := tableHeader.FindStringSubmatch(line); m != nil {
			skipping = ownsTable(m[1], name)
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, ""), nil
}

// tomlWith is data with the named server's table set to block, in place
// of any it had, at the end.
func tomlWith(data, name, block string) (string, error) {
	rest, err := tomlWithout(data, name)
	if err != nil {
		return "", err
	}
	if rest != "" && !strings.HasSuffix(rest, "\n") {
		rest += "\n"
	}
	if rest != "" && !strings.HasSuffix(rest, "\n\n") {
		rest += "\n"
	}
	return rest + block, nil
}

// checkInline refuses a config that sets mcp_servers outside table
// headers (an inline table or dotted keys), which a line edit could break.
func checkInline(data string) error {
	inline := regexp.MustCompile(`(?m)^\s*mcp_servers\s*[.=]`)
	if inline.MatchString(data) {
		return errInlineServers
	}
	return nil
}

// tomlCommand is the command in the named server's table, and whether the
// table is there.
func tomlCommand(data, name string) (string, bool) {
	found, inside := false, false
	command := ""
	for _, line := range strings.Split(data, "\n") {
		if m := tableHeader.FindStringSubmatch(line); m != nil {
			inside = m[1] == "mcp_servers."+name
			found = found || inside
			continue
		}
		if m := commandLine.FindStringSubmatch(line); inside && m != nil {
			command = tomlUnquote(m[1])
		}
	}
	return command, found
}

// commandLine matches a `command = "..."` line.
var commandLine = regexp.MustCompile(`^\s*command\s*=\s*"((?:[^"\\]|\\.)*)"`)

// tomlUnquote undoes tomlString's escapes.
func tomlUnquote(s string) string {
	r := strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\r`, "\r", `\t`, "\t")
	return r.Replace(s)
}
