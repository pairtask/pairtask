package hub

import (
	"context"
	"github.com/pairtask/pairtask/internal/protocol"
	"slices"
	"testing"
)

func consentSessionOpen(t *testing.T, h *Hub, ctx context.Context, s *OperatorSession) protocol.SessionInfo {
	t.Helper()
	response := h.OpenOperatorSession(ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "consent-open"}, s)
	if response.Type != protocol.TypeOperatorSession || response.Session == nil {
		t.Fatalf("open: %+v", response)
	}
	return *response.Session
}

func TestConsentSessionAcceptanceGenerationChangesOnReconnect(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	old := NewOperatorSession(1)
	first := consentSessionOpen(t, h, ctx, old)
	if first.Epoch == "" || first.Gen == 0 || !slices.Contains(first.Capabilities, protocol.CapabilityConsentPrompts) {
		t.Fatalf("operator session lacks consent identity: %+v", first)
	}
	h.CloseOperatorSession(old)
	replacement := NewOperatorSession(1)
	second := consentSessionOpen(t, h, ctx, replacement)
	if first.Epoch != second.Epoch || first.Gen == second.Gen || first.SessionID == second.SessionID {
		t.Fatalf("reconnect retained consent authority: old=%+v new=%+v", first, second)
	}
	h.CloseOperatorSession(old)
	busy := h.OpenOperatorSession(ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "busy"}, NewOperatorSession(1))
	if busy.Code != protocol.ErrOwnerBusy {
		t.Fatalf("stale close removed replacement: %+v", busy)
	}
	h.CloseOperatorSession(replacement)
}

func TestConsentSessionAcceptanceRestartChangesBrokerEpoch(t *testing.T) {
	var epochs []string
	for i := 0; i < 2; i++ {
		h := newTestHub(t)
		ctx, cancel := context.WithCancel(context.Background())
		go h.Run(ctx)
		session := NewOperatorSession(1)
		opened := consentSessionOpen(t, h, ctx, session)
		epochs = append(epochs, opened.Epoch)
		h.CloseOperatorSession(session)
		cancel()
		<-h.Done()
	}
	if epochs[0] == "" || epochs[0] == epochs[1] {
		t.Fatalf("broker incarnation reused consent epoch: %q", epochs)
	}
}
