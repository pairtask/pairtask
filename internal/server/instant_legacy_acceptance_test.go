//go:build linux

package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// This intentionally implements the v1 wire exchange without Session.Serve:
// changing the production Session must not silently upgrade our old peer.
type legacyFrames struct{ messaging.Frames }

func (f legacyFrames) send(wire []byte, err error) error {
	if err != nil {
		return err
	}
	return f.Send(wire)
}

func (f legacyFrames) hello() error {
	wire, err := f.Recv()
	if err != nil {
		return err
	}
	hello, err := messaging.DecodeHello(wire)
	if err != nil {
		return err
	}
	if hello.Min > 1 || hello.Max < 1 {
		return fmt.Errorf("client no longer offers v1: %+v", hello)
	}
	return f.send(messaging.EncodeHello(messaging.Hello{Min: 1, Max: 1}))
}

func (f legacyFrames) receiveMessage() (*messaging.Message, error) {
	wire, err := f.Recv()
	if err != nil {
		return nil, err
	}
	kind, err := messaging.Kind(wire)
	if err != nil {
		return nil, err
	}
	if kind == messaging.FrameEnd {
		return nil, messaging.DecodeEnd(wire)
	}
	m, err := messaging.DecodeMessage(wire)
	if err != nil {
		return nil, err
	}
	return &m, f.send(messaging.EncodeStored(messaging.Stored{ID: m.ID}))
}

func (f legacyFrames) receiveMessages() ([]messaging.Message, error) {
	var messages []messaging.Message
	for {
		m, err := f.receiveMessage()
		if err != nil {
			return nil, err
		}
		if m == nil {
			return messages, nil
		}
		messages = append(messages, *m)
	}
}

func (f legacyFrames) finish() error {
	if err := f.send(messaging.EncodeEnd()); err != nil {
		return err
	}
	wire, err := f.Recv()
	if err != nil {
		return err
	}
	if err := messaging.DecodeEnd(wire); err != nil {
		return err
	}
	return f.send(messaging.EncodeEnd())
}

func legacyMessageRound(frames messaging.Frames) ([]messaging.Message, error) {
	f := legacyFrames{frames}
	if err := f.hello(); err != nil {
		return nil, err
	}
	messages, err := f.receiveMessages()
	if err != nil {
		return nil, err
	}
	return messages, f.finish()
}

type legacyRound struct {
	messages []messaging.Message
	err      error
}

type legacyAcceptor struct {
	listener net.Listener
	identity *peer.WorkspaceIdentity
	peerKey  string
}

func (a legacyAcceptor) round(ctx context.Context) legacyRound {
	raw, err := a.listener.Accept()
	if err != nil {
		return legacyRound{err: err}
	}
	defer raw.Close()
	conn, err := firstcontact.ServerWith(ctx, raw, a.identity, time.Now, firstcontact.ServerOptions{
		MessagePins: func(key string) bool { return key == a.peerKey },
	})
	if err != nil {
		return legacyRound{err: err}
	}
	defer conn.Close()
	rows, err := legacyMessageRound(conn.MessageFrames(ctx))
	return legacyRound{rows, err}
}

func (a legacyAcceptor) serve(ctx context.Context, rounds chan<- legacyRound) {
	for range 2 {
		result := a.round(ctx)
		rounds <- result
		if result.err != nil {
			return
		}
	}
}

func awaitLegacyRound(t *testing.T, rounds <-chan legacyRound, id string) {
	t.Helper()
	select {
	case result := <-rounds:
		operatorNoError(t, result.err)
		operatorEqual(t, "legacy round message count", len(result.messages), 1)
		operatorEqual(t, "legacy round ID", result.messages[0].ID, id)
	case <-time.After(3 * time.Second):
		t.Fatal("new dialer did not complete a v1 exchange")
	}
}

func TestInstantTransportAcceptanceNewDialerFallsBackToV1Peer(t *testing.T) {
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	joiner := newRedeemOperator(t, cfg, "")
	own, err := joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	remote := messageIdentity(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	operatorNoError(t, err)
	t.Cleanup(func() { ln.Close() })
	messageJoinedContact(joiner, remote.Fingerprint(), ln.Addr().String())
	rounds := make(chan legacyRound, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acceptor := legacyAcceptor{ln, remote, own.Identity.Fingerprint()}
	go acceptor.serve(ctx, rounds)
	side := instantSide{joiner, remote.Fingerprint()}
	for n := 0; n < 2; n++ {
		id := fmt.Sprintf("legacy-%d", n)
		side.send(t, id, "still reaches an old peer")
		awaitLegacyRound(t, rounds, id)
		awaitMessageDelivery(t, joiner.st.PeerMessages(), remote.Fingerprint(), id)
	}
}
