package peer

import (
	"context"
	"errors"
	"fmt"
)

// Settlement (#178): over a contact connection, where each side pins the
// other's identity, the joiner asks the inviter what became of its join.
// The inviter answers only from durable state, never begins an attempt and
// never writes an outcome. A decision travels in three steps, each durable
// before the next is sent: the joiner records it and acks; the inviter
// records the delivery (and, for an acceptance, the contact) and confirms;
// the joiner records the confirmation. Any loss leaves a step to repeat.

// Settle settles one join with its inviter on c, a contact connection to
// the identity the join authenticated. A decided, confirmed join returns
// at once.
func (j *Joiner) Settle(ctx context.Context, f Frames, c Connection, opID string) (JoinRecord, error) {
	r, found, err := j.store.GetJoin(ctx, opID)
	if err != nil {
		return JoinRecord{}, err
	}
	if !found {
		return JoinRecord{}, ErrNoJoin
	}
	if confirmed(r) {
		return r, nil
	}
	if err := settleable(r, c); err != nil {
		return r, err
	}
	f = boundedFrames{f}
	reply, err := ask(f, r)
	if err != nil {
		return r, err
	}
	r, err = j.applyReply(ctx, r, reply)
	if err != nil {
		return r, err
	}
	// Only a matched, final answer, the same as any decision we hold, is
	// ever acknowledged.
	if !decisive(reply) {
		return r, contradicts(r, reply)
	}
	return j.confirm(ctx, f, r)
}

func decisive(reply SettleReply) bool {
	return reply.Receipt == ReceiptMatched && reply.Decision != PairingPending
}

// contradicts reports an inviter now calling a decided join pending.
func contradicts(r JoinRecord, reply SettleReply) error {
	if final(r.State) && reply.Receipt == ReceiptMatched {
		return ErrJoinConflict
	}
	return nil
}

func confirmed(r JoinRecord) bool {
	return final(r.State) && !r.DecisionConfirmedAt.IsZero()
}

func final(s JoinState) bool {
	return s == JoinContact || s == JoinDeclined
}

// settleable requires an authenticated, correlated join and the connection
// to be with that same inviter.
func settleable(r JoinRecord, c Connection) error {
	if r.Peer == "" || r.InviterOperationID == "" {
		return ErrJoinState
	}
	if c.PeerLeaf != r.Peer {
		return ErrPeerMismatch
	}
	return nil
}

// ask sends the join's exact correlation and reads the inviter's answer.
func ask(f Frames, r JoinRecord) (SettleReply, error) {
	frame, err := EncodeSettle(Settle{JoinerOperationID: r.OperationID, InviterOperationID: r.InviterOperationID,
		InvitationID: r.InvitationID, TranscriptID: r.TranscriptID})
	if err != nil {
		return SettleReply{}, err
	}
	if err := f.Send(frame); err != nil {
		return SettleReply{}, err
	}
	frame, err = f.Recv()
	if err != nil {
		return SettleReply{}, err
	}
	return DecodeSettleReply(frame)
}

// applyReply takes in a matched receipt and a final decision. Nothing is
// believed without the inviter matching our exact receipt.
func (j *Joiner) applyReply(ctx context.Context, r JoinRecord, reply SettleReply) (JoinRecord, error) {
	if reply.Receipt != ReceiptMatched {
		return r, nil
	}
	r, err := j.matchReceipt(ctx, r)
	if err != nil || reply.Decision == PairingPending {
		return r, err
	}
	if final(r.State) {
		return r, finalState(r.State, decisionStates[reply.Decision])
	}
	return j.store.RecordDecision(ctx, r.OperationID, reply.Decision, j.clock())
}

func (j *Joiner) matchReceipt(ctx context.Context, r JoinRecord) (JoinRecord, error) {
	if r.State != JoinOutcomeUnknown {
		return r, nil
	}
	return j.store.RecordReceipt(ctx, r.OperationID, JoinReceipt{InvitationID: r.InvitationID,
		InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: r.TranscriptID}, j.clock())
}

// decisionOf is the decision a final join holds.
func decisionOf(s JoinState) PairingState {
	if s == JoinContact {
		return PairingAccepted
	}
	return PairingRejected
}

// confirm acks the recorded decision and records the inviter's receipt of it.
func (j *Joiner) confirm(ctx context.Context, f Frames, r JoinRecord) (JoinRecord, error) {
	ack := Ack{JoinerOperationID: r.OperationID, Decision: decisionOf(r.State)}
	frame, err := EncodeAck(ack)
	if err != nil {
		return r, err
	}
	if err := f.Send(frame); err != nil {
		return r, err
	}
	frame, err = f.Recv()
	if err != nil {
		return r, err
	}
	receipt, err := DecodeAckReceipt(frame)
	if err != nil {
		return r, err
	}
	if Ack(receipt) != ack {
		return r, fmt.Errorf("%w: ack receipt for another join or decision", ErrPairingProtocol)
	}
	return j.store.RecordDecisionConfirmed(ctx, r.OperationID, ack.Decision, j.clock())
}

// ServeSettle answers one settlement on c, a contact connection whose peer
// the caller pinned to a known pairing request. Only that peer's own
// outcome is ever matched, and only an ack on this connection, for the join
// and decision just answered, is recorded.
func (i *Inviter) ServeSettle(ctx context.Context, f Frames, c Connection) error {
	f = boundedFrames{f}
	request, err := readSettle(f)
	if err != nil {
		return err
	}
	reply, err := i.answer(ctx, request, c.PeerLeaf)
	if err != nil {
		return err
	}
	if err := sendReply(f, reply); err != nil {
		return err
	}
	if reply.Receipt != ReceiptMatched || reply.Decision == PairingPending {
		return nil
	}
	return i.recordAck(ctx, f, request, reply, c.PeerLeaf)
}

func readSettle(f Frames) (Settle, error) {
	frame, err := f.Recv()
	if err != nil {
		return Settle{}, err
	}
	m, err := DecodeSettle(frame)
	if err != nil {
		return Settle{}, err
	}
	ids := []bool{validPairingID(m.JoinerOperationID), validPairingID(m.InviterOperationID), validPairingID(m.InvitationID)}
	for _, ok := range append(ids, validTranscriptID(m.TranscriptID)) {
		if !ok {
			return Settle{}, fmt.Errorf("%w: invalid settle", ErrPairingProtocol)
		}
	}
	return m, nil
}

// answer reads durable state only: the exact outcome for this peer, and its
// request's decision.
func (i *Inviter) answer(ctx context.Context, m Settle, peer string) (SettleReply, error) {
	unknown := SettleReply{Receipt: ReceiptUnknown, Decision: PairingPending}
	store := i.inv.store
	matched, err := MatchReceipt(ctx, store, JoinReceipt{InvitationID: m.InvitationID, InviterOperationID: m.InviterOperationID,
		Joiner: peer, TranscriptID: m.TranscriptID})
	if err != nil || !matched {
		return unknown, err
	}
	request, found, err := store.Request(ctx, peer)
	if err != nil || !found {
		return unknown, err
	}
	return SettleReply{Receipt: ReceiptMatched, Decision: request.State}, nil
}

func sendReply(f Frames, reply SettleReply) error {
	frame, err := EncodeSettleReply(reply)
	if err != nil {
		return err
	}
	return f.Send(frame)
}

// recordAck takes the joiner's ack of the decision just served, records the
// delivery, and only then confirms it.
func (i *Inviter) recordAck(ctx context.Context, f Frames, m Settle, reply SettleReply, peer string) error {
	frame, err := f.Recv()
	if err != nil {
		return err
	}
	ack, err := DecodeAck(frame)
	if err != nil {
		return err
	}
	if ack != (Ack{JoinerOperationID: m.JoinerOperationID, Decision: reply.Decision}) {
		return fmt.Errorf("%w: ack for another join or decision", ErrPairingProtocol)
	}
	if _, err := i.inv.store.Deliver(ctx, peer, ack.Decision, i.inv.clock()); err != nil {
		return errors.Join(errors.New("peer: delivery not recorded"), err)
	}
	frame, err = EncodeAckReceipt(AckReceipt(ack))
	if err != nil {
		return err
	}
	return f.Send(frame)
}
