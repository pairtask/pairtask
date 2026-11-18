package hub

import (
	"context"
	"crypto"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
)

// SpaceConfig enables Pairbook spaces (operator_space).
type SpaceConfig struct {
	// Directory makes the client for one directory, signing as this
	// workspace. nil = space.NewClient(base, signer, space.Options{HTTP: HTTP, Now: Clock}).
	Directory func(base string, signer crypto.Signer) (space.Directory, error)
	HTTP      *http.Client
	Clock     func() time.Time
}

// SetSpaces enables operator_space; without it, every space operation is
// refused as not configured.
func (h *Hub) SetSpaces(cfg SpaceConfig) {
	if cfg.Directory == nil {
		cfg.Directory = func(base string, signer crypto.Signer) (space.Directory, error) {
			return space.NewClient(base, signer, space.Options{HTTP: cfg.HTTP, Now: cfg.Clock})
		}
	}
	h.spaces = &cfg
}

// spaceBusy holds the spaces with a change in flight, one change per
// space: operator operations take it on the Run goroutine, and an inbound
// connect, off it, takes the same slot.
type spaceBusy struct {
	mu   sync.Mutex
	held map[string]bool
}

// hold takes id's slot; false when a change holds it already.
func (b *spaceBusy) hold(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held[id] {
		return false
	}
	if b.held == nil {
		b.held = map[string]bool{}
	}
	b.held[id] = true
	return true
}

func (b *spaceBusy) release(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.held, id)
}

// spaceHandler is one space operation: checks on the Run goroutine, then either
// an answer or a job for a worker.
type spaceHandler func(h *Hub, ctx context.Context, r protocol.SpaceRequest) (spaceStep, *routingError)

// spaceStep is what an operation does next: answer now, or run job on a
// worker and answer from there. space is the space job works on, if known.
type spaceStep struct {
	now   *protocol.SpaceResult
	job   spaceJob
	space string
}

// spaceReads only read from the directory, so they may overlap with
// anything; every other job holds its space until it answers.
var spaceReads = map[string]bool{protocol.SpaceOpRequests: true, protocol.SpaceOpMembers: true}

// spaceJob talks to the directory under net, then records what the
// directory accepted under local, which outlives net's cancellation so an
// accepted change is never left unrecorded.
type spaceJob func(net, local context.Context) (*protocol.SpaceResult, error)

var spaceOps = map[string]spaceHandler{
	protocol.SpaceOpList:     (*Hub).spaceList,
	protocol.SpaceOpCreate:   (*Hub).spaceCreate,
	protocol.SpaceOpJoin:     (*Hub).spaceJoin,
	protocol.SpaceOpInvite:   (*Hub).spaceInvite,
	protocol.SpaceOpRequests: (*Hub).spaceRequests,
	protocol.SpaceOpApprove:  (*Hub).spaceApprove,
	protocol.SpaceOpMembers:  (*Hub).spaceMembers,
	protocol.SpaceOpPublish:  (*Hub).spacePublish,
	protocol.SpaceOpRevoke:   (*Hub).spaceRevoke,
	protocol.SpaceOpLeave:    (*Hub).spaceLeave,
	protocol.SpaceOpDelete:   (*Hub).spaceDelete,
	protocol.SpaceOpConnect:  (*Hub).spaceConnect,
}

// operatorSpace serves one space operation. Local checks and the pins are
// read here; every directory call runs on a worker, so a slow directory
// never holds up the Run goroutine.
func (h *Hub) operatorSpace(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	op, rerr := h.spaceOpFor(ctx, req.msg)
	if rerr != nil {
		return rerr.frame(req.msg.MessageID)
	}
	step, rerr := op(h, ctx, *req.msg.SpaceRequest)
	if rerr != nil {
		return rerr.frame(req.msg.MessageID)
	}
	if step.job == nil {
		return spaceAnswer(req.msg.MessageID, step.now)
	}
	held := ""
	if !spaceReads[req.msg.SpaceRequest.Op] && step.space != "" {
		if held = step.space; !h.spaceBusy.hold(held) {
			return spaceRefusal("another change to space " + held + " is still running; try again once it answers").frame(req.msg.MessageID)
		}
	}
	h.startSpaceJob(req, step.job, held)
	return answerLater
}

func (h *Hub) spaceOpFor(ctx context.Context, msg protocol.Message) (spaceHandler, *routingError) {
	if msg.SpaceRequest == nil || len(msg.MessageID) > 128 {
		return nil, &routingError{protocol.ErrMalformedFrame, "operator_space needs space_request and a message_id of at most 128 bytes"}
	}
	op, ok := spaceOps[msg.SpaceRequest.Op]
	if !ok {
		return nil, &routingError{protocol.ErrMalformedFrame, "unknown space op " + msg.SpaceRequest.Op}
	}
	if h.spaces == nil {
		return nil, spaceRefusal("spaces are not configured on this broker")
	}
	if problem := h.loadIdentity(ctx); problem != "" {
		return nil, spaceRefusal(problem)
	}
	return op, nil
}

// startSpaceJob runs job off the Run goroutine, bounded by AttemptBudget
// and cancelled at shutdown, and answers through the Run goroutine, which
// then releases held, the space the job changed.
func (h *Hub) startSpaceJob(req operatorRequest, job spaceJob, held string) {
	runCtx := h.pair.runCtx
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		net, cancelNet := context.WithTimeout(runCtx, AttemptBudget)
		defer cancelNet()
		local, cancelLocal := context.WithTimeout(context.WithoutCancel(runCtx), AttemptBudget+PairingGrace)
		defer cancelLocal()
		result, err := job(net, local)
		h.finishOperator(runCtx, req, func(context.Context) protocol.Message {
			h.spaceBusy.release(held)
			if err != nil {
				return spaceRefusal(err.Error()).frame(req.msg.MessageID)
			}
			return spaceAnswer(req.msg.MessageID, result)
		})
	}()
}

func spaceAnswer(id string, result *protocol.SpaceResult) protocol.Message {
	if result == nil {
		result = &protocol.SpaceResult{}
	}
	return protocol.Message{Type: protocol.TypeOperatorSpaceResult, MessageID: id, SpaceResult: result}
}

func spaceRefusal(detail string) *routingError { return &routingError{protocol.ErrSpace, detail} }

// directory is the client for pin's directory, signing as this workspace.
func (h *Hub) directory(base string) (space.Directory, *routingError) {
	d, err := h.spaces.Directory(base, h.identity)
	if err != nil {
		return nil, spaceRefusal(err.Error())
	}
	return d, nil
}

// pinFor is the pin id names, or the only pin when id is empty.
func (h *Hub) pinFor(ctx context.Context, id string) (space.Pin, *routingError) {
	if id != "" {
		p, ok, err := h.store.SpacePin(ctx, id)
		if err != nil || !ok {
			return p, spaceRefusal("space " + id + " is not pinned here")
		}
		return p, nil
	}
	pins, err := h.store.SpacePins(ctx)
	switch {
	case err != nil:
		return space.Pin{}, spaceRefusal("space pins unavailable")
	case len(pins) == 0:
		return space.Pin{}, spaceRefusal("no space is pinned here; create or join one first")
	case len(pins) > 1:
		return space.Pin{}, spaceRefusal("several spaces are pinned; name one with --space: " + strings.Join(pinIDs(pins), ", "))
	}
	return pins[0], nil
}

func pinIDs(pins []space.Pin) []string {
	ids := make([]string, len(pins))
	for i, p := range pins {
		ids[i] = p.Space
	}
	return ids
}

// conflicting refuses p when the store already pins its space to another
// owner or directory: checked before any directory is contacted.
func (h *Hub) conflicting(ctx context.Context, p space.Pin) *routingError {
	held, ok, err := h.store.SpacePin(ctx, p.Space)
	if err != nil {
		return spaceRefusal("space pins unavailable")
	}
	if ok && !sameTrust(held, p) {
		return spaceRefusal("space " + p.Space + " is pinned to another owner or directory; nothing was sent")
	}
	return nil
}

// sameTrust: a and b trust the same owner through the same directory.
func sameTrust(a, b space.Pin) bool { return a.Owner == b.Owner && a.Base == b.Base }

// keep pins p once the directory accepted it; a pin that changed meanwhile
// is refused, and the existing one stays.
func (h *Hub) keep(ctx context.Context, p space.Pin) (*protocol.SpaceResult, error) {
	if err := h.store.PinSpace(ctx, p); err != nil {
		if errors.Is(err, space.ErrPinConflict) {
			return nil, errors.New("space " + p.Space + " was pinned to another owner or directory meanwhile")
		}
		return nil, err
	}
	return &protocol.SpaceResult{Pins: []protocol.SpacePin{h.pinView(p)}}, nil
}

func (h *Hub) pinView(p space.Pin) protocol.SpacePin {
	return protocol.SpacePin{Space: p.Space, Owner: p.Owner, Directory: p.Base, JoinedAt: p.JoinedAt, Owned: p.Owner == h.identity.Fingerprint()}
}

// spaceName is the name this workspace uses in a space: the asked one, or
// its pairing name.
func (h *Hub) spaceName(name string) string {
	if name != "" {
		return name
	}
	if h.pair.cfg.Name != "" {
		return h.pair.cfg.Name
	}
	return peer.DefaultPairingName
}

// validity is how long an operation's record may last by default, and at
// most, as the contract allows.
type validity struct{ def, max time.Duration }

// of is the asked validity in milliseconds, or v.def when none is asked;
// it is checked against v.max before it becomes a Duration.
func (v validity) of(ms int64) (time.Duration, *routingError) {
	if ms < 0 || ms > v.max.Milliseconds() {
		return 0, &routingError{protocol.ErrMalformedFrame, "valid_for_ms must be between 1 and " + v.max.String()}
	}
	if ms == 0 {
		return v.def, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}
