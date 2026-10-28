package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/egress"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/server"
)

// loadPairing reads the experimental pairing settings (#178); all are off
// by default.
func loadPairing(lookup settings.LookupFunc, cfg *config) error {
	cfg.PairingSocketPath, _, _ = settings.LookupIn(lookup, "PAIRING_SOCKET")
	cfg.PairingEgress, _, _ = settings.LookupIn(lookup, "PAIRING_EGRESS")
	cfg.PairingName, _, _ = settings.LookupIn(lookup, "PAIRING_NAME")
	cfg.PairingSocketGroup, _, _ = settings.LookupIn(lookup, "PAIRING_SOCKET_GROUP")
	cfg.PairingSocketGID = -1
	for _, step := range []func(*config) error{parseEgress, checkPairingChoices, resolveSocketGroup, checkPairingSocket, checkSharedSeparation} {
		if err := step(cfg); err != nil {
			return err
		}
	}
	return nil
}

// parseEgress accepts no egress, "direct", or "socket:/ABSOLUTE/PATH".
func parseEgress(cfg *config) error {
	path, isSocket := strings.CutPrefix(cfg.PairingEgress, "socket:")
	switch {
	case cfg.PairingEgress == "" || cfg.PairingEgress == "direct":
		return nil
	case isSocket && filepath.IsAbs(path):
		cfg.PairingEgressSocket = filepath.Clean(path)
		return nil
	}
	return errors.New(settings.Name("PAIRING_EGRESS") + " must be empty, \"direct\" or \"socket:/ABSOLUTE/PATH\"")
}

// resolveSocketGroup turns PAIRING_SOCKET_GROUP into a GID; it shares the
// pairing socket, so it needs one.
func resolveSocketGroup(cfg *config) error {
	if cfg.PairingSocketGroup == "" {
		return nil
	}
	if cfg.PairingSocketPath == "" {
		return errors.New(settings.Name("PAIRING_SOCKET_GROUP") + " requires " + settings.Name("PAIRING_SOCKET"))
	}
	group, err := user.LookupGroup(cfg.PairingSocketGroup)
	if err != nil {
		return fmt.Errorf("%s: %w", settings.Name("PAIRING_SOCKET_GROUP"), err)
	}
	cfg.PairingSocketGID, err = strconv.Atoi(group.Gid)
	return err
}

func checkPairingChoices(cfg *config) error {
	if cfg.PairingName != "" && peer.CheckPairingName(cfg.PairingName) != nil {
		return errors.New(settings.Name("PAIRING_NAME") + " must be at most 64 printable bytes")
	}
	return nil
}

// checkPairingSocket keeps the pairing socket apart from the agent and
// operator sockets.
func checkPairingSocket(cfg *config) error {
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
	pc.Dial = egressDial(cfg)
	return pc
}

// egressDial is how a redemption reaches its destination: through
// connector's forwarder, directly, or not at all.
func egressDial(cfg config) func(ctx context.Context, destination string) (net.Conn, error) {
	if cfg.PairingEgressSocket != "" {
		socket := cfg.PairingEgressSocket
		return func(ctx context.Context, destination string) (net.Conn, error) {
			return egress.Dial(ctx, socket, destination)
		}
	}
	if cfg.PairingEgress == "direct" {
		return func(ctx context.Context, destination string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", destination)
		}
	}
	return nil
}

// checkSharedSeparation keeps the shared route's socket directories away
// from the broker's own: agent and operator sockets, the database and the
// workspace key. Paths are compared cleaned, with symlinks resolved.
func checkSharedSeparation(cfg *config) error {
	shared := sharedDirs(*cfg)
	if len(shared) == 0 {
		return nil
	}
	private := privateDirs(*cfg)
	for _, dir := range shared {
		if slices.ContainsFunc(private, func(p string) bool { return overlaps(dir, p) }) {
			return fmt.Errorf("the shared pairing or egress socket directory %s must neither be nor contain nor sit inside the agent, operator, database and key directories", dir)
		}
	}
	return nil
}

// overlaps reports two directories where either is, or contains, the other.
func overlaps(a, b string) bool {
	return within(a, b) || within(b, a)
}

// sharedDirs are the directories the shared route exposes to connector.
func sharedDirs(cfg config) []string {
	var dirs []string
	if cfg.PairingSocketGID >= 0 {
		dirs = append(dirs, resolved(filepath.Dir(cfg.PairingSocketPath)))
	}
	if cfg.PairingEgressSocket != "" {
		dirs = append(dirs, resolved(filepath.Dir(cfg.PairingEgressSocket)))
	}
	return dirs
}

// privateDirs are the broker's own directories, by their configured paths
// and, for the database, where a symlinked file actually lives.
func privateDirs(cfg config) []string {
	dirs := []string{resolved(filepath.Dir(cfg.SocketPath)), resolved(filepath.Dir(cfg.DBPath)),
		filepath.Dir(resolved(cfg.DBPath)), resolved(cfg.DBPath + ".peer")}
	if cfg.OperatorSocketPath != "" {
		dirs = append(dirs, resolved(filepath.Dir(cfg.OperatorSocketPath)))
	}
	return dirs
}

// resolved is a path made absolute and, where it exists, with symlinks
// resolved, so relative and aliased paths compare alike.
func resolved(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// within reports dir equal to or inside parent.
func within(dir, parent string) bool {
	rel, err := filepath.Rel(parent, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	if p.cfg.PairingSocketGID >= 0 {
		srv = server.NewSharedPairing(p.cfg.PairingSocketPath, p.cfg.PairingSocketGID, p.h, p.log)
	}
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
