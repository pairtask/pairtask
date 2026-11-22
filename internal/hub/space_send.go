package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

// One path sends every outbox record, whether the operator's op sends it
// now or the refresher sends it again later: it decodes the record kept
// (never the request in hand), sends it, and finishes it the same way. A
// definitive answer settles the record (and erases a join's secret) and is
// returned; a retryable one (network, 408, 429, 5xx) keeps it for the next
// pass.

// sendRetryable: err may go away by sending the same record again.
func sendRetryable(err error) bool {
	var r *space.Refusal
	if !errors.As(err, &r) {
		return true
	}
	return r.Status == http.StatusRequestTimeout || r.Status == http.StatusTooManyRequests || r.Status >= 500
}

// errSendExpired: a join kept past its invite's lifetime, never sent again.
var errSendExpired = errors.New("the request's invite has expired")

// dispatch sends it and finishes it.
func (h *Hub) dispatch(net, local context.Context, it store.SpaceSend) (*protocol.SpaceResult, error) {
	if !it.ExpiresAt.IsZero() && !h.authority.now().Before(it.ExpiresAt) {
		return nil, errors.Join(errSendExpired, h.store.SettleSpaceSend(local, it.Kind, it.Digest))
	}
	pin, dir, err := h.sendTarget(local, it)
	if errors.Is(err, errSendMoved) {
		return nil, errors.Join(err, h.store.SettleSpaceSend(local, it.Kind, it.Digest))
	}
	if err != nil {
		return nil, err
	}
	switch it.Kind {
	case "map":
		return h.dispatchMap(net, local, sendTo{pin, dir, it})
	case "invite":
		return nil, h.dispatchInvite(net, local, sendTo{pin, dir, it})
	}
	return nil, h.dispatchChange(net, local, sendTo{pin, dir, it})
}

// sendTo is a record and where it goes.
type sendTo struct {
	pin space.Pin
	dir space.MapDirectory
	it  store.SpaceSend
}

// errSendMoved: the space is no longer pinned to the owner and directory
// a record was queued for; it is never sent elsewhere.
var errSendMoved = errors.New("the space's pin changed since the request was queued; it was not sent")

// sendTarget is the pin and client a record goes to: only its space's
// current pin, and only while that pin names the owner and directory the
// record was queued for, so a secret never goes to another directory.
func (h *Hub) sendTarget(ctx context.Context, it store.SpaceSend) (space.Pin, space.MapDirectory, error) {
	pin, found, err := h.store.SpacePin(ctx, it.Space)
	if err != nil {
		return space.Pin{}, nil, err
	}
	if !found || pin.Base != it.Base || pin.Owner != it.Owner {
		return space.Pin{}, nil, errSendMoved
	}
	dir, err := h.mapDirectory(pin.Base)
	return pin, dir, err
}

// settleOn settles s's record when err is definitive, and returns err.
func (h *Hub) settleOn(local context.Context, s sendTo, err error) error {
	if err != nil && sendRetryable(err) {
		return err
	}
	return errors.Join(err, h.store.SettleSpaceSend(local, s.it.Kind, s.it.Digest))
}

func (h *Hub) dispatchMap(net, local context.Context, s sendTo) (*protocol.SpaceResult, error) {
	var req mapPut
	if err := json.Unmarshal(s.it.Body, &req); err != nil {
		return nil, h.settleOn(local, s, err)
	}
	err := s.dir.PutMap(net, s.pin, req.Put)
	switch {
	case err == nil:
		return h.keepPublished(local, s, req)
	case conflict(err):
		settled := h.store.SettleSpaceSend(local, s.it.Kind, s.it.Digest) // its base is gone: it can never apply
		return &protocol.SpaceResult{Stale: true}, errors.Join(settled, h.RefreshSpace(local, s.pin.Space))
	}
	return nil, h.settleOn(local, s, err)
}

// keepPublished keeps the map the directory took, consumes its invites
// and settles the record.
func (h *Hub) keepPublished(local context.Context, s sendTo, req mapPut) (*protocol.SpaceResult, error) {
	if err := h.KeepSpaceMap(local, s.pin.Space, req.Put.Map); err != nil && !errors.Is(err, pairbook.ErrRollback) {
		return nil, err // ErrRollback: a newer map is kept already; this one was taken before it
	}
	for _, c := range req.Invites {
		key := store.SpaceInvite{Space: s.pin.Space, Owner: s.pin.Owner, TokenHash: c.TokenHash}
		if err := h.store.ConsumeSpaceInvite(local, key, c.Change); err != nil {
			return nil, err
		}
	}
	if err := h.store.SettleSpaceSend(local, s.it.Kind, s.it.Digest); err != nil {
		return nil, err
	}
	return &protocol.SpaceResult{Mark: markView(req.Put.Map.SpaceMap.Mark())}, nil
}

// dispatchInvite sends an invite; one the directory refused is no longer
// live in the ledger.
func (h *Hub) dispatchInvite(net, local context.Context, s sendTo) error {
	var inv pairbook.SignedMapInvite
	if err := json.Unmarshal(s.it.Body, &inv); err != nil {
		return h.settleOn(local, s, err)
	}
	err := s.dir.PostMapInvite(net, s.pin, inv)
	if err != nil && !sendRetryable(err) {
		key := store.SpaceInvite{Space: s.pin.Space, Owner: s.pin.Owner, TokenHash: s.it.Digest}
		err = errors.Join(err, h.store.DropSpaceInvite(local, key))
	}
	return h.settleOn(local, s, err)
}

// dispatchChange sends a request, with a join's secret, then asks for a
// refresh so its effect shows soon.
func (h *Hub) dispatchChange(net, local context.Context, s sendTo) error {
	var ch pairbook.SignedChange
	if err := json.Unmarshal(s.it.Body, &ch); err != nil {
		return h.settleOn(local, s, err)
	}
	var token *[32]byte
	if len(s.it.Token) == 32 {
		token = (*[32]byte)(s.it.Token)
	}
	err := s.dir.SubmitChange(net, s.pin, ch, token)
	if errors.Is(err, space.ErrChangeToken) {
		err = &space.Refusal{Status: http.StatusBadRequest, Code: "invalid"} // never sendable
	}
	return h.settleOn(local, s, err)
}

// resendOutbox sends each waiting record again through dispatch.
func (h *Hub) resendOutbox(ctx context.Context) {
	items, err := h.store.SpaceSends(ctx)
	if err != nil {
		return
	}
	for _, it := range items {
		if _, err := h.dispatch(ctx, ctx, it); err != nil {
			h.log.Info("space send", "kind", it.Kind, "space", it.Space, "err", err)
		}
	}
}

// joinLifetime caps how long a join, and its secret, is kept for sending:
// no invite lives longer.
const joinLifetime = pairbook.InviteValidity

// joinDeadline is when a join stops being sent: the invite's own expiry
// when the host gave it, never later than the cap.
func joinDeadline(now, inviteExpires time.Time) time.Time {
	limit := now.Add(joinLifetime)
	if !inviteExpires.IsZero() && inviteExpires.Before(limit) {
		return inviteExpires
	}
	return limit
}
