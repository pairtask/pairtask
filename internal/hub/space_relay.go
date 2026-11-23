package hub

import (
	"context"
	"crypto/ed25519"
	"errors"
	"maps"
	"net"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// The relay path (designs/pairbook/relay.md). The relay is the default way
// two workspaces of a space reach each other; direct only to a workspace
// that hosts, i.e. keeps a published address. The peer protocol runs end
// to end over a relay splice, unchanged: the relay forwards bytes only.
//
// A contact reached through a relay keeps a relay destination, so every
// path that reconnects by Destination goes through the relay again.

const (
	relayDestPrefix = "pairbook-relay:"
	relayPassEvery  = 30 * time.Second
	relayReachValid = 30 * time.Minute // a RelayReach record's validity; renewed at half
	replacedBackoff = 10 * time.Minute // after another registration took this slot
	maxRelayAccepts = 8                // accepts in flight on one slot
)

var errNoRelays = errors.New("this Pairbook serves no relays")

// relayRoute is target (a key ID) through relay, in space.
type relayRoute struct{ space, relay, target string }

// destination is the route as a contact keeps it.
func (r relayRoute) destination() string {
	return relayDestPrefix + r.space + "/" + r.relay + "/" + r.target
}

// parseRelayDestination reads a relay destination; ok is false for any
// other destination.
func parseRelayDestination(d string) (relayRoute, bool) {
	rest, found := strings.CutPrefix(d, relayDestPrefix)
	if !found {
		return relayRoute{}, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return relayRoute{}, false
	}
	r := relayRoute{parts[0], parts[1], parts[2]}
	return r, r.valid()
}

func (r relayRoute) valid() bool {
	if r.space == "" || !pairbook.ValidRelayID(r.relay) {
		return false
	}
	return strings.HasPrefix(r.target, "sha256:")
}

// relayAware dials relay destinations through the relay, and every other
// destination as base does. Nil base stays nil: no peer links at all.
func (h *Hub) relayAware(base func(context.Context, string) (net.Conn, error)) func(context.Context, string) (net.Conn, error) {
	if base == nil {
		return nil
	}
	return func(ctx context.Context, d string) (net.Conn, error) {
		if route, ok := parseRelayDestination(d); ok {
			return h.dialRelay(ctx, route)
		}
		return base(ctx, d)
	}
}

func (h *Hub) dialRelay(ctx context.Context, r relayRoute) (net.Conn, error) {
	pin, rd, err := h.relayDirectory(ctx, r.space)
	if err != nil {
		return nil, err
	}
	return space.DialRelay(ctx, rd.RelayEnd(pin, r.relay), r.target)
}

// relayDirectory is space's pin and its Pairbook, when that serves relays.
func (h *Hub) relayDirectory(ctx context.Context, spaceID string) (space.Pin, space.RelayDirectory, error) {
	if h.spaces == nil {
		return space.Pin{}, nil, errors.New("spaces are not configured")
	}
	pin, found, err := h.store.SpacePin(ctx, spaceID)
	if err != nil || !found {
		return pin, nil, errors.Join(err, errors.New("space "+spaceID+" isn't pinned here"))
	}
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return pin, nil, err
	}
	rd, ok := dir.(space.RelayDirectory)
	if !ok {
		return pin, nil, errNoRelays
	}
	return pin, rd, nil
}

// relayDestinations are target's relay destinations in pin's space, in the
// order its record lists them; ErrNoRelayReach when it listens on none.
func relayDestinations(ctx context.Context, dir space.MapDirectory, pin space.Pin, key ed25519.PublicKey) ([]string, error) {
	rd, ok := dir.(space.RelayDirectory)
	if !ok {
		return nil, space.ErrNoRelayReach
	}
	reach, err := rd.MapRelayReach(ctx, pin, key)
	if err != nil {
		return nil, err
	}
	dests := make([]string, len(reach.Relays))
	for i, relay := range reach.Relays {
		dests[i] = relayRoute{pin.Space, relay, pairbook.KeyID(key)}.destination()
	}
	return dests, nil
}

// relayReachable: node keeps a listen slot in space, as its Pairbook says.
// Anything unknown counts as no: the next pass asks again.
func (h *Hub) relayReachable(ctx context.Context, spaceID string, node ed25519.PublicKey) bool {
	pin, found, err := h.store.SpacePin(ctx, spaceID)
	if err != nil || !found {
		return false
	}
	dir, err := h.mapDirectory(pin.Base)
	if err != nil {
		return false
	}
	_, err = relayDestinations(ctx, dir, pin, node)
	return err == nil
}

// hosting: the operator keeps a published address for space here.
func (h *Hub) hosting(ctx context.Context, spaceID string) bool {
	_, found, err := h.store.SpaceAddressOf(ctx, spaceID)
	return err == nil && found
}

// ServeRelays keeps a listen slot on a relay in every space this workspace
// is bound in and doesn't host, publishes its RelayReach there, and hands
// each accepted connection to serve, as a socket's would be. It returns
// when ctx ends, closing every slot.
func (h *Hub) ServeRelays(ctx context.Context, serve func(context.Context, net.Conn)) {
	slots := relaySlots{h: h, serve: serve, live: map[string]*relaySlot{}}
	defer slots.closeAll()
	tick := time.NewTicker(relayPassEvery)
	defer tick.Stop()
	for {
		slots.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

type relaySlots struct {
	h     *Hub
	serve func(context.Context, net.Conn)
	live  map[string]*relaySlot // by space
}

type relaySlot struct {
	relay     string
	l         *space.RelayListener
	reachAt   time.Time // RelayReach last published
	waitUntil time.Time // after a replacement: don't fight the other copy
}

func (s relaySlots) pass(ctx context.Context) {
	if s.h.spaces == nil {
		return // spaces aren't configured: nothing to listen for
	}
	wanted, err := s.wanted(ctx)
	if err != nil {
		return
	}
	for id := range wanted {
		s.keep(ctx, id)
	}
	s.dropUnwanted(wanted)
}

// wanted are the spaces this workspace is bound in and doesn't host.
func (s relaySlots) wanted(ctx context.Context) (map[string]bool, error) {
	pins, err := s.h.store.SpacePins(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, pin := range pins {
		wanted[pin.Space] = s.h.boundHere(pin.Space) && !s.h.hosting(ctx, pin.Space)
	}
	maps.DeleteFunc(wanted, func(_ string, want bool) bool { return !want })
	return wanted, nil
}

// dropUnwanted closes the slots of spaces no longer wanted: left, unbound,
// or hosting now.
func (s relaySlots) dropUnwanted(wanted map[string]bool) {
	for id, slot := range s.live {
		if !wanted[id] {
			slot.close()
			delete(s.live, id)
		}
	}
}

// keep opens space's slot if it has none, and renews its RelayReach.
func (s relaySlots) keep(ctx context.Context, spaceID string) {
	slot := s.live[spaceID]
	if slot != nil && slot.open() {
		s.renewReach(ctx, spaceID, slot)
		return
	}
	if slot != nil && time.Now().Before(slot.waitUntil) {
		return
	}
	next, err := s.open(ctx, spaceID, slot)
	if err != nil {
		s.h.log.Info("relay slot not open", "space", spaceID, "err", err)
		return
	}
	s.live[spaceID] = next
}

// open registers on the Pairbook's first relay and publishes RelayReach.
func (s relaySlots) open(ctx context.Context, spaceID string, prev *relaySlot) (*relaySlot, error) {
	if prev.justReplaced() {
		prev.waitUntil = time.Now().Add(replacedBackoff)
		return prev, nil
	}
	pin, rd, err := s.h.relayDirectory(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	relays, err := rd.Relays(ctx, pin)
	if err != nil || len(relays) == 0 {
		return nil, errors.Join(err, errNoRelays)
	}
	l, err := space.ListenRelay(ctx, rd.RelayEnd(pin, relays[0].ID))
	if err != nil {
		return nil, err
	}
	slot := &relaySlot{relay: relays[0].ID, l: l}
	go s.accept(ctx, l)
	s.renewReach(ctx, spaceID, slot)
	return slot, nil
}

// renewReach publishes the slot's RelayReach when never published or at
// half its validity.
func (s relaySlots) renewReach(ctx context.Context, spaceID string, slot *relaySlot) {
	if !slot.reachAt.IsZero() && time.Since(slot.reachAt) < relayReachValid/2 {
		return
	}
	pin, rd, err := s.h.relayDirectory(ctx, spaceID)
	if err == nil {
		err = rd.PublishRelayReach(ctx, pin, []string{slot.relay}, relayReachValid)
	}
	if err != nil {
		s.h.log.Info("relay reach not published", "space", spaceID, "err", err)
		return
	}
	slot.reachAt = time.Now()
}

// accept takes the slot's offers, a bounded number at a time; one over
// the bound is declined, and the dialer tries again later.
func (s relaySlots) accept(ctx context.Context, l *space.RelayListener) {
	inFlight := make(chan struct{}, maxRelayAccepts)
	for offer := range l.Offers() {
		select {
		case inFlight <- struct{}{}:
		default:
			_ = l.Decline(ctx, offer)
			continue
		}
		go func() {
			defer func() { <-inFlight }()
			conn, err := l.Accept(ctx, offer)
			if err != nil {
				return
			}
			s.serve(ctx, conn)
		}()
	}
}

func (s relaySlots) closeAll() {
	for _, slot := range s.live {
		slot.close()
	}
}

func (r *relaySlot) open() bool {
	select {
	case <-r.l.Done():
		return false
	default:
		return true
	}
}

func (r *relaySlot) close() { _ = r.l.Close() }

// justReplaced: another registration took this slot, and no backoff ran yet.
func (r *relaySlot) justReplaced() bool {
	if r == nil || !r.waitUntil.IsZero() {
		return false
	}
	return errors.Is(r.l.Err(), space.ErrRelayReplaced)
}
