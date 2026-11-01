//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// This deterministic exchange is ONLY a test double for orchestration. It is
// not a PAKE and makes no cryptographic security or interoperability claim.
type redeemExchange struct {
	afterPersist  func(peer.ExchangeBinding) error
	beforeCommit  func()
	peerOverride  string
	confirmations int
	invites       int
}

func redeemDigest(b peer.ExchangeBinding, code []byte) []byte {
	data, _ := json.Marshal(b)
	digest := sha256.Sum256(append(data, code...))
	return digest[:]
}
func (x *redeemExchange) Join(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte, persist func(context.Context, peer.ExchangeResult) error) error {
	want := redeemDigest(b, code)
	got, err := f.Recv()
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return peer.ErrExchangeFailed
	}
	remote := b.Inviter
	if x.peerOverride != "" {
		remote = x.peerOverride
	}
	if err := persist(ctx, peer.ExchangeResult{Peer: remote, TranscriptID: want}); err != nil {
		return err
	}
	if x.afterPersist != nil {
		if err := x.afterPersist(b); err != nil {
			return err
		}
	}
	x.confirmations++
	return f.Send(want)
}
func (x *redeemExchange) Invite(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte) (peer.ExchangeResult, error) {
	x.invites++
	want := redeemDigest(b, code)
	if err := f.Send(want); err != nil {
		return peer.ExchangeResult{}, err
	}
	got, err := f.Recv()
	if err != nil {
		return peer.ExchangeResult{}, err
	}
	if !bytes.Equal(got, want) {
		return peer.ExchangeResult{}, peer.ErrExchangeFailed
	}
	if x.beforeCommit != nil {
		x.beforeCommit()
	}
	remote := b.Joiner
	if x.peerOverride != "" {
		remote = x.peerOverride
	}
	return peer.ExchangeResult{Peer: remote, TranscriptID: want}, nil
}

type redeemPipe struct {
	ctx     context.Context
	in, out chan []byte
}

func (p redeemPipe) Send(b []byte) error {
	select {
	case p.out <- bytes.Clone(b):
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}
func (p redeemPipe) Recv() ([]byte, error) {
	select {
	case b, ok := <-p.in:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-p.ctx.Done():
		return nil, p.ctx.Err()
	}
}

type redeemFilter struct {
	peer.Frames
	send func([]byte) ([]byte, error)
}

func (f redeemFilter) Send(b []byte) error {
	b, err := f.send(bytes.Clone(b))
	if err != nil {
		return err
	}
	return f.Frames.Send(b)
}

type redeemFixture struct {
	inv                        invitationFixture
	open                       peer.Invitation
	alice, bob                 *peer.WorkspaceIdentity
	joins                      peer.JoinStore
	joinEx, inviteEx           *redeemExchange
	joinBinding, inviteBinding []byte
	filter                     func([]byte) ([]byte, error)
}

func newRedeemFixture(t *testing.T) *redeemFixture {
	t.Helper()
	r := &redeemFixture{inv: newInvitationFixture(t, peer.NewMemPairingStore()), joins: peer.NewMemJoinStore(), joinEx: &redeemExchange{}, inviteEx: &redeemExchange{}}
	r.open = openInvitation(t, r.inv)
	r.alice = identityLoad(t, filepath.Join(t.TempDir(), "alice"), &identityMarker{}, rand.Reader)
	r.bob = identityLoad(t, filepath.Join(t.TempDir(), "bob"), &identityMarker{}, rand.Reader)
	r.joinBinding = bytes.Repeat([]byte{7}, 32)
	r.inviteBinding = bytes.Clone(r.joinBinding)
	return r
}

type redeemResult struct {
	row                peer.JoinRecord
	joinErr, inviteErr error
}
type redeemSide struct {
	join bool
	row  peer.JoinRecord
	err  error
}

func (r *redeemFixture) run(t *testing.T, code []byte) redeemResult {
	t.Helper()
	return r.runWith(t, code, r.joinEx, r.inviteEx)
}

func (r *redeemFixture) runWith(t *testing.T, code []byte, joinEx, inviteEx peer.Exchanger) redeemResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := make(chan []byte, 4), make(chan []byte, 4)
	joinFrames := redeemPipe{ctx, a, b}
	var inviteFrames peer.Frames = redeemPipe{ctx, b, a}
	if r.filter != nil {
		inviteFrames = redeemFilter{inviteFrames, r.filter}
	}
	joiner, err := peer.NewJoiner(joinEx, r.joins, r.bob, r.inv.clock.read, rand.Reader)
	invOK(t, err)
	inviter, err := peer.NewInviter(inviteEx, r.inv.manager, r.alice)
	invOK(t, err)
	results := make(chan redeemSide, 2)
	go func() {
		defer close(b)
		row, err := joiner.Join(ctx, joinFrames, peer.JoinInput{Connection: peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, Code: code, Name: "Bob"})
		results <- redeemSide{join: true, row: row, err: err}
	}()
	go func() {
		defer close(a)
		_, err := inviter.Serve(ctx, inviteFrames, peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.inviteBinding})
		results <- redeemSide{err: err}
	}()
	var result redeemResult
	for range 2 {
		s := <-results
		if s.join {
			result.row, result.joinErr = s.row, s.err
		} else {
			result.inviteErr = s.err
		}
	}
	return result
}
func (r *redeemFixture) code() []byte { return []byte(strings.ReplaceAll(r.open.Code, "-", "")) }
func redeemRows(t *testing.T, s peer.JoinStore) []peer.JoinRecord {
	t.Helper()
	rows, err := s.Joins(context.Background())
	invOK(t, err)
	return rows
}
func redeemNoOutcome(t *testing.T, r *redeemFixture) {
	t.Helper()
	rows, err := r.inv.store.Requests(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), 0)
}

func (r *redeemFixture) checkVerified(b peer.ExchangeBinding) error {
	row, found, err := r.joins.GetJoin(context.Background(), b.JoinerOperationID)
	if err != nil {
		return err
	}
	checks := []bool{found, row.State == peer.JoinOutcomeUnknown, row.Peer == b.Inviter, row.Joiner == b.Joiner, len(row.TranscriptID) > 0, row.InvitationID == b.InvitationID, row.InviterOperationID == b.InviterOperationID}
	if slices.Contains(checks, false) {
		return errors.New("confirmation preceded durable authenticated correlation")
	}
	return nil
}
func (r *redeemFixture) checkDurableReceipt(frame []byte) ([]byte, error) {
	if !redeemReceipt(frame) {
		return frame, nil
	}
	receipt, err := peer.DecodeReceipt(frame)
	if err != nil {
		return nil, err
	}
	ok, err := peer.MatchReceipt(context.Background(), r.inv.store, receipt)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("receipt preceded durable inviter outcome")
	}
	return frame, nil
}

// The fake exchange sends raw SHA256 digests, whose bytes can resemble a
// receipt header. Real receipts include identity and correlation fields and
// are longer than a digest.
func redeemReceipt(frame []byte) bool {
	return len(frame) > sha256.Size && frame[1] == peer.FrameReceipt
}

// dropRedeemReceipt is a frame filter that loses the inviter's receipt and
// passes every other frame unchanged, a digest whose second byte looks like
// a receipt header included.
func dropRedeemReceipt(frame []byte) ([]byte, error) {
	if redeemReceipt(frame) {
		return nil, io.ErrUnexpectedEOF
	}
	return frame, nil
}
