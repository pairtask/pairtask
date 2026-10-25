// Command codex is an App Server stdio proxy for one explicitly managed thread.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/budget"
	"github.com/pairtask/pairtask/internal/host"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/resume"
	"golang.org/x/sync/errgroup"
)

func main() {
	socket := flag.String("socket", "/tmp/collab-ai.sock", "broker Unix socket path")
	agent := flag.String("agent-id", "", "logical inbox ID (required)")
	binary := flag.String("codex", "codex", "Codex executable")
	terminal := flag.Bool("terminal", false, "launch the normal Codex terminal UI through this proxy; pass Codex arguments after --")
	mcpSocket := flag.String("mcp-socket", "", "internal stdio relay to the managed session's MCP socket")
	tokenCap := flag.Int64("token-cap", 0, "soft cap on reported Codex totalTokens; requires --budget-file")
	budgetFile := flag.String("budget-file", "", "persistent token budget file (one launcher at a time)")
	budgetName := flag.String("budget", "", "named budget created by collab budget create; cannot combine with inline cap flags")
	statusSocket := flag.String("budget-status-socket", "", "publish read-only named-budget status at this absolute Unix socket path")
	var rooms protocol.RoomFlags
	flag.Var(&rooms, "room", "join this room on registration (repeatable)")
	restricted := flag.Bool("restricted-operator", false, "experimental text-only stdio ingress; requires --budget; blocks configuration, resume and terminal mode")
	flag.Parse()
	mode := restrictedMode{enabled: *restricted, budgetName: *budgetName}
	if err := mode.validate(*terminal, *mcpSocket, flag.Args()); err != nil {
		log.Print(err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := bridge.ClientConfig{SocketPath: *socket, AgentID: *agent, Harness: "codex-app-server", Rooms: rooms}
	selection := budgetSelection{name: *budgetName, path: *budgetFile, limit: *tokenCap}
	cap, err := selection.open(*mcpSocket)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	if cap != nil {
		defer cap.Close()
	}
	closeStatus, err := openBudgetStatus(ctx, *statusSocket, *budgetName, cap)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	defer closeStatus()
	launcher := codexLauncher{client: cfg, binary: *binary, budget: cap, restricted: *restricted}
	err = launcher.runMode(ctx, *mcpSocket, *terminal, flag.Args())
	if err != nil {
		closeStatus()
		log.Print(err)
		os.Exit(1)
	}
}

func (l codexLauncher) runMode(ctx context.Context, mcpSocket string, terminal bool, args []string) error {
	operator := operatorIO{input: os.Stdin, output: os.Stdout}
	if mcpSocket != "" {
		return relayMCP(ctx, mcpSocket, operator)
	}
	if terminal {
		return l.runTerminal(ctx, args)
	}
	return l.runOperator(ctx, operator)
}

func optionalBudget(path string, limit int64, relay string) (*budget.Budget, error) {
	return (budgetSelection{path: path, limit: limit}).open(relay)
}

type budgetSelection struct {
	name, path string
	limit      int64
}

func (s budgetSelection) open(relay string) (*budget.Budget, error) {
	if s.name != "" {
		return s.openNamed(relay)
	}
	path, limit := s.path, s.limit
	if path == "" && limit == 0 {
		return nil, nil
	}
	if relay != "" {
		return nil, errors.New("token caps belong on the launcher, not the internal MCP relay")
	}
	return budget.Open(path, limit)
}

func (s budgetSelection) openNamed(relay string) (*budget.Budget, error) {
	if s.path != "" || s.limit != 0 {
		return nil, errors.New("--budget cannot be combined with --token-cap or --budget-file")
	}
	if relay != "" {
		return nil, errors.New("token caps belong on the launcher, not the internal MCP relay")
	}
	store, err := budget.DefaultStore()
	if err != nil {
		return nil, err
	}
	return store.Open(s.name)
}

func run(ctx context.Context, cfg bridge.ClientConfig, binary string) error {
	return runWithOperator(ctx, cfg, binary, operatorIO{input: os.Stdin, output: os.Stdout})
}

type operatorIO struct {
	input  io.ReadCloser
	output io.WriteCloser
}

// The terminal and its App Server share one launch configuration and budget.
type codexLauncher struct {
	client     bridge.ClientConfig
	binary     string
	budget     *budget.Budget
	restricted bool
	appStderr  io.Writer // nil preserves stderr for non-terminal integrations.
}

func (l codexLauncher) budgetResult(err error) error {
	if l.budget == nil {
		return err
	}
	if stopped := l.budget.Err(); stopped != nil {
		return stopped
	}
	return err
}

func runWithOperator(ctx context.Context, cfg bridge.ClientConfig, binary string, operator operatorIO) error {
	return (codexLauncher{client: cfg, binary: binary}).runOperator(ctx, operator)
}

func (l codexLauncher) runOperator(ctx context.Context, operator operatorIO) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := bridge.NewLazyClient(l.client)
	if err != nil {
		return err
	}
	defer client.Close()
	cmd := appServerCommand(ctx, l.binary, "app-server", "--listen", "stdio://")
	cmd.Stderr = os.Stderr
	if l.appStderr != nil {
		cmd.Stderr = l.appStderr
	}
	input, output, err := appServerPipes(cmd)
	if err != nil {
		return err
	}
	finish, err := l.beginSession()
	if err != nil {
		input.Close()
		output.Close()
		return err
	}
	settled := true // No child exists yet; a failed exec cannot have spent tokens.
	defer func() {
		if err := finish(settled); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return err
	}
	settled = false
	defer func() { cancel(); input.Close(); output.Close(); stopAppServer(cmd) }()
	p := host.NewProxy(host.NewWire(input), host.NewWire(operator.output))
	p.Budget = l.budget
	p.RestrictedOperator = l.restricted
	p.OnThread = recordThread(l.client.AgentID, resume.Store{Dir: resumeStateDir()})
	p.Listener = bridge.NewListener(ctx, client, p)
	defer func() {
		p.Listener.Close() // Join peer delivery before deciding that admissions are settled.
		settled = result == nil && p.SessionSettled()
	}()
	result = serveWithTools(ctx, p, operator.input, output)
	return result
}

func serveWithTools(ctx context.Context, p *host.Proxy, operatorIn, output io.ReadCloser) error {
	tools, err := host.OpenTools(ctx, p.Listener)
	if err != nil {
		return err
	}
	p.Tools = tools
	defer tools.Close()
	if p.RestrictedOperator {
		// The secured client has no local execution environment. Fresh threads
		// can use App Server dynamic tools without spawning a local MCP relay.
		return serve(ctx, p, operatorIn, output)
	}
	endpoint, err := newRuntimeMCP(ctx, p.Listener, func() bool { return p.ThreadID() != "" })
	if err != nil {
		return err
	}
	defer endpoint.Close()
	p.RuntimeMCP, err = endpoint.config()
	if err != nil {
		return err
	}
	return serve(ctx, p, operatorIn, output)
}

func serve(ctx context.Context, p *host.Proxy, operatorIn, output io.ReadCloser) error {
	group, ctx := errgroup.WithContext(ctx)
	stop := context.AfterFunc(ctx, func() { operatorIn.Close(); output.Close() })
	defer stop()
	group.Go(func() error {
		return readOperatorFrames(ctx, operatorIn, func(f host.Frame) error { return p.FromOperator(ctx, f) })
	})
	group.Go(func() error { return readHostFrames(ctx, p, output) })
	group.Go(func() error { return p.ServeTools(ctx) })
	group.Go(func() error { return p.ServeBudget(ctx) })
	err := group.Wait()
	if p.Budget != nil && p.Budget.Err() != nil {
		return p.Budget.Err()
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
