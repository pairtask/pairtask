package server

import (
	"context"
	"log/slog"
	"net"
	"os"

	"github.com/pairtask/pairtask/internal/hub"
)

// NewPrivate is an agent server on a socket in a private directory: the
// directory must be ours with mode 0700, the socket is made 0600, and each
// connection is served only when the kernel says its peer is this same
// user, so only this user's processes reach it. The Desktop node serves
// the agents on its Mac so.
func NewPrivate(socketPath string, h *hub.Hub, log *slog.Logger) *Server {
	s := New(socketPath, h, log)
	s.prepare = privateDir
	s.secure = func(path string) error { return os.Chmod(path, 0o600) }
	serve := s.handle
	s.handle = func(ctx context.Context, conn net.Conn) {
		if !samePeer(conn, os.Getuid()) {
			log.Warn("refused an agent connection from another user", "socket", socketPath)
			conn.Close()
			return
		}
		serve(ctx, conn)
	}
	return s
}

// samePeer: the kernel reports conn's peer as uid; unknown is refused.
func samePeer(conn net.Conn, uid int) bool {
	got, err := peerUID(conn)
	return err == nil && got == uid
}
