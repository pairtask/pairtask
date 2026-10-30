//go:build linux

package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

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
	type round struct {
		messages []messaging.Message
		err      error
	}
	rounds := make(chan round, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		for range 2 {
			raw, err := ln.Accept()
			if err != nil {
				rounds <- round{err: err}
				return
			}
			conn, err := firstcontact.ServerWith(ctx, raw, remote, time.Now, firstcontact.ServerOptions{
				MessagePins: func(key string) bool { return key == own.Identity.Fingerprint() },
			})
			if err != nil {
				raw.Close()
				rounds <- round{err: err}
				return
			}
			rows, err := legacyMessageRound(conn.MessageFrames(ctx))
			conn.Close()
			rounds <- round{rows, err}
			if err != nil {
				return
			}
		}
	}()
	for n := 0; n < 2; n++ {
		id := fmt.Sprintf("legacy-%d", n)
		instantSend(t, joiner, remote.Fingerprint(), id, "still reaches an old peer")
		select {
		case result := <-rounds:
			operatorNoError(t, result.err)
			if len(result.messages) != 1 || result.messages[0].ID != id {
				t.Fatalf("wrong legacy round: %+v", result.messages)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("new dialer did not complete a v1 exchange")
		}
		awaitMessageDelivery(t, joiner.st.PeerMessages(), remote.Fingerprint(), id)
	}
}
