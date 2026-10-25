package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// startOptions is what `pairtask start` was asked for.
type startOptions struct {
	Agent, ID, Model, Effort, Socket string
	DryRun, Resume                   bool
	Native                           []string           // everything after --, for the agent itself
	Rooms                            protocol.RoomFlags // --room, repeatable: rooms to join on registration
}

// startLayout is where the launchers and configs live; tests point it elsewhere.
type startLayout struct {
	CodexLauncher, CodexBinary, ClaudeBinary, BaseConfig, ConfigDir string
	PiBinary, PiExtension, MCPBinary                                string // pi and its extension; the bridge it runs
	StateDir, CodexHome                                             string // resume bindings; Codex's saved conversations
}

func defaultStartLayout() startLayout {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return startLayout{
		CodexLauncher: "/usr/local/lib/pairtask/pairtask-codex",
		CodexBinary:   "/usr/local/bin/codex",
		ClaudeBinary:  "/usr/local/bin/claude",
		BaseConfig:    "/etc/pairtask/claude-mcp.json",
		ConfigDir:     filepath.Join(dir, "pairtask"),
		StateDir:      stateDir(),
		PiBinary:      "/usr/local/bin/pi",
		PiExtension:   cmp.Or(os.Getenv("PAIRTASK_PI_EXTENSION"), "/usr/local/lib/pairtask/pi-extension"),
		MCPBinary:     "/usr/local/lib/pairtask/pairtask-mcp",
		CodexHome:     cmp.Or(os.Getenv("CODEX_HOME"), filepath.Join(os.Getenv("HOME"), ".codex")),
	}
}

func stateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(dir, "pairtask")
}

// startPlan is what a start would run: the launcher argv and, for Claude,
// the MCP config it needs written first.
type startPlan struct {
	Argv       []string
	Env        []string // added to the environment, for harnesses that read it
	ConfigPath string
	Config     []byte
}

const startUsage = "usage: pairtask start (claude|codex|pi) [--id ID] [--model NAME] [--effort LEVEL] [--socket PATH] [--room ROOM]... [--resume] [--dry-run] [-- agent arguments]"

var startToken = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const brokerSocketDefault = "/tmp/collab-ai.sock"

// parseStart reads the arguments after "start".
func parseStart(args []string) (startOptions, error) {
	var opts startOptions
	if len(args) == 0 {
		return opts, errors.New(startUsage)
	}
	if args[0] == "-h" || args[0] == "--help" {
		return opts, flag.ErrHelp
	}
	opts.Agent = args[0]
	rest, native := splitNative(args[1:])
	opts.Native = native
	flags := startFlags(&opts)
	if err := flags.Parse(rest); err != nil {
		return opts, err
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q; agent arguments go after --", flags.Arg(0))
	}
	if err := opts.normalizeResume(); err != nil {
		return opts, err
	}
	return opts, opts.validate()
}

func startFlags(opts *startOptions) *flag.FlagSet {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.ID, "id", opts.Agent+"-1", "logical agent ID") // an explicit empty ID stays empty and is rejected
	flags.StringVar(&opts.Model, "model", "", "model name for the agent")
	flags.StringVar(&opts.Effort, "effort", "", "reasoning effort for the agent")
	flags.StringVar(&opts.Socket, "socket", "", "broker Unix socket path")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "print the command and the config path; run nothing")
	flags.BoolVar(&opts.Resume, "resume", false, "codex: continue the conversation this ID last owned")
	flags.Var(&opts.Rooms, "room", "join this room on start (repeatable)")
	return flags
}

// splitNative separates the agent's own arguments, after "--", from ours.
func splitNative(args []string) (ours, native []string) {
	if i := slices.Index(args, "--"); i >= 0 {
		return args[:i], args[i+1:]
	}
	return args, nil
}

// normalizeResume turns a managed selector into --resume and refuses
// --resume next to arguments that already choose a conversation.
func (o *startOptions) normalizeResume() error {
	if !o.Resume {
		o.adoptManagedSelector()
		return nil
	}
	if o.Agent != "codex" {
		return errors.New("--resume is for codex; claude has its own --resume and --continue")
	}
	if nativeLifecycle(o.Native) {
		return errors.New("--resume already chooses the conversation; drop the resume or fork arguments after --")
	}
	return nil
}

// adoptManagedSelector turns Codex's `resume` or `resume --last` into
// --resume.
func (o *startOptions) adoptManagedSelector() {
	if o.Agent != "codex" {
		return
	}
	if managedSelector(o.Native) {
		o.Resume, o.Native = true, nil
	}
}

func (o *startOptions) validate() error {
	if err := o.validateAgent(); err != nil {
		return err
	}
	if err := o.validateID(); err != nil {
		return err
	}
	return o.validateValues()
}

func (o *startOptions) validateAgent() error {
	switch o.Agent {
	case "claude", "codex", "pi":
		return nil
	}
	return fmt.Errorf("unknown agent %q: use claude, codex or pi", o.Agent)
}

func (o *startOptions) validateID() error {
	if strings.ContainsRune(o.ID, 0) {
		return errors.New("--id cannot contain NUL")
	}
	return protocol.ValidateAgentID(o.ID)
}

// validateValues: the model is an argv value kept literally (aliases such as
// opus[1m]); the effort is interpolated into Codex's TOML, so it is a token.
func (o *startOptions) validateValues() error {
	if strings.ContainsRune(o.Model, 0) || strings.ContainsRune(o.Socket, 0) {
		return errors.New("arguments cannot contain NUL")
	}
	if o.Effort != "" && o.Agent == "pi" {
		return errors.New("--effort is for claude and codex; pass pi's own options after --")
	}
	if o.Effort != "" && !startToken.MatchString(o.Effort) {
		return fmt.Errorf("--effort %q: letters, digits, '.', '_' and '-' only", o.Effort)
	}
	return nil
}

// buildStart turns the options into the launcher command. base is the
// installed Claude MCP config (unused for Codex).
func buildStart(opts startOptions, layout startLayout, base []byte) (startPlan, error) {
	switch opts.Agent {
	case "codex":
		return codexPlan(opts, layout), nil
	case "pi":
		return piPlan(opts, layout), nil
	}
	config, err := claudeConfig(base, serverIdentity{ID: opts.ID, Socket: opts.Socket, Rooms: opts.Rooms})
	if err != nil {
		return startPlan{}, err
	}
	path := layout.configPath(opts.ID)
	argv := []string{layout.ClaudeBinary, "--strict-mcp-config", "--mcp-config", path, "--dangerously-load-development-channels", "server:pairtask"}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "--effort", opts.Effort)
	}
	return startPlan{Argv: append(argv, opts.Native...), ConfigPath: path, Config: config}, nil
}

func codexPlan(opts startOptions, layout startLayout) startPlan {
	socket := opts.Socket
	if socket == "" {
		socket = brokerSocketDefault
	}
	argv := []string{layout.CodexLauncher, "--codex", layout.CodexBinary, "--agent-id", opts.ID, "--socket", socket}
	argv = append(append(argv, roomArgs(opts.Rooms)...), "--terminal", "--")
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	if opts.Effort != "" {
		argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", opts.Effort)) // a TOML basic string; the token is validated
	}
	return startPlan{Argv: append(argv, opts.Native...)}
}

// serverIdentity is what start changes in the pairtask MCP server entry; an
// empty Socket keeps the installed one.
type serverIdentity struct {
	ID, Socket string
	Rooms      []string // added as --room flags
}

// roomArgs are --room flags for the bridge or the launcher.
func roomArgs(rooms []string) []string {
	var out []string
	for _, r := range rooms {
		out = append(out, "--room", r)
	}
	return out
}

// claudeConfig is the installed config with only the pairtask server's
// --agent-id (and --socket, when given) changed; every other field and
// server is kept as it was.
func claudeConfig(base []byte, want serverIdentity) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, fmt.Errorf("claude MCP config: %w", err)
	}
	server, args, err := pairtaskServer(doc)
	if err != nil {
		return nil, err
	}
	args, err = rewriteArgs(args, want)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	server["args"] = out
	return json.MarshalIndent(doc, "", "  ")
}

// pairtaskServer finds mcpServers.pairtask and its string arguments.
func pairtaskServer(doc map[string]any) (map[string]any, []string, error) {
	servers, _ := doc["mcpServers"].(map[string]any)
	server, _ := servers["pairtask"].(map[string]any)
	raw, _ := server["args"].([]any)
	if server == nil || raw == nil {
		return nil, nil, errors.New("claude MCP config: mcpServers.pairtask.args missing")
	}
	args := make([]string, 0, len(raw))
	for _, a := range raw {
		s, ok := a.(string)
		if !ok {
			return nil, nil, errors.New("claude MCP config: mcpServers.pairtask.args must be strings")
		}
		args = append(args, s)
	}
	return server, args, nil
}

// rewriteArgs validates the pairtask flags and sets the ID and the socket.
func rewriteArgs(args []string, want serverIdentity) ([]string, error) {
	for _, name := range []string{"--agent-id", "--socket"} {
		if err := (flagEdit{Name: name}).check(args); err != nil {
			return nil, err
		}
	}
	args, err := flagEdit{Name: "--agent-id", Value: want.ID}.apply(args)
	if err == nil && want.Socket != "" {
		args, err = flagEdit{Name: "--socket", Value: want.Socket, AppendMissing: true}.apply(args)
	}
	return append(args, roomArgs(want.Rooms)...), err
}

// check rejects a malformed occurrence of the flag in args: at the end with
// no value, or followed by another flag instead of a value.
func (e flagEdit) check(args []string) error {
	for i, a := range args {
		if a != e.Name {
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return fmt.Errorf("claude MCP config: %s has no value in mcpServers.pairtask.args", e.Name)
		}
	}
	return nil
}

// flagEdit is one flag to set in the server's arguments.
type flagEdit struct {
	Name, Value   string
	AppendMissing bool // add the flag when absent instead of failing
}

// apply replaces the value of the well-formed flag in args, whether written
// "--flag X" or "--flag=X"; exactly one occurrence is expected, or none when
// the edit may append.
func (e flagEdit) apply(args []string) ([]string, error) {
	found := 0
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == e.Name:
			out = append(out, e.Name, e.Value)
			i++
			found++
		case strings.HasPrefix(args[i], e.Name+"="):
			out = append(out, e.Name+"="+e.Value)
			found++
		default:
			out = append(out, args[i])
		}
	}
	if found == 1 {
		return out, nil
	}
	if found == 0 && e.AppendMissing {
		return append(out, e.Name, e.Value), nil
	}
	return nil, fmt.Errorf("claude MCP config: %s appears %d times in mcpServers.pairtask.args, want one", e.Name, found)
}

// configPath names the per-ID config without putting the ID in the path.
func (l startLayout) configPath(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(l.ConfigDir, "claude-mcp-"+hex.EncodeToString(sum[:])[:16]+".json")
}

// writeConfig writes the plan's config for this user only, replacing the old
// file atomically so a failed write leaves the previous config in place.
func (p startPlan) writeConfig() error {
	if p.Config == nil {
		return nil
	}
	path, data := p.ConfigPath, p.Config
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".claude-mcp-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name) // the previous config, if any, is still in place
	}
	return err
}

// execFunc replaces the process; syscall.Exec in production.
type execFunc func(argv0 string, argv, env []string) error

// startRuntime is where start writes, reads its layout from, and how it
// hands over the process.
type startRuntime struct {
	Stdout, Stderr io.Writer
	Layout         startLayout
	Exec           execFunc
	Status         statusFunc             // broker status for the resume checks; nil skips them
	Chdir          func(dir string) error // os.Chdir in production
}

// runStart is `pairtask start`: write the config if any, then replace this
// process with the launcher so it owns the terminal and the exit status.
func runStart(args []string, rt startRuntime) int {
	opts, err := parseStart(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(rt.Stdout, startUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(rt.Stderr, err)
		fmt.Fprintln(rt.Stderr, startUsage)
		return 2
	}
	dir, err := resolveResume(&opts, rt)
	if err != nil {
		return 2
	}
	plan, code := planStart(opts, rt)
	if code != 0 {
		return code
	}
	if opts.DryRun {
		printPlan(plan, rt.Stdout)
		printDir(dir, rt.Stdout)
		return 0
	}
	return launchIn(dir, plan, rt)
}

// launchIn changes to dir, when one was chosen, and launches.
func launchIn(dir string, plan startPlan, rt startRuntime) int {
	if dir != "" && rt.Chdir != nil {
		if err := rt.Chdir(dir); err != nil {
			fmt.Fprintln(rt.Stderr, err)
			return 1
		}
	}
	return launch(plan, rt)
}

// planStart reads the installed config when Claude needs it and builds the plan.
func planStart(opts startOptions, rt startRuntime) (startPlan, int) {
	var base []byte
	if opts.Agent == "claude" {
		var err error
		if base, err = os.ReadFile(rt.Layout.BaseConfig); err != nil {
			fmt.Fprintln(rt.Stderr, err)
			return startPlan{}, 1
		}
	}
	plan, err := buildStart(opts, rt.Layout, base)
	if err != nil {
		fmt.Fprintln(rt.Stderr, err)
		return startPlan{}, 2
	}
	return plan, 0
}

func printPlan(plan startPlan, stdout io.Writer) {
	for _, a := range plan.Argv {
		fmt.Fprintln(stdout, a)
	}
	if plan.ConfigPath != "" {
		fmt.Fprintln(stdout, "config: "+plan.ConfigPath)
	}
	for _, kv := range plan.Env {
		fmt.Fprintln(stdout, "env: "+kv)
	}
}

// launch writes the config, then hands the process to the launcher.
func launch(plan startPlan, rt startRuntime) int {
	if err := plan.writeConfig(); err != nil {
		fmt.Fprintln(rt.Stderr, err)
		return 1
	}
	if err := rt.Exec(plan.Argv[0], plan.Argv, append(os.Environ(), plan.Env...)); err != nil {
		fmt.Fprintln(rt.Stderr, "exec "+plan.Argv[0]+": "+err.Error())
		return 1
	}
	return 0
}
