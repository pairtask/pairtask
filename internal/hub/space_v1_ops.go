package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

// Pairbook v1 operator ops (space maps). The host signs; the broker checks
// each record locally before it pins anything or writes to a directory,
// keeps it in the outbox before sending, and sends the same bytes on every
// retry. A join's invite secret is used only to send its change and is
// never put in a result, a log or an error.

func init() {
	for op, h := range map[string]spaceHandler{
		protocol.SpaceOpMapAccept:      (*Hub).mapAccept,
		protocol.SpaceOpMapPublish:     (*Hub).mapPublish,
		protocol.SpaceOpMapCurrent:     (*Hub).mapCurrent,
		protocol.SpaceOpMapChanges:     (*Hub).mapChanges,
		protocol.SpaceOpMapDrop:        (*Hub).mapDrop,
		protocol.SpaceOpMapInvite:      (*Hub).mapInvite,
		protocol.SpaceOpMapInviteFetch: (*Hub).mapInviteFetch,
		protocol.SpaceOpMapSubmit:      (*Hub).mapSubmit,
		protocol.SpaceOpMapRefresh:     (*Hub).mapRefresh,
		protocol.SpaceOpMapStatus:      (*Hub).mapStatus,
	} {
		spaceOps[op] = h
	}
	for _, op := range []string{protocol.SpaceOpMapCurrent, protocol.SpaceOpMapChanges, protocol.SpaceOpMapInviteFetch, protocol.SpaceOpMapStatus} {
		spaceReads[op] = true
	}
}

// mapAccept signs this workspace's acceptance of a person's claim.
func (h *Hub) mapAccept(_ context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	a, err := h.AcceptBinding(BindingRequest{Lineage: r.Lineage, Epoch: r.Epoch, Person: r.Person, At: r.At})
	if err != nil {
		return spaceStep{}, spaceRefusal(err.Error())
	}
	return spaceStep{now: &protocol.SpaceResult{Acceptance: a.Acceptance, WorkspaceKey: a.WorkspaceKey}}, nil
}

// mapPut is a publish request as the host sends it: the put, and the
// invites the map's joins consume.
type mapPut struct {
	Put     space.MapPut  `json:"put"`
	Invites []mapConsumed `json:"invites,omitempty"`
}

// mapConsumed is an invite (token hash, 64 hex) a join change (digest)
// used.
type mapConsumed struct {
	TokenHash string `json:"token_hash"`
	Change    string `json:"change"`
}

// mapPublish checks the host's next map against the pin and the stored
// mark (or, for a space's first map, against its own owner key), pins a new
// space, and sends it. A 409 means another map won: the broker fetches and
// keeps the winner, and answers stale so the host folds again.
func (h *Hub) mapPublish(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	var req mapPut
	if rerr := decodeRecord(r.Record, &req); rerr != nil {
		return spaceStep{}, rerr
	}
	pin, rerr := h.publishPin(ctx, r.Directory, req.Put)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		return h.sendMap(net, local, pin, req)
	}}, nil
}

// publishPin is the pin a put goes under, checked locally first: the pin
// kept for its space, or, for a first map, the pin it would make.
func (h *Hub) publishPin(ctx context.Context, directory string, put space.MapPut) (space.Pin, *routingError) {
	id := put.Map.Space
	pin, found, err := h.store.SpacePin(ctx, id)
	if err != nil {
		return space.Pin{}, spaceRefusal(err.Error())
	}
	if !found {
		pin = space.Pin{Space: id, Owner: pairbook.KeyID(put.Map.Owner), Base: directory, JoinedAt: time.Now().UTC()}
	}
	if rerr := h.checkPut(ctx, pin, put, found); rerr != nil {
		return space.Pin{}, rerr
	}
	return pin, nil
}

// checkPut: the put's base is the stored mark (zero for a first map), and
// its map verifies under the pin from that base, as the next version.
func (h *Hub) checkPut(ctx context.Context, pin space.Pin, put space.MapPut, pinned bool) *routingError {
	_, stored, _, err := h.store.SpaceMap(ctx, pin.Space)
	if err != nil {
		return spaceRefusal(err.Error())
	}
	if put.Base != stored || put.Map.Version != put.Base.Version+1 || (!pinned && put.Base != (pairbook.Mark{})) {
		return spaceRefusal("the map isn't the next version of the one kept here; fetch and fold again")
	}
	if _, err := put.Map.Verify(pin.Space, pin.Owner, put.Base, h.authority.now()); err != nil {
		return spaceRefusal("the map doesn't verify: " + err.Error())
	}
	return nil
}

// sendMap checks the directory origin, pins a new space, keeps the put in
// the outbox and sends the kept record.
func (h *Hub) sendMap(net, local context.Context, pin space.Pin, req mapPut) (*protocol.SpaceResult, error) {
	if _, err := h.mapDirectory(pin.Base); err != nil {
		return nil, err // a directory the client refuses: nothing pinned
	}
	if err := h.store.PinSpace(local, pin); err != nil {
		return nil, err
	}
	digest := req.Put.Map.SpaceMap.Mark().Digest
	item, err := h.queue(local, store.SpaceSend{Kind: "map", Digest: hex.EncodeToString(digest[:]), Space: pin.Space, Base: pin.Base, Owner: pin.Owner}, req)
	if err != nil {
		return nil, err
	}
	return h.dispatch(net, local, item)
}

// queue keeps body in the outbox as item, and returns what is kept.
func (h *Hub) queue(ctx context.Context, item store.SpaceSend, body any) (store.SpaceSend, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return store.SpaceSend{}, err
	}
	item.Body, item.CreatedAt = b, time.Now().UTC()
	return h.store.QueueSpaceSend(ctx, item)
}

// conflict: the directory answered 409.
func conflict(err error) bool {
	var r *space.Refusal
	return errors.As(err, &r) && r.Status == http.StatusConflict
}

// mapCurrent is the map kept for a space, expired or not, and its mark.
func (h *Hub) mapCurrent(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	m, mark, found, err := h.store.SpaceMap(ctx, pin.Space)
	if err != nil || !found {
		return spaceStep{}, spaceRefusal("no map kept for space " + pin.Space)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return spaceStep{}, spaceRefusal(err.Error())
	}
	return spaceStep{now: &protocol.SpaceResult{Record: body, Mark: markView(mark)}}, nil
}

// mapChanges are the pending requests the owner has to fold.
func (h *Hub) mapChanges(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, _ context.Context) (*protocol.SpaceResult, error) {
		dir, err := h.mapDirectory(pin.Base)
		if err != nil {
			return nil, err
		}
		changes, err := dir.Changes(net, pin)
		if err != nil {
			return nil, err
		}
		return changesResult(changes)
	}}, nil
}

func changesResult(changes []pairbook.SignedChange) (*protocol.SpaceResult, error) {
	out := &protocol.SpaceResult{Changes: []json.RawMessage{}}
	for _, c := range changes {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		out.Changes = append(out.Changes, b)
	}
	return out, nil
}

// mapDrop tells the directory the owner refused a pending request.
func (h *Hub) mapDrop(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	digest, ok := digestOf(r.Digest)
	if !ok {
		return spaceStep{}, &routingError{protocol.ErrMalformedFrame, "digest must be 64 hex"}
	}
	return spaceStep{space: pin.Space, job: func(net, _ context.Context) (*protocol.SpaceResult, error) {
		dir, err := h.mapDirectory(pin.Base)
		if err != nil {
			return nil, err
		}
		return nil, dir.DropChange(net, pin, digest)
	}}, nil
}

// mapInvite records and sends an owner-signed invite, checked against the
// pin first.
func (h *Hub) mapInvite(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	var inv pairbook.SignedMapInvite
	if rerr := decodeRecord(r.Record, &inv); rerr != nil {
		return spaceStep{}, rerr
	}
	if err := inv.Verify(pin.Space, pin.Owner, h.authority.now()); err != nil {
		return spaceStep{}, spaceRefusal("the invite doesn't verify: " + err.Error())
	}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		return h.sendInvite(net, local, pin, inv)
	}}, nil
}

func (h *Hub) sendInvite(net, local context.Context, pin space.Pin, inv pairbook.SignedMapInvite) (*protocol.SpaceResult, error) {
	hash := hex.EncodeToString(inv.TokenHash[:])
	ledger := store.SpaceInvite{Space: pin.Space, Owner: pin.Owner, TokenHash: hash, BaseVersion: inv.Base.Version,
		BaseDigest: hex.EncodeToString(inv.Base.Digest[:]), ExpiresAt: time.UnixMilli(inv.ExpiresAt)}
	if err := h.store.KeepSpaceInvite(local, ledger); err != nil {
		return nil, err
	}
	item, err := h.queue(local, store.SpaceSend{Kind: "invite", Digest: hash, Space: pin.Space, Base: pin.Base, Owner: pin.Owner}, inv)
	if err != nil {
		return nil, err
	}
	return h.dispatch(net, local, item)
}

// mapInviteFetch is the invite a link names, verified against the owner
// the link pins. Nothing of the link goes in an error.
func (h *Hub) mapInviteFetch(_ context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	inv, err := space.ParseInvite(r.Link)
	if err != nil {
		return spaceStep{}, spaceRefusal("not a valid invite link")
	}
	return spaceStep{job: func(net, _ context.Context) (*protocol.SpaceResult, error) {
		dir, err := h.mapDirectory(inv.Base)
		if err != nil {
			return nil, err
		}
		got, err := dir.MapInvite(net, inv)
		if err != nil {
			return nil, errors.New("the invite could not be fetched or doesn't verify")
		}
		body, err := json.Marshal(got)
		return &protocol.SpaceResult{Record: body}, err
	}}, nil
}

// mapSubmit sends a person's signed request. A join comes with its link:
// its space, owner and token must be the change's, and its space is
// pinned (pending until a map includes this workspace).
func (h *Hub) mapSubmit(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	var ch pairbook.SignedChange
	if rerr := decodeRecord(r.Record, &ch); rerr != nil {
		return spaceStep{}, rerr
	}
	sub, rerr := h.submission(ctx, r, ch)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: sub.pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		return nil, h.sendChange(net, local, sub)
	}}, nil
}

// submission is a checked request: its pin, change and, for a join, the
// invite secret.
type submission struct {
	pin   space.Pin
	ch    pairbook.SignedChange
	token *[32]byte
	until time.Time // a join: its invite's expiry, from the host
	fresh bool      // the pin is made by this join
}

func (h *Hub) submission(ctx context.Context, r protocol.SpaceRequest, ch pairbook.SignedChange) (submission, *routingError) {
	if ch.Action != pairbook.ChangeJoin {
		pin, rerr := h.pinFor(ctx, r.Space)
		if rerr == nil && pin.Space != ch.Space {
			rerr = spaceRefusal("the request is for another space")
		}
		return submission{pin: pin, ch: ch}, rerr
	}
	inv, err := space.ParseInvite(r.Link)
	if err != nil {
		return submission{}, spaceRefusal("a join needs its invite link")
	}
	if inv.Space != ch.Space || sha256.Sum256(inv.Token[:]) != ch.TokenHash {
		return submission{}, spaceRefusal("the link isn't this join's invite")
	}
	kept, pinned, err := h.store.SpacePin(ctx, inv.Space)
	if err != nil {
		return submission{}, spaceRefusal(err.Error())
	}
	if pinned && (kept.Owner != inv.Owner || kept.Base != inv.Base) {
		return submission{}, spaceRefusal("this space is pinned to another owner or directory than the link names")
	}
	pin := space.Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base, JoinedAt: time.Now().UTC()}
	if _, err := h.mapDirectory(pin.Base); err != nil {
		return submission{}, spaceRefusal("the link names a directory this broker can't use")
	}
	return submission{pin: pin, ch: ch, token: &inv.Token, until: r.Until, fresh: !pinned}, nil
}

// sendChange pins a join's space, keeps the change (with a join's secret,
// until its deadline) in the outbox, and sends the kept record.
func (h *Hub) sendChange(net, local context.Context, s submission) error {
	if s.fresh {
		if err := h.store.PinSpace(local, s.pin); err != nil {
			return err
		}
	}
	digest := s.ch.Digest()
	item := store.SpaceSend{Kind: "change", Digest: hex.EncodeToString(digest[:]), Space: s.pin.Space, Base: s.pin.Base, Owner: s.pin.Owner}
	if s.token != nil {
		item.Token, item.ExpiresAt = s.token[:], joinDeadline(h.authority.now(), s.until)
	}
	item, err := h.queue(local, item, s.ch)
	if err != nil {
		return err
	}
	if _, err = h.dispatch(net, local, item); err == nil {
		h.kickRefresh() // the change was taken: its effect shows at the next map
	}
	return err
}

// mapRefresh fetches and keeps a space's current map now.
func (h *Hub) mapRefresh(ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError) {
	pin, rerr := h.pinFor(ctx, r.Space)
	if rerr != nil {
		return spaceStep{}, rerr
	}
	return spaceStep{space: pin.Space, job: func(net, local context.Context) (*protocol.SpaceResult, error) {
		if err := h.RefreshSpace(net, pin.Space); err != nil {
			return nil, err
		}
		_, mark, _, err := h.store.SpaceMap(local, pin.Space)
		return &protocol.SpaceResult{Mark: markView(mark)}, err
	}}, nil
}

// decodeRecord reads an op's record.
func decodeRecord(raw json.RawMessage, out any) *routingError {
	if len(raw) == 0 || json.Unmarshal(raw, out) != nil {
		return &routingError{protocol.ErrMalformedFrame, "the record is missing or malformed"}
	}
	return nil
}

func markView(m pairbook.Mark) *protocol.SpaceMark {
	return &protocol.SpaceMark{Version: m.Version, Digest: hex.EncodeToString(m.Digest[:])}
}

func digestOf(s string) ([32]byte, bool) {
	var d [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(d) || hex.EncodeToString(b) != s {
		return d, false
	}
	copy(d[:], b)
	return d, true
}
