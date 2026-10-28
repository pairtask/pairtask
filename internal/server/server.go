// Package server accepts UDS connections and bridges them to the hub.
package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"

	"github.com/google/uuid"
)

const (
	helloTimeout = 10 * time.Second
	writeTimeout = 5 * time.Second
	sendBufSize  = 64
)

// Server listens on a Unix domain socket.
type Server struct {
	socketPath string
	hub        *hub.Hub
	log        *slog.Logger
	mu         sync.Mutex
	ln         *net.UnixListener
	socketInfo os.FileInfo
	cancel     context.CancelFunc
	closed     bool
	handle     func(context.Context, net.Conn)
	// prepare checks the path before binding, and secure the socket after;
	// both are optional.
	prepare func(path string) error
	secure  func(path string) error
}

// New creates a Server bound to socketPath (not yet listening).
func New(socketPath string, h *hub.Hub, log *slog.Logger) *Server {
	s := &Server{socketPath: socketPath, hub: h, log: log}
	s.handle = s.handleConn
	return s
}

// Listen starts accepting connections until ctx is cancelled.
func (s *Server) Listen(ctx context.Context) error {
	s.mu.Lock()
	if s.closed || s.ln != nil {
		s.mu.Unlock()
		return errors.New("server already started or closed")
	}
	// Never unlink an existing path. Even a stale socket must be removed explicitly.
	// Lstat also rejects dangling symlinks, which bind may follow on some systems.
	if _, err := os.Lstat(s.socketPath); !errors.Is(err, os.ErrNotExist) {
		s.mu.Unlock()
		if err == nil {
			return fmt.Errorf("socket path already exists: %s", s.socketPath)
		}
		return fmt.Errorf("inspect socket path: %w", err)
	}
	if err := s.run(s.prepare); err != nil {
		s.mu.Unlock()
		return err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.socketPath, Net: "unix"})
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := s.run(s.secure); err != nil {
		ln.Close()
		os.Remove(s.socketPath)
		s.mu.Unlock()
		return err
	}
	info, err := os.Lstat(s.socketPath)
	if err != nil {
		ln.Close()
		s.mu.Unlock()
		return fmt.Errorf("stat socket: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	s.ln = ln
	s.socketInfo = info
	s.cancel = cancel
	s.mu.Unlock()
	s.log.Info("listening", "socket", s.socketPath)

	stop := context.AfterFunc(ctx, func() { s.Close() })
	var connections sync.WaitGroup
	defer func() {
		s.Close()
		stop()
		connections.Wait()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil // shutting down
			}
			return fmt.Errorf("accept: %w", err)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			s.handle(ctx, conn)
		}()
	}
}

// run applies an optional path hook.
func (s *Server) run(hook func(string) error) error {
	if hook == nil {
		return nil
	}
	return hook(s.socketPath)
}

// Close stops acceptance and cancels connections. Listen waits for their cleanup.
// Only the socket created by this Server may be removed.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	if s.ln == nil {
		return nil
	}
	err := s.ln.Close()
	info, statErr := os.Lstat(s.socketPath)
	if statErr == nil && os.SameFile(s.socketInfo, info) {
		return errors.Join(err, os.Remove(s.socketPath))
	}
	if errors.Is(statErr, os.ErrNotExist) {
		statErr = nil
	}
	return errors.Join(err, statErr)
}

// handleConn runs the lifecycle of one agent connection:
// hello handshake, then concurrent read and write pumps.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	// One buffered reader per connection: frames must be decoded sequentially
	// from a single source, otherwise a decoder's read-ahead buffer would
	// swallow subsequent frames.
	r := bufio.NewReader(conn)

	client, err := s.handshake(ctx, conn, r)
	if err != nil {
		s.log.Warn("handshake failed", "remote", conn.RemoteAddr(), "error", err)
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrExpectedHello, Detail: err.Error()})
		return
	}

	if client == nil { // A one-shot status request has already been answered.
		return
	}
	client.Disconnect = func() {
		cancel()
		conn.Close()
	}
	if !s.hub.Register(ctx, client) {
		return
	}

	writeDone := make(chan struct{})
	go s.writePump(ctx, client, conn, writeDone)

	s.readPump(ctx, client, r)
	cancel()
	conn.Close()
	s.hub.Unregister(client)
	<-writeDone
}

// handshake handles a one-shot status request or prepares a messaging hello.
func (s *Server) handshake(ctx context.Context, conn net.Conn, r *bufio.Reader) (*hub.Client, error) {
	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	defer conn.SetReadDeadline(time.Time{})

	var msg protocol.Message
	if err := protocol.ReadFrame(r, &msg); err != nil {
		return nil, fmt.Errorf("read hello: %w", err)
	}
	if msg.Type == protocol.TypeStatus {
		s.writeStatus(ctx, conn)
		return nil, nil
	}
	if msg.Type != protocol.TypeHello {
		return nil, fmt.Errorf("first frame must be %q, got %q", protocol.TypeHello, msg.Type)
	}
	if err := protocol.ValidateAgentID(msg.AgentID); err != nil {
		return nil, err
	}
	for _, room := range msg.Rooms {
		if err := protocol.ValidateRoomID(room); err != nil {
			return nil, err
		}
	}

	return &hub.Client{
		JoinRooms:       msg.Rooms,
		ID:              msg.AgentID,
		SessionID:       uuid.NewString(),
		Harness:         msg.Harness,
		Model:           msg.Model,
		ProtocolVersion: min(msg.ProtocolVersion, protocol.Version),
		Send:            make(chan protocol.Message, sendBufSize),
	}, nil
}

// readPump decodes frames from the connection and submits them to the hub.
func (s *Server) readPump(ctx context.Context, client *hub.Client, r *bufio.Reader) {
	for {
		var msg protocol.Message
		if err := protocol.ReadFrame(r, &msg); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("read error", "agent_id", client.ID, "error", err)
			}
			return
		}
		if !s.hub.Submit(ctx, hub.Inbound{From: client, Msg: msg}) {
			return
		}
	}
}

// writePump encodes frames from the client's Send channel to the connection.
// Either pump ending closes the connection so its peer cannot remain blocked.
func (s *Server) writePump(ctx context.Context, client *hub.Client, conn net.Conn, done chan<- struct{}) {
	defer close(done)
	defer conn.Close()
	w := bufio.NewWriter(conn)
	for {
		var msg protocol.Message
		select {
		case <-ctx.Done():
			return
		case next, ok := <-client.Send:
			if !ok {
				return
			}
			msg = next
		}
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return
		}
		if err := protocol.WriteFrame(w, msg); err != nil {
			s.log.Debug("write error", "agent_id", client.ID, "error", err)
			return
		}
	}
}
