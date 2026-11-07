//go:build linux

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

func (f *installLiveFixture) settleInviter(t *testing.T, o peer.PairingOutcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	destination := relayToPairing(t, f.f)
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	operatorNoError(t, err)
	defer raw.Close()
	conn, err := firstcontact.ContactClient(ctx, raw, f.remote, time.Now, f.ep.Identity.Fingerprint())
	operatorNoError(t, err)
	defer conn.Close()
	frames := conn.Frames(ctx)
	b, err := peer.EncodeSettle(peer.Settle{JoinerOperationID: "install-live-join", InviterOperationID: o.OperationID, InvitationID: o.InvitationID, TranscriptID: o.TranscriptID})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(b))
	b, err = frames.Recv()
	operatorNoError(t, err)
	reply, err := peer.DecodeSettleReply(b)
	operatorNoError(t, err)
	operatorEqual(t, "matched settlement", reply.Receipt, peer.ReceiptMatched)
	operatorEqual(t, "accepted settlement", reply.Decision, peer.PairingAccepted)
	b, err = peer.EncodeAck(peer.Ack{JoinerOperationID: "install-live-join", Decision: peer.PairingAccepted})
	operatorNoError(t, err)
	operatorNoError(t, frames.Send(b))
	b, err = frames.Recv()
	operatorNoError(t, err)
	_, err = peer.DecodeAckReceipt(b)
	operatorNoError(t, err)
}
func (f *installLiveFixture) joinerServer(t *testing.T) (net.Listener, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	operatorNoError(t, err)
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() { done <- f.answerJoiner(listener) }()
	return listener, done
}
func (f *installLiveFixture) answerJoiner(listener net.Listener) error {
	raw, err := listener.Accept()
	if err != nil {
		return err
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := firstcontact.ServerWith(ctx, raw, f.remote, time.Now, firstcontact.ServerOptions{ContactPins: func(fp string) bool { return fp == f.ep.Identity.Fingerprint() }})
	if err != nil {
		return err
	}
	defer conn.Close()
	request, err := installAnswerSettle(conn.Frames(ctx))
	if err != nil {
		return err
	}
	return installAnswerAck(conn.Frames(ctx), request.JoinerOperationID)
}
func installAnswerSettle(frames peer.Frames) (peer.Settle, error) {
	b, err := frames.Recv()
	if err != nil {
		return peer.Settle{}, err
	}
	request, err := peer.DecodeSettle(b)
	if err != nil {
		return request, err
	}
	b, err = peer.EncodeSettleReply(peer.SettleReply{Receipt: peer.ReceiptMatched, Decision: peer.PairingAccepted})
	if err != nil {
		return request, err
	}
	return request, frames.Send(b)
}
func installAnswerAck(frames peer.Frames, operation string) error {
	b, err := frames.Recv()
	if err != nil {
		return err
	}
	if _, err = peer.DecodeAck(b); err != nil {
		return err
	}
	b, err = peer.EncodeAckReceipt(peer.AckReceipt{JoinerOperationID: operation, Decision: peer.PairingAccepted})
	if err != nil {
		return err
	}
	return frames.Send(b)
}
