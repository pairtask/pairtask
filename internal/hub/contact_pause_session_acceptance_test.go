package hub

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type pauseRemoteServer struct {
	id       *peer.WorkspaceIdentity
	db       *store.Store
	listener net.Listener
	ready    chan struct{}
	done     chan error
	stored   chan struct{}
	release  chan struct{}
	gate     sync.Once
	hold     bool
}

func newPauseRemoteServer(t *testing.T, h *Hub, ctx context.Context, hold bool) *pauseRemoteServer {
	t.Helper()
	id := addressPeer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "pause-remote.db"))
	messageWorkerOK(t, err)
	t.Cleanup(func() { db.Close() })
	key, at := h.identity.Fingerprint(), time.Now()
	_, err = db.Commit(ctx, peer.PairingOutcome{OperationID: "remote-pair", InvitationID: "remote-invitation", IdentityKey: key, Name: "Peer", TranscriptID: make([]byte, 64), At: at})
	messageWorkerOK(t, err)
	_, err = db.Accept(ctx, key)
	messageWorkerOK(t, err)
	_, err = db.Deliver(ctx, key, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	messageWorkerOK(t, err)
	t.Cleanup(func() { listener.Close() })
	s := &pauseRemoteServer{id: id, db: db, listener: listener, ready: make(chan struct{}, 2), done: make(chan error, 4), stored: make(chan struct{}), release: make(chan struct{}), hold: hold}
	t.Cleanup(func() { addressRelease(s.release) })
	go func() {
		for {
			err := s.serve(ctx, key)
			s.done <- err
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
		}
	}()
	return s
}

type pauseStoredGate struct {
	messaging.Frames
	server *pauseRemoteServer
}

func (f pauseStoredGate) Send(frame []byte) error {
	kind, _ := messaging.Kind(frame)
	if f.server.hold && kind == messaging.FrameStored {
		f.server.gate.Do(func() { close(f.server.stored); <-f.server.release })
	}
	return f.Frames.Send(frame)
}

func (s *pauseRemoteServer) serve(ctx context.Context, key string) error {
	raw, err := s.listener.Accept()
	if err != nil {
		return err
	}
	defer raw.Close()
	conn, err := firstcontact.ServerWith(ctx, raw, s.id, time.Now, firstcontact.ServerOptions{MessagePins: func(fp string) bool { return fp == key }})
	if err != nil {
		return err
	}
	defer conn.Close()
	open := func(ctx context.Context) messaging.Frames {
		return pauseStoredGate{Frames: conn.MessageFrames(ctx), server: s}
	}
	session := messaging.Session{Frames: open(ctx), Store: s.db.PeerMessages(), Contact: key, Now: time.Now}
	_, version, err := session.OpenServe(ctx)
	if err != nil {
		return err
	}
	session.Version = version
	on, err := session.AnswerLive(true)
	if err != nil {
		return err
	}
	if !on {
		return errors.New("fixture did not negotiate a live session")
	}
	s.ready <- struct{}{}
	link := messaging.Link{Session: session, Open: open, Timing: messaging.LinkTiming{Round: 2 * time.Second, Grace: time.Second, Heartbeat: time.Second, Rotate: 1 << 20, MaxAge: time.Minute}}
	return link.RunServer(ctx)
}

func TestContactPauseHubSessionAcceptanceOutboundFenceSurvivesLiveHandoff(t *testing.T) {
	f := newAddressFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	remote := newPauseRemoteServer(t, f.h, ctx, false)
	f.key = remote.id.Fingerprint()
	addressContact(t, f.h, f.key)
	f.h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", remote.listener.Addr().String())
	}})
	c := f.contact(t)
	_, live, err := f.h.dialSession(ctx, c)
	messageWorkerOK(t, err)
	if live == nil {
		t.Fatal("outbound session did not hand off a live link")
	}
	addressAwait(t, remote.ready)
	// Keep the old socket: the store generation, not dropLive, is the fence.
	hubPauseContact(t, f.h, f.key, true)
	hubPauseContact(t, f.h, f.key, false)
	_, err = f.h.store.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.key, ID: "after-pause", Text: "must remain queued", Now: time.Now()})
	messageWorkerOK(t, err)
	f.h.goLive(ctx, live)
	t.Cleanup(func() { cancel(); f.h.pair.workers.Wait() })
	f.h.SyncMessages(f.key)
	addressAwait(t, remote.done)
	rows, _, err := f.h.store.PeerMessages().Page(context.Background(), f.key, 0, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].State, messaging.StateQueued)
	rows, _, err = remote.db.PeerMessages().Page(context.Background(), f.h.identity.Fingerprint(), 0, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 0)
}

func TestContactPauseHubSessionAcceptanceDelayedStoredIsFencedAndFreshSessionReplays(t *testing.T) {
	f := newAddressFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remote := newPauseRemoteServer(t, f.h, ctx, true)
	f.key = remote.id.Fingerprint()
	addressContact(t, f.h, f.key)
	f.h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", remote.listener.Addr().String())
	}})
	_, live, err := f.h.dialSession(ctx, f.contact(t))
	messageWorkerOK(t, err)
	if live == nil {
		t.Fatal("fixture did not enter outbound live mode")
	}
	addressAwait(t, remote.ready)
	f.h.goLive(ctx, live)
	t.Cleanup(func() { addressRelease(remote.release); cancel(); f.h.pair.workers.Wait() })
	_, err = f.h.store.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.key, ID: "in-flight", Text: "stored remotely before pause", Now: time.Now()})
	messageWorkerOK(t, err)
	f.h.SyncMessages(f.key)
	addressAwait(t, remote.stored)
	hubPauseContact(t, f.h, f.key, true)
	hubPauseContact(t, f.h, f.key, false)
	addressRelease(remote.release)
	addressAwait(t, remote.done)
	rows, _, err := f.h.store.PeerMessages().Page(ctx, f.key, 0, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].State, messaging.StateQueued)
	result, recovered, err := f.h.dialSession(ctx, f.contact(t))
	messageWorkerOK(t, err)
	if recovered == nil {
		t.Fatal("fresh session did not recover a live link")
	}
	defer recovered.conn.Close()
	defer f.h.releaseLive(f.key, recovered.link)
	messageSyncEqual(t, result.Sent, 1)
	rows, _, err = f.h.store.PeerMessages().Page(ctx, f.key, 0, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, rows[0].State, messaging.StateDelivered)
	rows, _, err = remote.db.PeerMessages().Page(ctx, f.h.identity.Fingerprint(), 0, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].ID, "in-flight")
}

func TestContactPauseHubSessionAcceptancePausedAdmissionPreventsOutboundDial(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	var calls atomic.Int32
	f.h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(context.Context, string) (net.Conn, error) { calls.Add(1); return nil, context.Canceled }})
	hubPauseContact(t, f.h, f.key, true)
	_, _, err := f.h.dialSession(context.Background(), f.contact(t))
	messageSyncEqual(t, errors.Is(err, store.ErrPaused), true)
	messageSyncEqual(t, calls.Load(), int32(0))
}
