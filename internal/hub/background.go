package hub

import "context"

// The hub's background work, begun when Run starts and settled when it
// ends: pairing (redemption, settlement and message sync workers) and the
// shadow classifier's worker. Each is explicit here; none runs unless its
// configuration asks for it.

func (h *Hub) beginBackground(ctx context.Context) {
	h.beginPairing(ctx)
	h.startClassifier(ctx)
	h.startNotifier(ctx)
}

// settleBackground waits for every worker: pairing first, so its grace
// starts at once, then the classifier, whose only wait is the provider
// call's own timeout.
func (h *Hub) settleBackground() {
	h.settlePairing()
	h.settleClassifier()
	h.settleNotifier()
}
