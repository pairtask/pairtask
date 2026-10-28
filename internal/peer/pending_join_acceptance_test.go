//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func pendingRequest() peer.JoinRequest {
	return peer.JoinRequest{OperationID: "operator-command", Destination: "colleague.example:443", Name: "Bob", Code: []byte("739-281")}
}
func pendingJoiner(t *testing.T, r *redeemFixture) *peer.Joiner {
	t.Helper()
	j, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, bytes.NewReader(nil))
	invOK(t, err)
	return j
}
func TestPendingJoinAcceptanceIntentBeforeConnectionAndReplay(t *testing.T) {
	r := newRedeemFixture(t)
	j := pendingJoiner(t, r)
	req := pendingRequest()
	p, row, err := j.Begin(context.Background(), req)
	invOK(t, err)
	invEqual(t, p != nil, true)
	invEqual(t, row.OperationID, req.OperationID)
	invEqual(t, row.State, peer.JoinIntent)
	invEqual(t, row.Destination, req.Destination)
	invEqual(t, row.Peer, "")
	invEqual(t, len(redeemRows(t, r.joins)), 1)
	req.Code = []byte("111111")
	replay, same, err := j.Begin(context.Background(), req)
	invOK(t, err)
	invEqual(t, replay == nil, true)
	invEqual(t, same.OperationID, row.OperationID)
	invEqual(t, len(redeemRows(t, r.joins)), 1)
	for _, mutate := range []func(*peer.JoinRequest){func(q *peer.JoinRequest) { q.Name = "Mallory" }, func(q *peer.JoinRequest) { q.Destination = "other.example:443" }} {
		changed := req
		mutate(&changed)
		_, _, err := j.Begin(context.Background(), changed)
		invEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
	}
	invEqual(t, r.joinEx.confirmations, 0)
}

type pendingNoIO struct{ calls int }

func (f *pendingNoIO) Send([]byte) error     { f.calls++; return io.ErrUnexpectedEOF }
func (f *pendingNoIO) Recv() ([]byte, error) { f.calls++; return nil, io.ErrUnexpectedEOF }

func TestPendingJoinAcceptanceAbandonIsDurableAndSingleUse(t *testing.T) {
	r := newRedeemFixture(t)
	path := filepath.Join(t.TempDir(), "joins.db")
	db := cpaceSQLite(t, path)
	r.joins = db
	j := pendingJoiner(t, r)
	p, _, err := j.Begin(context.Background(), pendingRequest())
	invOK(t, err)
	row, err := p.Abandon(context.Background(), context.Canceled)
	invEqual(t, errors.Is(err, context.Canceled), true)
	invEqual(t, row.State, peer.JoinFailed)
	_, err = p.Abandon(context.Background(), io.EOF)
	invEqual(t, errors.Is(err, peer.ErrJoinState), true)
	f := &pendingNoIO{}
	_, err = p.Run(context.Background(), f, peer.Connection{})
	invEqual(t, errors.Is(err, peer.ErrJoinState), true)
	invEqual(t, f.calls, 0)
	invOK(t, db.Close())
	r.joins = cpaceSQLite(t, path)
	_, err = r.joins.RecoverJoins(context.Background(), r.inv.clock.read())
	invOK(t, err)
	replay, restored, err := pendingJoiner(t, r).Begin(context.Background(), pendingRequest())
	invOK(t, err)
	invEqual(t, replay == nil, true)
	invEqual(t, restored.State, peer.JoinFailed)
	invEqual(t, restored.Destination, pendingRequest().Destination)
}

func TestPendingJoinAcceptanceInvalidConnectionFailsBeforeIO(t *testing.T) {
	r := newRedeemFixture(t)
	p, _, err := pendingJoiner(t, r).Begin(context.Background(), pendingRequest())
	invOK(t, err)
	f := &pendingNoIO{}
	_, err = p.Run(context.Background(), f, peer.Connection{})
	invError(t, err)
	invEqual(t, f.calls, 0)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinFailed)
	_, err = p.Run(context.Background(), f, peer.Connection{})
	invEqual(t, errors.Is(err, peer.ErrJoinState), true)
	invEqual(t, f.calls, 0)
}

func TestPendingJoinAcceptanceInvalidInputsNeverPersist(t *testing.T) {
	for _, mutate := range []func(*peer.JoinRequest){
		func(q *peer.JoinRequest) { q.OperationID = "" },
		func(q *peer.JoinRequest) { q.OperationID = strings.Repeat("x", 129) },
		func(q *peer.JoinRequest) { q.Destination = "" },
		func(q *peer.JoinRequest) { q.Destination = "bad\naddress:443" },
		func(q *peer.JoinRequest) { q.Destination = strings.Repeat("x", 256) },
		func(q *peer.JoinRequest) { q.Name = "bad\nname" },
		func(q *peer.JoinRequest) { q.Code = []byte("not-a-code") },
	} {
		r := newRedeemFixture(t)
		req := pendingRequest()
		mutate(&req)
		p, _, err := pendingJoiner(t, r).Begin(context.Background(), req)
		invError(t, err)
		invEqual(t, p == nil, true)
		invEqual(t, len(redeemRows(t, r.joins)), 0)
	}
}

func TestPendingJoinAcceptanceRequestRedactsCode(t *testing.T) {
	req := pendingRequest()
	data, err := json.Marshal(req)
	invOK(t, err)
	for _, rendered := range []string{string(data), fmt.Sprintf("%v", req), fmt.Sprintf("%+v", req), fmt.Sprintf("%#v", req)} {
		invEqual(t, strings.Contains(rendered, string(req.Code)), false)
		invEqual(t, strings.Contains(rendered, fmt.Sprint(req.Code)), false)
		invEqual(t, strings.Contains(rendered, fmt.Sprintf("%x", req.Code)), false)
	}
}

func TestPendingJoinAcceptanceConcurrentBeginHasOneOwner(t *testing.T) {
	r := newRedeemFixture(t)
	j := pendingJoiner(t, r)
	start := make(chan struct{})
	type result struct {
		pending *peer.PendingJoin
		row     peer.JoinRecord
		err     error
	}
	results := make(chan result, 16)
	for range 16 {
		go func() {
			<-start
			p, row, err := j.Begin(context.Background(), pendingRequest())
			results <- result{p, row, err}
		}()
	}
	close(start)
	owners := 0
	for range 16 {
		r := <-results
		invOK(t, r.err)
		invEqual(t, r.row.OperationID, pendingRequest().OperationID)
		if r.pending != nil {
			owners++
			_, err := r.pending.Abandon(context.Background(), io.EOF)
			invEqual(t, errors.Is(err, io.EOF), true)
		}
	}
	invEqual(t, owners, 1)
	invEqual(t, len(redeemRows(t, r.joins)), 1)
}

func TestPendingJoinAcceptanceLegacyIDCollisionDoesNotPanicOrExchange(t *testing.T) {
	r := newRedeemFixture(t)
	opID := strings.Repeat("0", 32)
	_, err := r.joins.BeginJoin(context.Background(), peer.JoinStart{OperationID: opID, Joiner: r.bob.Fingerprint(), Name: "Bob", At: r.inv.clock.read()})
	invOK(t, err)
	j, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, bytes.NewReader(make([]byte, 16)))
	invOK(t, err)
	f := &pendingNoIO{}
	row, err := j.Join(context.Background(), f, peer.JoinInput{Connection: peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, Name: "Bob", Code: r.code()})
	invEqual(t, errors.Is(err, peer.ErrJoinConflict), true)
	invEqual(t, row.OperationID, opID)
	invEqual(t, f.calls, 0)
}
