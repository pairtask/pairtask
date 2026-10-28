package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/server"
)

// loadPairing reads the experimental pairing settings (#178); all are off
// by default.
func loadPairing(lookup settings.LookupFunc, cfg *config) error {
	cfg.PairingSocketPath, _, _ = settings.LookupIn(lookup, "PAIRING_SOCKET")
	cfg.PairingEgress, _, _ = settings.LookupIn(lookup, "PAIRING_EGRESS")
	cfg.PairingName, _, _ = settings.LookupIn(lookup, "PAIRING_NAME")
	if err := checkPairingChoices(*cfg); err != nil {
		return err
	}
	return checkPairingSocket(*cfg)
}

func checkPairingChoices(cfg config) error {
	if cfg.PairingEgress != "" && cfg.PairingEgress != "direct" {
		return errors.New(settings.Name("PAIRING_EGRESS") + " must be empty or \"direct\"")
	}
	if cfg.PairingName != "" && peer.CheckPairingName(cfg.PairingName) != nil {
		return errors.New(settings.Name("PAIRING_NAME") + " must be at most 64 printable bytes")
	}
	return nil
}

// checkPairingSocket keeps the pairing socket apart from the agent and
// operator sockets.
func checkPairingSocket(cfg config) error {
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

// pairingEndpoint is the inbound pairing socket, when configured.
type pairingEndpoint struct {
	cfg config
	h   *hub.Hub
	log *slog.Logger
}

// start runs the pairing socket; a broken one stops the broker, as a broken
// operator endpoint does.
func (p pairingEndpoint) start(ctx context.Context, stop func()) <-chan error {
	done := make(chan error, 1)
	if p.cfg.PairingSocketPath == "" {
		done <- nil
		return done
	}
	srv := server.NewPairing(p.cfg.PairingSocketPath, p.h, p.log)
	go func() {
		defer srv.Close()
		err := srv.Listen(ctx)
		if err != nil {
			err = fmt.Errorf("pairing endpoint: %w", err)
			stop()
		}
		done <- err
	}()
	return done
}
