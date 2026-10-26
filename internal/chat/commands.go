package chat

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// argument is the text after a command name, already trimmed.
type argument string

func (a argument) empty() bool { return a == "" }

// target reads "#n", a message number.
func (a argument) target() (uint64, error) {
	if !strings.HasPrefix(string(a), "#") {
		return 0, errTarget
	}
	n, err := strconv.ParseUint(string(a[1:]), 10, 64)
	if err != nil || n == 0 {
		return 0, errTarget
	}
	return n, nil
}

var errTarget = errors.New("expects #n, a message number")

// command is one slash command. check validates the argument before anything
// runs, so a malformed command never acts partly; available hides commands
// that can't apply now.
type command struct {
	name      string
	aliases   []string
	usage     string
	help      string
	check     func(argument) error
	run       func(m *Model, arg argument) tea.Cmd
	available func(m *Model) bool
}

func noArgs(a argument) error {
	if !a.empty() {
		return errors.New("takes no arguments")
	}
	return nil
}

func targetArg(a argument) error {
	_, err := a.target()
	return err
}

// copyArg is "#n", or "ref AGENT" with an optional @ before the agent.
func copyArg(a argument) error {
	if agent, ok := a.refAgent(); ok {
		if agent == "" {
			return errors.New("usage: /copy ref AGENT")
		}
		return nil
	}
	return targetArg(a)
}

// refAgent reads "ref AGENT"; ok is false when the argument isn't a ref copy.
func (a argument) refAgent() (agent string, ok bool) {
	head, rest, _ := strings.Cut(string(a), " ")
	if head != "ref" {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimSpace(rest), "@"), true
}

func optionalTarget(a argument) error {
	if a.empty() {
		return nil
	}
	return targetArg(a)
}

func replyArg(a argument) error {
	if a == "cancel" {
		return nil
	}
	return optionalTarget(a)
}

var errToArg = errors.New(`expects a JSON array such as ["codex-1"], or nothing to clear`)

// toArg accepts nothing, or exactly one JSON array of strings.
func toArg(a argument) error {
	if a.empty() {
		return nil
	}
	if !strings.HasPrefix(string(a), "[") {
		return errToArg
	}
	var ids []string
	if json.Unmarshal([]byte(a), &ids) != nil || ids == nil {
		return errToArg
	}
	if !onlyJSON(a) {
		return errToArg
	}
	return nil
}

// onlyJSON reports that a holds one JSON value and nothing after it.
func onlyJSON(a argument) bool {
	var raw json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(a)))
	if decoder.Decode(&raw) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func anyArg(argument) error { return nil }

var commands = []command{
	{name: "agents", aliases: []string{"who"}, help: "list agents", check: noArgs, run: (*Model).agentsCommand},
	{name: "reply", aliases: []string{"r"}, usage: "[#n|cancel]", help: "reply to a received message", check: replyArg, run: (*Model).replyRun, available: (*Model).hasReceived},
	{name: "ack", usage: "[#n|all|from id]", help: "acknowledge: pick one, #n, all, or from one agent (received, not approval)", check: ackArg, run: (*Model).ackCommand, available: (*Model).hasReceived},
	{name: "history", help: "load older messages", check: noArgs, run: (*Model).historyCommand},
	{name: "chat", usage: `all|id|@a @b|"id"|["a","b"]`, help: "switch conversation: all, an agent, a group of agents, or JSON for any IDs", check: chatArg, run: (*Model).chatCommand},
	{name: "room", aliases: []string{"rooms"}, usage: "[NAME | add NAME @a | remove NAME @a | delete NAME]", help: "rooms: list, open (creating) a room's tab, add or remove members, delete a room", check: roomArg, run: (*Model).roomsCommand},
	{name: "close", help: "hide this conversation from the bar (nothing is deleted; new messages reopen it)", check: noArgs, run: (*Model).closeCommand},
	{name: "forget", help: "remove this group or 1:1 from the bar until /chat reopens it (its messages stay in All)", check: noArgs, run: (*Model).forgetCommand},
	{name: "stages", usage: "#n", help: "delivery stages of a sent message", check: targetArg, run: (*Model).stagesCommand, available: (*Model).hasSent},
	{name: "claims", usage: "#n", help: "which agents claimed which tasks of a sent message (never an approval)", check: targetArg, run: (*Model).claimsCommand, available: (*Model).hasSent},
	{name: "to", usage: `["id", …]`, help: "address IDs literally; /to alone clears", check: toArg, run: (*Model).toCommand},
	{name: "find", usage: "[text]", help: "find a message; /find again continues", check: anyArg, run: (*Model).findCommand},
	{name: "copy", usage: "#n | ref AGENT", help: "ask the terminal to copy a message's text, or an agent's status ref", check: copyArg, run: (*Model).copyCommand},
	{name: "diagram", usage: "#n [K]", help: "save a message's mermaid or plantuml source to a file (nothing is rendered or run)", check: diagramArg, run: (*Model).diagramCommand},
	{name: "clear", help: "hide everything above this point (nothing is deleted)", check: noArgs, run: (*Model).clearCommand},
	{name: "help", help: "keys, commands and addressing", check: noArgs, run: func(m *Model, _ argument) tea.Cmd { m.toggleHelp(); return nil }},
	{name: "quit", aliases: []string{"exit"}, help: "quit", check: noArgs, run: func(m *Model, _ argument) tea.Cmd { return m.quit() }},
}

func (c command) offered(m *Model) bool { return c.available == nil || c.available(m) }

// commandCandidates lists the offered command names starting with prefix.
func (m *Model) commandCandidates(prefix argument) []string {
	var out []string
	for _, c := range commands {
		if c.offered(m) && strings.HasPrefix(c.name, string(prefix)) {
			out = append(out, c.name)
		}
	}
	return out
}

// lookupCommand resolves an exact name or alias, then a unique prefix.
func lookupCommand(name argument) (command, []string) {
	var matches []string
	for _, c := range commands {
		if c.name == string(name) || slices.Contains(c.aliases, string(name)) {
			return c, nil
		}
		if strings.HasPrefix(c.name, string(name)) {
			matches = append(matches, c.name)
		}
	}
	if len(matches) == 1 {
		return byName(matches[0]), nil
	}
	return command{}, matches
}

func byName(name string) command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return command{}
}

// runCommandLine runs the typed command. Pasted command text is inert.
func (m *Model) runCommandLine() tea.Cmd {
	line := m.compose.cmd
	if line.pasted {
		m.status = "type the command; pasted text doesn't run"
		return nil
	}
	text := strings.TrimSpace(line.input.Value())
	if text == "" {
		m.closeCommandLine()
		return nil
	}
	name, arg, _ := strings.Cut(text, " ")
	return m.runCommand(argument(name), argument(strings.TrimSpace(arg)))
}

func (m *Model) runCommand(name, arg argument) tea.Cmd {
	c, candidates := lookupCommand(name)
	switch {
	case c.run == nil && len(candidates) > 1:
		m.status = "/" + safe(string(name)) + " could be: /" + strings.Join(candidates, ", /")
	case c.run == nil:
		m.status = "unknown command /" + safe(string(name)) + " (/help lists commands)"
	default:
		return m.runChecked(c, arg)
	}
	return nil
}

func (m *Model) runChecked(c command, arg argument) tea.Cmd {
	if err := c.check(arg); err != nil {
		m.status = "/" + c.name + " " + err.Error()
		return nil
	}
	m.closeCommandLine()
	m.status = ""
	return c.run(m, arg)
}
