package hub

import (
	"context"
	"crypto"
	"errors"

	"github.com/pairtask/pairtask/internal/space"
)

// Refreshing a space's map (Pairbook v1): fetch the current map from the
// space's directory, then keep it as any map is kept (verified against the
// pin and the durable mark, then active). A refusal or an outage changes
// no authority: the kept map stays active until it expires. Its cause is
// kept for status, and the next successful refresh clears it.

var errSpacesOff = errors.New("hub: spaces aren't configured")

// RefreshSpace fetches and keeps id's current map.
func (h *Hub) RefreshSpace(ctx context.Context, id string) error {
	err := h.refreshSpace(ctx, id)
	h.authority.noteRefresh(spaceKey(id), err)
	return err
}

func (h *Hub) refreshSpace(ctx context.Context, id string) error {
	if h.spaces == nil || h.store == nil {
		return errSpacesOff
	}
	pin, ok, err := h.store.SpacePin(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("hub: space " + id + " isn't pinned here")
	}
	d, err := h.mapDirectory(pin.Base)
	if err != nil {
		return err
	}
	m, err := d.Map(ctx, pin)
	if err != nil {
		return err
	}
	return h.KeepSpaceMap(ctx, id, m)
}

// mapDirectory is the v1 client for base, signing as this workspace.
func (h *Hub) mapDirectory(base string) (space.MapDirectory, error) {
	ep, err := h.PairingEndpoint()
	if err != nil {
		return nil, err
	}
	return h.spaces.Maps(base, ep.Identity)
}

// SpaceRefreshError is why id's last refresh failed, or "" after a
// successful one.
func (h *Hub) SpaceRefreshError(id string) string {
	return h.authority.refreshError(spaceKey(id))
}

func (a *spaceAuthority) noteRefresh(k spaceKey, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		delete(a.refused, k)
		return
	}
	a.refused[k] = err.Error()
}

func (a *spaceAuthority) refreshError(k spaceKey) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refused[k]
}

// defaultMaps is the real v1 client.
func defaultMaps(cfg SpaceConfig) func(base string, signer crypto.Signer) (space.MapDirectory, error) {
	return func(base string, signer crypto.Signer) (space.MapDirectory, error) {
		return space.NewClient(base, signer, space.Options{HTTP: cfg.HTTP, Now: cfg.Clock})
	}
}
