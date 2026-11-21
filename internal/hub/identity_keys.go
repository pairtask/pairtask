package hub

import (
	"context"
	"crypto/ed25519"
	"time"
)

// NoteWorkspaceKey records a paired workspace's identity key as a message
// session presents it (#392): identity bindings to that workspace are
// judged against it. A failure is logged, never fatal to the session: a
// binding to an unknown workspace only stays unverified.
func (h *Hub) NoteWorkspaceKey(ctx context.Context, pub ed25519.PublicKey) {
	if len(pub) != ed25519.PublicKeySize {
		return
	}
	if err := h.store.NoteWorkspaceKey(ctx, pub, time.Now()); err != nil {
		h.log.Warn("workspace key not recorded", "error", err)
	}
}
