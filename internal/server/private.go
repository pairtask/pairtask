package server

import (
	"log/slog"
	"os"

	"github.com/pairtask/pairtask/internal/hub"
)

// NewPrivate is an agent server on a socket in a private directory: the
// directory must be ours with mode 0700, and the socket is made 0600, so
// only this user's processes reach it. The Desktop node serves the agents
// on its Mac so.
func NewPrivate(socketPath string, h *hub.Hub, log *slog.Logger) *Server {
	s := New(socketPath, h, log)
	s.prepare = privateDir
	s.secure = func(path string) error { return os.Chmod(path, 0o600) }
	return s
}
