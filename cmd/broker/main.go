// Command broker runs the collab-ai UDS message broker for local AI agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := loadConfig(os.LookupEnv)
	if err == nil {
		err = run(cfg, log)
	}
	if err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

type config struct {
	SocketPath string
	DBPath     string
	// OperatorSocketPath is empty when the operator endpoint is disabled.
	OperatorSocketPath string
	OperatorUID        int
	// Pairing (#178, experimental): an empty socket path disables inbound
	// pairing; an empty egress disables redeeming other workspaces' codes.
	PairingSocketPath string
	PairingEgress     string
	PairingName       string
}

func loadConfig(lookup settings.LookupFunc) (config, error) {
	var cfg config
	var err error
	if cfg.SocketPath, err = settings.Required(lookup, "SOCKET_PATH", "/tmp/collab-ai.sock"); err != nil {
		return cfg, err
	}
	if cfg.DBPath, err = settings.Required(lookup, "DB_PATH", "./collab-ai.db"); err != nil {
		return cfg, err
	}
	if err := loadOperator(lookup, &cfg); err != nil {
		return cfg, err
	}
	return cfg, loadPairing(lookup, &cfg)
}

func loadOperator(lookup settings.LookupFunc, cfg *config) error {
	// The operator endpoint is optional: unset or empty disables it.
	cfg.OperatorSocketPath, _, _ = settings.LookupIn(lookup, "OPERATOR_SOCKET_PATH")
	uid, _, _ := settings.LookupIn(lookup, "OPERATOR_UID")
	if cfg.OperatorSocketPath == "" {
		if uid != "" {
			return errors.New(settings.Name("OPERATOR_UID") + " requires " + settings.Name("OPERATOR_SOCKET_PATH"))
		}
		return nil
	}
	parsed, err := strconv.Atoi(uid)
	if err != nil || parsed < 0 {
		return errors.New(settings.Name("OPERATOR_SOCKET_PATH") + " requires " + settings.Name("OPERATOR_UID") +
			", a non-negative numeric UID")
	}
	cfg.OperatorUID = parsed
	if filepath.Clean(cfg.OperatorSocketPath) == filepath.Clean(cfg.SocketPath) {
		return errors.New("the operator socket must differ from the agent socket")
	}
	return nil
}

// loadPairing reads the experimental pairing settings; all are off by
// default.
func loadPairing(lookup settings.LookupFunc, cfg *config) error {
	cfg.PairingSocketPath, _, _ = settings.LookupIn(lookup, "PAIRING_SOCKET")
	cfg.PairingEgress, _, _ = settings.LookupIn(lookup, "PAIRING_EGRESS")
	cfg.PairingName, _, _ = settings.LookupIn(lookup, "PAIRING_NAME")
	if cfg.PairingEgress != "" && cfg.PairingEgress != "direct" {
		return errors.New(settings.Name("PAIRING_EGRESS") + " must be empty or \"direct\"")
	}
	if cfg.PairingName != "" && peer.CheckPairingName(cfg.PairingName) != nil {
		return errors.New(settings.Name("PAIRING_NAME") + " must be at most 64 printable bytes")
	}
	if cfg.PairingSocketPath == "" {
		return nil
	}
	pairing := filepath.Clean(cfg.PairingSocketPath)
	for _, other := range []string{cfg.SocketPath, cfg.OperatorSocketPath} {
		if other != "" && pairing == filepath.Clean(other) {
			return errors.New("the pairing socket must differ from the agent and operator sockets")
		}
	}
	return nil
}

// pairingConfig wires the experimental CPace pairing only when inbound or
// egress is configured, so a broker that doesn't pair never creates a
// workspace key at startup. Direct egress dials TCP from this broker, which
// suits a standalone broker only.
func pairingConfig(cfg config) hub.PairingConfig {
	pc := hub.PairingConfig{Name: cfg.PairingName}
	if cfg.PairingSocketPath == "" && cfg.PairingEgress == "" {
		return pc
	}
	pc.Exchanger = pake.New()
	if cfg.PairingEgress == "direct" {
		pc.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", destination)
		}
	}
	return pc
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	lastSeq, err := st.LastSeq(ctx)
	if err != nil {
		return err
	}
	log.Info("state loaded", "db", cfg.DBPath, "last_seq", lastSeq)

	h := hub.New(st, lastSeq, log)
	h.SetPairing(pairingConfig(cfg))
	if n, err := h.RecoverJoins(ctx); err != nil {
		return fmt.Errorf("recover pairing redemptions: %w", err)
	} else if n > 0 {
		log.Info("pairing redemptions interrupted before verification marked unverified", "count", n)
	}
	go h.Run(ctx)
	defer func() {
		stop()
		<-h.Done()
	}()

	operatorDone := make(chan error, 1)
	if cfg.OperatorSocketPath == "" {
		operatorDone <- nil
	} else {
		op := server.NewOperator(cfg.OperatorSocketPath, h, cfg.OperatorUID, log)
		defer op.Close()
		go func() {
			err := op.Listen(ctx)
			if err != nil {
				err = fmt.Errorf("operator endpoint: %w", err)
				stop() // The broker does not run with a broken operator endpoint.
			}
			operatorDone <- err
		}()
	}

	pairingDone := startPairing(ctx, cfg, h, log, stop)

	srv := server.New(cfg.SocketPath, h, log)
	defer srv.Close()

	err = srv.Listen(ctx)
	stop()
	err = errors.Join(err, <-operatorDone, <-pairingDone)
	<-h.Done()
	log.Info("shutdown complete")
	return err
}

// startPairing runs the inbound pairing socket when configured; a broken
// one stops the broker, as a broken operator endpoint does.
func startPairing(ctx context.Context, cfg config, h *hub.Hub, log *slog.Logger, stop func()) <-chan error {
	done := make(chan error, 1)
	if cfg.PairingSocketPath == "" {
		done <- nil
		return done
	}
	p := server.NewPairing(cfg.PairingSocketPath, h, log)
	go func() {
		defer p.Close()
		err := p.Listen(ctx)
		if err != nil {
			err = fmt.Errorf("pairing endpoint: %w", err)
			stop()
		}
		done <- err
	}()
	return done
}
