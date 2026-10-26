// Command peerprobe runs the #178 peer probe's pieces on real containers:
// the TLS gate inside secured, the opaque relay in the online connector, a
// client that sends one contact request, and a bare Unix connect check. It
// is a validation tool for the disposable CI topology, never shipped in an
// image or installed as a service.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

const usage = "usage: peerprobe gate|relay|send|connect [flags]"

// command is one subcommand: it returns an exit code and an error to print.
type command func(ctx context.Context, args []string) (int, error)

var commands = map[string]command{
	"gate":    wrap(runGate),
	"relay":   wrap(runRelay),
	"send":    runSend,
	"connect": func(_ context.Context, args []string) (int, error) { return runConnect(args) },
}

// wrap adapts a command that only fails to the exit-code form.
func wrap(run func(context.Context, []string) error) command {
	return func(ctx context.Context, args []string) (int, error) { return 0, run(ctx, args) }
}

func main() {
	if len(os.Args) < 2 || commands[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code, err := commands[os.Args[1]](ctx, os.Args[2:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = max(code, 1)
	}
	os.Exit(code)
}

// event is one JSON line on stdout; the runner selects lines by "event".
func event(name string, fields map[string]any) {
	fields["event"] = name
	encoded, _ := json.Marshal(fields)
	fmt.Println(string(encoded))
}

// gateFlags are the gate's settings.
type gateFlags struct {
	socket, state string
	discoverable  bool
}

func parseGate(args []string) (gateFlags, error) {
	var f gateFlags
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.StringVar(&f.socket, "socket", "", "Unix socket to listen on (required)")
	flags.StringVar(&f.state, "state", "", "private directory for the gate's identity (required)")
	flags.BoolVar(&f.discoverable, "discoverable", true, "answer contact requests; false refuses them")
	if err := flags.Parse(args); err != nil {
		return f, err
	}
	if f.socket == "" || f.state == "" {
		return f, errors.New("gate needs --socket and --state")
	}
	return f, nil
}

// runGate serves the TLS gate on a Unix socket, with a self-signed identity
// kept under --state, a private directory outside the shared IPC volume.
func runGate(ctx context.Context, args []string) error {
	f, err := parseGate(args)
	if err != nil {
		return err
	}
	cert, err := loadIdentity(identityPaths{dir: f.state, name: "gate"})
	if err != nil {
		return err
	}
	gate, err := peer.NewGate(peer.GateConfig{
		Cert:         cert,
		Store:        peer.NewMemStore(),
		Discoverable: func() bool { return f.discoverable },
		OnConnection: logConnection,
	})
	if err != nil {
		return err
	}
	ln, err := listenSocket(f.socket)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Leaf.Raw})
	event("gate_ready", map[string]any{"socket": f.socket, "fingerprint": peer.Fingerprint(cert.Leaf), "cert_pem": string(certPEM)})
	return gate.Serve(ctx, ln)
}

// logConnection records what the kernel says connected, never assuming it.
func logConnection(info peer.ConnInfo) {
	fields := map[string]any{"fingerprint": info.Fingerprint, "uid": -1, "gid": -1}
	if info.HasCreds {
		fields["uid"], fields["gid"] = info.UID, info.GID
	}
	event("peer_credentials", fields)
}

// listenSocket listens on a Unix socket at path, replacing only a stale
// socket, and makes it group-connectable.
func listenSocket(path string) (net.Listener, error) {
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// runRelay forwards a TCP listener to the gate's Unix socket, opaquely.
func runRelay(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("relay", flag.ContinueOnError)
	listen := flags.String("listen", "", "TCP address to listen on (required)")
	dial := flags.String("dial", "", "the gate's Unix socket (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listen == "" || *dial == "" {
		return errors.New("relay needs --listen and --dial")
	}
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", *dial)
	}})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	event("relay_ready", map[string]any{"listen": ln.Addr().String(), "dial": *dial})
	return relay.Serve(ctx, ln)
}

// sendFlags are the client's settings.
type sendFlags struct {
	addr, serverCert, state, id, note, name string
}

func parseSend(args []string) (sendFlags, error) {
	var f sendFlags
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.StringVar(&f.addr, "addr", "", "the relay's host:port (required)")
	flags.StringVar(&f.serverCert, "server-cert", "", "PEM file with the gate's exact certificate (required)")
	flags.StringVar(&f.state, "state", "", "private directory for this side's identity (required)")
	flags.StringVar(&f.id, "request-id", "", "request id (required)")
	flags.StringVar(&f.note, "note", "", "note to send")
	flags.StringVar(&f.name, "name", "probe", "display name")
	if err := flags.Parse(args); err != nil {
		return f, err
	}
	for _, required := range []string{f.addr, f.serverCert, f.state, f.id} {
		if required == "" {
			return f, errors.New("send needs --addr, --server-cert, --state and --request-id")
		}
	}
	return f, nil
}

// runSend sends one contact request through a relay and prints the reply.
// Exit 0 for pending or duplicate, 2 for refused, 1 for a transport or
// verification failure.
func runSend(ctx context.Context, args []string) (int, error) {
	f, err := parseSend(args)
	if err != nil {
		return 1, err
	}
	reply, client, err := sendOnce(ctx, f)
	if err != nil {
		return 1, err
	}
	event("reply", map[string]any{"request_id": reply.RequestID, "status": reply.Status, "reason": reply.Reason,
		"gate_fingerprint": client.gate, "client_fingerprint": client.self})
	if reply.Status == peer.StatusRefused {
		return 2, nil
	}
	return 0, nil
}

// sentBy names the two sides of a send for the log.
type sentBy struct{ gate, self string }

func sendOnce(ctx context.Context, f sendFlags) (peer.Reply, sentBy, error) {
	cert, err := loadIdentity(identityPaths{dir: f.state, name: "client"})
	if err != nil {
		return peer.Reply{}, sentBy{}, err
	}
	server, err := readCertificate(f.serverCert)
	if err != nil {
		return peer.Reply{}, sentBy{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := peer.Dial(ctx, f.addr, peer.ClientConfig{Cert: cert, Server: server})
	if err != nil {
		return peer.Reply{}, sentBy{}, err
	}
	defer client.Close()
	reply, err := client.Send(ctx, peer.Record{Version: peer.SchemaVersion, Kind: peer.KindRequest, RequestID: f.id,
		From: peer.Identity{Name: f.name, Workspace: "probe"}, Note: f.note})
	return reply, sentBy{gate: client.ServerFingerprint(), self: peer.Fingerprint(cert.Leaf)}, err
}

// readCertificate reads one PEM certificate.
func readCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

// runConnect tries only to open the Unix socket, for the isolation checks:
// it reports success, EACCES or ENOENT and exits nonzero on failure.
func runConnect(args []string) (int, error) {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	socket := flags.String("socket", "", "Unix socket to connect to (required)")
	if err := flags.Parse(args); err != nil {
		return 1, err
	}
	if *socket == "" {
		return 1, errors.New("connect needs --socket")
	}
	conn, err := net.DialTimeout("unix", *socket, 5*time.Second)
	if err == nil {
		conn.Close()
		event("connect", map[string]any{"ok": true, "socket": *socket})
		return 0, nil
	}
	event("connect", map[string]any{"ok": false, "socket": *socket, "errno": errnoName(err), "error": err.Error()})
	return 3, nil
}

func errnoName(err error) string {
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return "EACCES"
	case errors.Is(err, syscall.ENOENT):
		return "ENOENT"
	}
	return "other"
}
