//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := make(chan []byte, 4), make(chan []byte, 4)
	joinFrames := redeemPipe{ctx, a, b}
	var inviteFrames peer.Frames = redeemPipe{ctx, b, a}
	if r.filter != nil {
		inviteFrames = redeemFilter{inviteFrames, r.filter}
	}
	joiner, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, rand.Reader)
	invOK(t, err)
	inviter, err := peer.NewInviter(r.inviteEx, r.inv.manager, r.alice, rand.Reader)
	invOK(t, err)
	results := make(chan redeemSide, 2)
	go func() {
		defer close(b)
		row, err := joiner.Join(ctx, joinFrames, peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, code, "Bob")
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

func TestRedeemAcceptancePersistsBeforeConfirmationAndReceipt(t *testing.T) {
	r := newRedeemFixture(t)
	r.joinEx.afterPersist = func(b peer.ExchangeBinding) error {
		row, found, err := r.joins.GetJoin(context.Background(), b.JoinerOperationID)
		if err != nil {
			return err
		}
		if !found || row.State != peer.JoinOutcomeUnknown || row.Peer != b.Inviter || row.Joiner != b.Joiner || len(row.TranscriptID) == 0 {
			return errors.New("confirmation preceded durable authenticated correlation")
		}
		return nil
	}
	r.filter = func(frame []byte) ([]byte, error) {
		if len(frame) < 2 || frame[1] != peer.FrameReceipt {
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
	result := r.run(t, r.code())
	invOK(t, result.joinErr)
	invOK(t, result.inviteErr)
	rows := redeemRows(t, r.joins)
	invEqual(t, len(rows), 1)
	row := rows[0]
	invEqual(t, row.State, peer.JoinAwaitingAcceptance)
	invEqual(t, row.Peer, r.alice.Fingerprint())
	invEqual(t, row.Joiner, r.bob.Fingerprint())
	outcome, found, err := r.inv.store.Outcome(context.Background(), row.InviterOperationID)
	invOK(t, err)
	invEqual(t, found, true)
	invEqual(t, outcome.IdentityKey, r.bob.Fingerprint())
	invEqual(t, bytes.Equal(outcome.TranscriptID, row.TranscriptID), true)
	invEqual(t, outcome.InvitationID, row.InvitationID)
	invEqual(t, invitationStatus(t, r.inv).Consumed, true)
}

type redeemFaultStore struct {
	peer.JoinStore
	verifyErr, receiptErr error
}

func (s redeemFaultStore) RecordVerified(ctx context.Context, id, fp string, transcript []byte, at time.Time) (peer.JoinRecord, error) {
	if s.verifyErr != nil {
		return peer.JoinRecord{}, s.verifyErr
	}
	return s.JoinStore.RecordVerified(ctx, id, fp, transcript, at)
}
func (s redeemFaultStore) RecordReceipt(ctx context.Context, id string, r peer.JoinReceipt, at time.Time) (peer.JoinRecord, error) {
	if s.receiptErr != nil {
		return peer.JoinRecord{}, s.receiptErr
	}
	return s.JoinStore.RecordReceipt(ctx, id, r, at)
}
func TestRedeemAcceptanceFailedLocalPersistencePreventsConfirmation(t *testing.T) {
	r := newRedeemFixture(t)
	r.joins = redeemFaultStore{JoinStore: r.joins, verifyErr: errors.New("disk failure")}
	result := r.run(t, r.code())
	invError(t, result.joinErr)
	invEqual(t, r.joinEx.confirmations, 0)
	redeemNoOutcome(t, r)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinFailed)
}
func TestRedeemAcceptanceLostReceiptKeepsImmutableUnknownOutcome(t *testing.T) {
	for _, failure := range []string{"drop", "corrupt", "store"} {
		t.Run(failure, func(t *testing.T) {
			r := newRedeemFixture(t)
			r.filter = func(frame []byte) ([]byte, error) {
				if len(frame) < 2 || frame[1] != peer.FrameReceipt {
					return frame, nil
				}
				if failure == "drop" {
					return nil, io.ErrClosedPipe
				}
				if failure == "corrupt" {
					frame[len(frame)-1] ^= 1
				}
				return frame, nil
			}
			if failure == "store" {
				r.joins = redeemFaultStore{JoinStore: r.joins, receiptErr: errors.New("receipt disk failure")}
			}
			result := r.run(t, r.code())
			invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
			row := redeemRows(t, r.joins)[0]
			invEqual(t, row.State, peer.JoinOutcomeUnknown)
			invEqual(t, row.Peer, r.alice.Fingerprint())
			invEqual(t, len(row.TranscriptID) > 0, true)
			_, found, err := r.inv.store.Outcome(context.Background(), row.InviterOperationID)
			invOK(t, err)
			invEqual(t, found, true)
			_, err = r.joins.RecoverJoins(context.Background(), r.inv.clock.read())
			invOK(t, err)
			invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinOutcomeUnknown)
		})
	}
}
func TestRedeemAcceptanceProviderErrorAfterPersistStaysUnknown(t *testing.T) {
	r := newRedeemFixture(t)
	r.joinEx.afterPersist = func(peer.ExchangeBinding) error { return peer.ErrExchangeFailed }
	result := r.run(t, r.code())
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinOutcomeUnknown)
	redeemNoOutcome(t, r)
}

func TestRedeemAcceptanceWrongCodeExhaustsWithoutOutcome(t *testing.T) {
	r := newRedeemFixture(t)
	wrong := r.code()
	wrong[0] = '0' + (wrong[0]-'0'+1)%10
	for n := 0; n < 3; n++ {
		result := r.run(t, wrong)
		invError(t, result.joinErr)
		invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2-n)
		redeemNoOutcome(t, r)
	}
	result := r.run(t, r.code())
	invError(t, result.joinErr)
	invEqual(t, errors.Is(result.inviteErr, peer.ErrInvitationExhausted), true)
	invEqual(t, invitationStatus(t, r.inv).Consumed, false)
}
func TestRedeemAcceptanceBindingAndAuthenticatedIdentityMustMatch(t *testing.T) {
	for _, fault := range []string{"binding", "joiner-peer", "inviter-peer"} {
		t.Run(fault, func(t *testing.T) {
			r := newRedeemFixture(t)
			switch fault {
			case "binding":
				r.joinBinding[0] ^= 1
			case "joiner-peer":
				r.joinEx.peerOverride = "sha256:" + strings.Repeat("a", 64)
			case "inviter-peer":
				r.inviteEx.peerOverride = "sha256:" + strings.Repeat("b", 64)
			}
			result := r.run(t, r.code())
			invError(t, result.joinErr)
			redeemNoOutcome(t, r)
			invEqual(t, invitationStatus(t, r.inv).Consumed, false)
		})
	}
}

type redeemScript struct {
	input [][]byte
	sent  [][]byte
	reads int
}

func (f *redeemScript) Send(b []byte) error { f.sent = append(f.sent, bytes.Clone(b)); return nil }
func (f *redeemScript) Recv() ([]byte, error) {
	f.reads++
	if len(f.input) == 0 {
		return nil, io.EOF
	}
	b := f.input[0]
	f.input = f.input[1:]
	return bytes.Clone(b), nil
}
func TestRedeemAcceptanceUnavailableAndSelfRefuseBeforeSideEffects(t *testing.T) {
	for _, which := range []string{"unavailable", "self", "empty-binding", "large-binding", "bad-peer"} {
		t.Run(which, func(t *testing.T) {
			r := newRedeemFixture(t)
			var ex peer.Exchanger = r.joinEx
			connection := peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.joinBinding}
			switch which {
			case "unavailable":
				ex = nil
			case "self":
				connection.PeerLeaf = r.alice.Fingerprint()
			case "empty-binding":
				connection.ChannelBinding = nil
			case "large-binding":
				connection.ChannelBinding = make([]byte, 65)
			case "bad-peer":
				connection.PeerLeaf = "not-a-fingerprint"
			}
			f := &redeemScript{}
			inviter, err := peer.NewInviter(ex, r.inv.manager, r.alice, rand.Reader)
			invOK(t, err)
			_, err = inviter.Serve(context.Background(), f, connection)
			invError(t, err)
			invEqual(t, f.reads, 0)
			invEqual(t, len(f.sent), 0)
			invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 3)
			joiner, err := peer.NewJoiner(ex, r.joins, r.alice, r.inv.clock.read, rand.Reader)
			invOK(t, err)
			_, err = joiner.Join(context.Background(), f, connection, r.code(), "Alice")
			invError(t, err)
			invEqual(t, len(redeemRows(t, r.joins)), 0)
			invEqual(t, len(f.sent), 0)
		})
	}
}

// Independent framing keeps adversarial inputs independent of production encoders.
func redeemWire(kind byte, fields ...[]byte) []byte {
	out := []byte{1, kind}
	for _, field := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(field)))
		out = append(out, field...)
	}
	return out
}
func TestRedeemAcceptanceMalformedHelloDoesNotCharge(t *testing.T) {
	valid := redeemWire(1, []byte("operation"), []byte("Bob"))
	cases := [][]byte{nil, {2, 1}, valid[:len(valid)-1], append(bytes.Clone(valid), 0), redeemWire(2, []byte("inv"), []byte("op")), make([]byte, 4097), redeemWire(1, nil, []byte("Bob")), redeemWire(1, []byte("op"), []byte("bad\nname"))}
	for n, frame := range cases {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := newRedeemFixture(t)
			f := &redeemScript{input: [][]byte{frame}}
			i, err := peer.NewInviter(r.inviteEx, r.inv.manager, r.alice, rand.Reader)
			invOK(t, err)
			_, err = i.Serve(context.Background(), f, peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.joinBinding})
			invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
			invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 3)
			redeemNoOutcome(t, r)
		})
	}
}
func TestRedeemAcceptanceCancelAndDeadlineBeforeCommit(t *testing.T) {
	for _, fault := range []string{"cancel", "timeout", "replace"} {
		t.Run(fault, func(t *testing.T) {
			r := newRedeemFixture(t)
			var hookErr error
			r.inviteEx.beforeCommit = func() {
				switch fault {
				case "cancel":
					hookErr = r.inv.manager.Cancel(r.open.ID)
				case "timeout":
					r.inv.clock.advance(31 * time.Second)
				case "replace":
					_, hookErr = r.inv.manager.Open()
				}
			}
			result := r.run(t, r.code())
			invOK(t, hookErr)
			invError(t, result.inviteErr)
			invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
			redeemNoOutcome(t, r)
		})
	}
}

func TestRedeemAcceptanceBusyRefusesWithoutAnotherCharge(t *testing.T) {
	r := newRedeemFixture(t)
	active := beginInvitation(t, r.inv, r.open.ID)
	result := r.run(t, r.code())
	invEqual(t, errors.Is(result.inviteErr, peer.ErrInvitationBusy), true)
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinRefused), true)
	var refusal *peer.JoinRefusal
	invEqual(t, errors.As(result.joinErr, &refusal), true)
	invEqual(t, refusal.Reason, "invitation_busy")
	invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2)
	invEqual(t, invitationStatus(t, r.inv).InFlight, true)
	invOK(t, active.Fail())
	redeemNoOutcome(t, r)
}

func TestRedeemAcceptanceFrameCodecStrictShapeAndLimits(t *testing.T) {
	cases := []struct {
		name   string
		wire   []byte
		decode func([]byte) error
	}{
		{"joiner", redeemWire(1, []byte("op"), []byte("Bob")), func(b []byte) error { _, err := peer.DecodeJoinerHello(b); return err }},
		{"inviter", redeemWire(2, []byte("inv"), []byte("op")), func(b []byte) error { _, err := peer.DecodeInviterHello(b); return err }},
		{"refusal", redeemWire(3, []byte("busy")), func(b []byte) error { _, err := peer.DecodeRefusal(b); return err }},
		{"receipt", redeemWire(4, []byte("inv"), []byte("op"), []byte("joiner"), []byte("transcript")), func(b []byte) error { _, err := peer.DecodeReceipt(b); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			invOK(t, c.decode(c.wire))
			badVersion := bytes.Clone(c.wire)
			badVersion[0] = 2
			badType := bytes.Clone(c.wire)
			badType[1] = 99
			for _, frame := range [][]byte{badVersion, badType, c.wire[:len(c.wire)-1], append(bytes.Clone(c.wire), 0), make([]byte, 4097)} {
				invEqual(t, errors.Is(c.decode(frame), peer.ErrPairingProtocol), true)
			}
		})
	}
	frame, err := peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 4092)})
	invOK(t, err)
	invEqual(t, len(frame), 4096)
	_, err = peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 4093)})
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	_, err = peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 65536)})
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
}

type redeemOversizedExchange struct {
	mode   string
	called bool
}

func (x *redeemOversizedExchange) Join(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte, persist func(context.Context, peer.ExchangeResult) error) error {
	x.called = true
	if x.mode == "send" {
		return f.Send(make([]byte, 4097))
	}
	_, err := f.Recv()
	return err
}
func (x *redeemOversizedExchange) Invite(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte) (peer.ExchangeResult, error) {
	err := x.Join(ctx, f, b, code, nil)
	return peer.ExchangeResult{}, err
}
func TestRedeemAcceptanceProviderFramesAreBoundedBothWays(t *testing.T) {
	for _, c := range [][2]string{{"joiner", "send"}, {"joiner", "receive"}, {"inviter", "send"}, {"inviter", "receive"}} {
		t.Run(c[0]+"/"+c[1], func(t *testing.T) { redeemOversizeCase(t, c[0], c[1]) })
	}
}
func redeemOversizeCase(t *testing.T, side, mode string) {
	t.Helper()
	r := newRedeemFixture(t)
	ex := &redeemOversizedExchange{mode: mode}
	f := &redeemScript{}
	var err error
	if side == "joiner" {
		f.input = [][]byte{redeemWire(2, []byte("inv"), []byte("remote")), make([]byte, 4097)}
		j, e := peer.NewJoiner(ex, r.joins, r.bob, r.inv.clock.read, rand.Reader)
		invOK(t, e)
		_, err = j.Join(context.Background(), f, peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, r.code(), "Bob")
	} else {
		f.input = [][]byte{redeemWire(1, []byte("op"), []byte("Bob")), make([]byte, 4097)}
		i, e := peer.NewInviter(ex, r.inv.manager, r.alice, rand.Reader)
		invOK(t, e)
		_, err = i.Serve(context.Background(), f, peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.inviteBinding})
	}
	invEqual(t, ex.called, true)
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	for _, sent := range f.sent {
		invEqual(t, len(sent) <= 4096, true)
	}
	redeemNoOutcome(t, r)
}

type redeemCommitFault struct {
	peer.PairingStore
	failures   int
	afterWrite bool
	seen       []peer.PairingOutcome
	advance    func()
}

func (s *redeemCommitFault) Commit(ctx context.Context, o peer.PairingOutcome) (peer.PairingRequest, error) {
	o.TranscriptID = bytes.Clone(o.TranscriptID)
	s.seen = append(s.seen, o)
	s.advance()
	if len(s.seen) > s.failures {
		return s.PairingStore.Commit(ctx, o)
	}
	if s.afterWrite {
		if _, err := s.PairingStore.Commit(ctx, o); err != nil {
			return peer.PairingRequest{}, err
		}
	}
	return peer.PairingRequest{}, errors.New("injected commit failure")
}
func TestRedeemAcceptanceCommitRetriesKeepOneFrozenOutcome(t *testing.T) {
	cases := []struct {
		name       string
		failures   int
		afterWrite bool
	}{{"transient", 2, false}, {"ambiguous", 1, true}, {"persistent", 99, false}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRedeemFixture(t)
			fault := &redeemCommitFault{PairingStore: peer.NewMemPairingStore(), failures: c.failures, afterWrite: c.afterWrite}
			r.inv = newInvitationFixture(t, fault)
			r.open = openInvitation(t, r.inv)
			fault.advance = func() { r.inv.clock.advance(time.Second) }
			result := r.run(t, r.code())
			redeemCheckCommitResult(t, r, fault, result)
		})
	}
}
func redeemCheckCommitResult(t *testing.T, r *redeemFixture, fault *redeemCommitFault, result redeemResult) {
	t.Helper()
	invEqual(t, r.inviteEx.invites, 1)
	invEqual(t, r.joinEx.confirmations, 1)
	invEqual(t, len(fault.seen), min(3, fault.failures+1))
	for _, o := range fault.seen[1:] {
		invEqual(t, reflect.DeepEqual(o, fault.seen[0]), true)
	}
	invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2)
	if fault.failures >= 3 {
		invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
		invEqual(t, invitationStatus(t, r.inv).InFlight, true)
		redeemNoOutcome(t, r)
		r.inv.clock.advance(31 * time.Second)
		invEqual(t, invitationStatus(t, r.inv).InFlight, false)
		return
	}
	invOK(t, result.joinErr)
	invOK(t, result.inviteErr)
	invEqual(t, invitationStatus(t, r.inv).Consumed, true)
	rows, err := fault.Requests(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), 1)
}

func TestRedeemAcceptanceCodeGroupingAndInvalidInputs(t *testing.T) {
	t.Run("grouped", func(t *testing.T) {
		r := newRedeemFixture(t)
		result := r.run(t, []byte(" "+r.open.Code+" "))
		invOK(t, result.joinErr)
		invOK(t, result.inviteErr)
	})
	for n, code := range []string{"", "123", "1234567890123456789", "123a56", "123\n456", "１２３４５６", strings.Repeat(" ", 65) + "123456"} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := newRedeemFixture(t)
			j, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, rand.Reader)
			invOK(t, err)
			f := &redeemScript{}
			_, err = j.Join(context.Background(), f, peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, []byte(code), "Bob")
			invEqual(t, errors.Is(err, peer.ErrInvalidBinding), true)
			invEqual(t, len(f.sent), 0)
			invEqual(t, f.reads, 0)
			invEqual(t, len(redeemRows(t, r.joins)), 0)
		})
	}
}
