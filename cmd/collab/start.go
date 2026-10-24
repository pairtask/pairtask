package main

import (
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
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// startOptions is what `pairtask start` was asked for.
type startOptions struct {
	Agent, ID, Model, Effort, Socket string
	DryRun                           bool
	Native                           []string // everything after --, for the agent itself
}

// startLayout is where the launchers and configs live; tests point it elsewhere.
type startLayout struct {
	CodexLauncher, CodexBinary, ClaudeBinary, BaseConfig, ConfigDir string
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
	}
}

// startPlan is what a start would run: the launcher argv and, for Claude,
// the MCP config it needs written first.
type startPlan struct {
	Argv       []string
	ConfigPath string
	Config     []byte
}

const startUsage = "usage: pairtask start (claude|codex) [--id ID] [--model NAME] [--effort LEVEL] [--socket PATH] [--dry-run] [-- agent arguments]"

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
	if given(flags, "id") && opts.ID == "" {
		return opts, errors.New("--id cannot be empty")
	}
	return opts, opts.validate()
}

func startFlags(opts *startOptions) *flag.FlagSet {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.ID, "id", "", "logical agent ID (default claude-1 or codex-1)")
	flags.StringVar(&opts.Model, "model", "", "model name for the agent")
	flags.StringVar(&opts.Effort, "effort", "", "reasoning effort for the agent")
	flags.StringVar(&opts.Socket, "socket", "", "broker Unix socket path")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "print the command and the config path; run nothing")
	return flags
}

// splitNative separates the agent's own arguments, after "--", from ours.
func splitNative(args []string) (ours, native []string) {
	if i := indexOf(args, "--"); i >= 0 {
		return args[:i], args[i+1:]
	}
	return args, nil
}

func given(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
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
	case "claude", "codex":
		return nil
	}
	return fmt.Errorf("unknown agent %q: use claude or codex", o.Agent)
}

func (o *startOptions) validateID() error {
	if o.ID == "" {
		o.ID = o.Agent + "-1"
	}
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
	if o.Effort != "" && !startToken.MatchString(o.Effort) {
		return fmt.Errorf("--effort %q: letters, digits, '.', '_' and '-' only", o.Effort)
	}
	return nil
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

// buildStart turns the options into the launcher command. base is the
// installed Claude MCP config (unused for Codex).
func buildStart(opts startOptions, layout startLayout, base []byte) (startPlan, error) {
	if opts.Agent == "codex" {
		return codexPlan(opts, layout), nil
	}
	config, err := claudeConfig(base, serverIdentity{ID: opts.ID, Socket: opts.Socket})
	if err != nil {
		return startPlan{}, err
	}
	path := configPath(layout.ConfigDir, opts.ID)
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
	argv := []string{layout.CodexLauncher, "--codex", layout.CodexBinary, "--agent-id", opts.ID, "--socket", socket, "--terminal", "--"}
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
		if err := checkFlag(args, name); err != nil {
			return nil, err
		}
	}
	args, err := setFlag(args, flagEdit{Name: "--agent-id", Value: want.ID})
	if err != nil || want.Socket == "" {
		return args, err
	}
	return setFlag(args, flagEdit{Name: "--socket", Value: want.Socket, AppendMissing: true})
}

// checkFlag rejects a malformed occurrence of name in args: at the end with
// no value, or followed by another flag instead of a value.
func checkFlag(args []string, name string) error {
	for i, a := range args {
		if a != name {
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return fmt.Errorf("claude MCP config: %s has no value in mcpServers.pairtask.args", name)
		}
	}
	return nil
}

// flagEdit is one flag to set in the server's arguments.
type flagEdit struct {
	Name, Value   string
	AppendMissing bool // add the flag when absent instead of failing
}

// setFlag replaces the value of a well-formed flag in args, whether written
// "--flag X" or "--flag=X"; exactly one occurrence is expected, or none when
// the edit may append.
func setFlag(args []string, edit flagEdit) ([]string, error) {
	found := 0
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == edit.Name:
			out = append(out, edit.Name, edit.Value)
			i++
			found++
		case strings.HasPrefix(args[i], edit.Name+"="):
			out = append(out, edit.Name+"="+edit.Value)
			found++
		default:
			out = append(out, args[i])
		}
	}
	if found == 1 {
		return out, nil
	}
	if found == 0 && edit.AppendMissing {
		return append(out, edit.Name, edit.Value), nil
	}
	return nil, fmt.Errorf("claude MCP config: %s appears %d times in mcpServers.pairtask.args, want one", edit.Name, found)
}

// configPath names the per-ID config without putting the ID in the path.
func configPath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, "claude-mcp-"+hex.EncodeToString(sum[:])[:16]+".json")
}

// writePrivate writes data to path for this user only, replacing the old file
// atomically so a failed write leaves the previous config in place.
func writePrivate(path string, data []byte) error {
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
	plan, code := planStart(opts, rt)
	if code != 0 {
		return code
	}
	if opts.DryRun {
		printPlan(plan, rt.Stdout)
		return 0
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
}

// launch writes the config, then hands the process to the launcher.
func launch(plan startPlan, rt startRuntime) int {
	if plan.Config != nil {
		if err := writePrivate(plan.ConfigPath, plan.Config); err != nil {
			fmt.Fprintln(rt.Stderr, err)
			return 1
		}
	}
	if err := rt.Exec(plan.Argv[0], plan.Argv, os.Environ()); err != nil {
		fmt.Fprintln(rt.Stderr, "exec "+plan.Argv[0]+": "+err.Error())
		return 1
	}
	return 0
}
