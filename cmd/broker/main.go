// Command broker runs the collab-ai UDS message broker for local AI agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
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
	// The secured/connector route: a group-shared pairing socket (GID -1
	// when unset) and connector's egress socket.
	PairingSocketGroup  string
	PairingSocketGID    int
	PairingEgressSocket string
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

// openHub loads the state and prepares the hub, pairing included, before
// it runs.
func openHub(ctx context.Context, cfg config, st *store.Store, log *slog.Logger) (*hub.Hub, error) {
	lastSeq, err := st.LastSeq(ctx)
	if err != nil {
		return nil, err
	}
	log.Info("state loaded", "db", cfg.DBPath, "last_seq", lastSeq)
	h := hub.New(st, lastSeq, log)
	h.SetPairing(pairingConfig(cfg))
	n, err := h.RecoverJoins(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover pairing redemptions: %w", err)
	}
	if n > 0 {
		log.Info("pairing redemptions interrupted before verification marked unverified", "count", n)
	}
	return h, nil
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	h, err := openHub(ctx, cfg, st, log)
	if err != nil {
		return err
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

	pairingDone := pairingEndpoint{cfg: cfg, h: h, log: log}.start(ctx, stop)

	srv := server.New(cfg.SocketPath, h, log)
	defer srv.Close()

	err = srv.Listen(ctx)
	stop()
	err = errors.Join(err, <-operatorDone, <-pairingDone)
	<-h.Done()
	log.Info("shutdown complete")
	return err
}
