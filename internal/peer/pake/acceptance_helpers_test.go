package pake_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func requireEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
func requireFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unexpected success")
	}
}
func binding() peer.ExchangeBinding {
	return peer.ExchangeBinding{InvitationID: "invitation", JoinerOperationID: "join-operation", InviterOperationID: "invite-operation", Inviter: "sha256:" + strings.Repeat("1", 64), Joiner: "sha256:" + strings.Repeat("2", 64), JoinerName: "Bob", ChannelBinding: bytes.Repeat([]byte{7}, 32)}
}

type exchangeCase struct {
	joinBinding, inviteBinding peer.ExchangeBinding
	joinCode, inviteCode       []byte
	persistErr                 error
	mutateJoin, mutateInvite   func(int, []byte) ([]byte, error)
}

func newCase() exchangeCase {
	return exchangeCase{joinBinding: binding(), inviteBinding: binding(), joinCode: []byte("928374"), inviteCode: []byte("928374")}
}

type recordingFrames struct {
	ctx     context.Context
	in, out chan []byte
	sent    [][]byte
	mutate  func(int, []byte) ([]byte, error)
}

func (f *recordingFrames) Send(b []byte) error {
	b = bytes.Clone(b)
	f.sent = append(f.sent, bytes.Clone(b))
	if f.mutate != nil {
		var err error
		b, err = f.mutate(len(f.sent), b)
		if err != nil {
			return err
		}
	}
	select {
	case f.out <- b:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *recordingFrames) Recv() ([]byte, error) {
	select {
	case b, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}
		return bytes.Clone(b), nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

type exchangeRun struct {
	joinResult, inviteResult peer.ExchangeResult
	joinErr, inviteErr       error
	persistCalls             int
	joinSent, inviteSent     [][]byte
}
type sideResult struct {
	join   bool
	result peer.ExchangeResult
	calls  int
	err    error
}

func exchange(t *testing.T, c exchangeCase) exchangeRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := make(chan []byte, 4), make(chan []byte, 4)
	j := &recordingFrames{ctx: ctx, in: a, out: b, mutate: c.mutateJoin}
	i := &recordingFrames{ctx: ctx, in: b, out: a, mutate: c.mutateInvite}
	ex := pake.New()
	results := make(chan sideResult, 2)
	go func() {
		defer close(b)
		var saved peer.ExchangeResult
		calls := 0
		err := ex.Join(ctx, j, c.joinBinding, c.joinCode, func(_ context.Context, r peer.ExchangeResult) error {
			calls++
			if len(j.sent) != 1 {
				return errors.New("final confirmation sent before persist")
			}
			if c.persistErr != nil {
				return c.persistErr
			}
			saved = peer.ExchangeResult{Peer: r.Peer, TranscriptID: bytes.Clone(r.TranscriptID)}
			return nil
		})
		results <- sideResult{join: true, result: saved, calls: calls, err: err}
	}()
	go func() {
		defer close(a)
		r, err := ex.Invite(ctx, i, c.inviteBinding, c.inviteCode)
		results <- sideResult{result: r, err: err}
	}()
	var out exchangeRun
	for range 2 {
		r := <-results
		if r.join {
			out.joinResult, out.joinErr, out.persistCalls = r.result, r.err, r.calls
		} else {
			out.inviteResult, out.inviteErr = r.result, r.err
		}
	}
	out.joinSent, out.inviteSent = j.sent, i.sent
	return out
}

type scriptFrames struct {
	input, sent [][]byte
	reads       int
}

func (s *scriptFrames) Send(b []byte) error { s.sent = append(s.sent, bytes.Clone(b)); return nil }
func (s *scriptFrames) Recv() ([]byte, error) {
	s.reads++
	if len(s.input) == 0 {
		return nil, io.EOF
	}
	b := s.input[0]
	s.input = s.input[1:]
	return bytes.Clone(b), nil
}
