// Command peerprobe runs the #178 peer probe's pieces on real containers:
// the TLS gate inside secured, the opaque relay in the online connector, a
// client that sends one contact request, and a bare Unix connect check. It
// is a validation tool for the disposable CI topology, never shipped in an
// image or installed as a service.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

const usage = "usage: peerprobe gate|relay|send|connect [flags]"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var code int
	var err error
	switch os.Args[1] {
	case "gate":
		err = runGate(ctx, os.Args[2:])
	case "relay":
		err = runRelay(ctx, os.Args[2:])
	case "send":
		code, err = runSend(ctx, os.Args[2:])
	case "connect":
		code, err = runConnect(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// event is one JSON line on stdout; the runner selects lines by "event".
func event(name string, fields map[string]any) {
	fields["event"] = name
	encoded, _ := json.Marshal(fields)
	fmt.Println(string(encoded))
}

// runGate serves the TLS gate on a Unix socket, with a self-signed identity
// kept under --state, a private directory outside the shared IPC volume.
func runGate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	socket := flags.String("socket", "", "Unix socket to listen on (required)")
	state := flags.String("state", "", "private directory for the gate's identity (required)")
	discoverable := flags.Bool("discoverable", true, "answer contact requests; false refuses them")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *socket == "" || *state == "" {
		return errors.New("gate needs --socket and --state")
	}
	cert, err := identity(*state, "gate")
	if err != nil {
		return err
	}
	gate, err := peer.NewGate(peer.GateConfig{
		Cert:         cert,
		Store:        peer.NewMemStore(),
		Discoverable: func() bool { return *discoverable },
		OnConnection: func(info peer.ConnInfo) {
			fields := map[string]any{"fingerprint": info.Fingerprint, "uid": -1, "gid": -1}
			if info.HasCreds {
				fields["uid"], fields["gid"] = info.UID, info.GID
			}
			event("peer_credentials", fields)
		},
	})
	if err != nil {
		return err
	}
	_ = os.Remove(*socket)
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(*socket, 0o660); err != nil {
		return err
	}
	event("gate_ready", map[string]any{"socket": *socket, "fingerprint": peer.Fingerprint(cert.Leaf)})
	return gate.Serve(ctx, ln)
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

// runSend sends one contact request through a relay and prints the reply.
// Exit 0 for pending or duplicate, 2 for refused, 1 for a transport or pin
// failure.
func runSend(ctx context.Context, args []string) (int, error) {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	addr := flags.String("addr", "", "the relay's host:port (required)")
	pin := flags.String("pin", "", "the gate's expected fingerprint (required)")
	state := flags.String("state", "", "private directory for this side's identity (required)")
	id := flags.String("request-id", "", "request id (required)")
	note := flags.String("note", "", "note to send")
	name := flags.String("name", "probe", "display name")
	if err := flags.Parse(args); err != nil {
		return 1, err
	}
	if *addr == "" || *pin == "" || *state == "" || *id == "" {
		return 1, errors.New("send needs --addr, --pin, --state and --request-id")
	}
	cert, err := identity(*state, "client")
	if err != nil {
		return 1, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := peer.Dial(ctx, *addr, peer.ClientConfig{Cert: cert, ServerPin: *pin})
	if err != nil {
		return 1, err
	}
	defer client.Close()
	reply, err := client.Send(ctx, peer.Record{Version: peer.SchemaVersion, Kind: peer.KindRequest, RequestID: *id,
		From: peer.Identity{Name: *name, Workspace: "probe"}, Note: *note})
	if err != nil {
		return 1, err
	}
	event("reply", map[string]any{"request_id": reply.RequestID, "status": reply.Status, "reason": reply.Reason,
		"gate_fingerprint": client.ServerFingerprint(), "client_fingerprint": peer.Fingerprint(cert.Leaf)})
	if reply.Status == peer.StatusRefused {
		return 2, nil
	}
	return 0, nil
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
	name := "other"
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		name = "EACCES"
	case errors.Is(err, syscall.ENOENT):
		name = "ENOENT"
	}
	event("connect", map[string]any{"ok": false, "socket": *socket, "errno": name, "error": err.Error()})
	return 3, nil
}

// identity loads or creates a self-signed certificate under dir.
func identity(dir, name string) (tls.Certificate, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	certPath, keyPath := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}
	cert, err := peer.SelfSigned(name)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM, keyPEM, err := peer.EncodePEM(cert)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}
