package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/pairbook"
)

func validBindingRequest() BindingRequest {
	return BindingRequest{Lineage: strings.Repeat("ab", 32), Person: "sha256:" + strings.Repeat("cd", 32), Epoch: 1, At: time.UnixMilli(1700000000000)}
}

func TestBrokerBindingRequiresLoadedIdentity(t *testing.T) {
	_, err := newTestHub(t).AcceptBinding(validBindingRequest())
	if !errors.Is(err, peer.ErrIdentityUnavailable) {
		t.Fatalf("before identity load: %v", err)
	}
}

func TestBrokerBindingAcceptanceUsesPinnedWorkspace(t *testing.T) {
	h := newTestHub(t)
	req := validBindingRequest()
	if problem := h.loadIdentity(context.Background()); problem != "" {
		t.Fatal(problem)
	}
	ep, err := h.PairingEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	reply, err := h.AcceptBinding(req)
	if err != nil {
		t.Fatal(err)
	}
	a, err := identity.ParseAcceptance(reply.Acceptance)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Check(ep.Identity.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if a.Workspace != ep.Identity.Fingerprint() || a.Workspace != pairbook.KeyID(reply.WorkspaceKey) {
		t.Fatal("acceptance key differs from pairing identity")
	}
	checkBindingFields(t, a, req)
}

func checkBindingFields(t *testing.T, a identity.Acceptance, req BindingRequest) {
	t.Helper()
	if pairbook.LineageHex(pairbook.Lineage(a.Lineage)) != req.Lineage || a.Person != req.Person {
		t.Fatal("incorrect person or lineage")
	}
	if a.Epoch != req.Epoch || !a.At.Equal(req.At) {
		t.Fatal("incorrect epoch or time")
	}
}

func TestBrokerRefusesUnencodableBinding(t *testing.T) {
	h := newTestHub(t)
	if problem := h.loadIdentity(context.Background()); problem != "" {
		t.Fatal(problem)
	}
	l := strings.Repeat("ab", 32)
	p := "sha256:" + strings.Repeat("cd", 32)
	at := time.UnixMilli(1700000000000)
	for _, tc := range []struct {
		name, lineage, person string
		epoch                 uint64
		at                    time.Time
	}{
		{"uppercase lineage", strings.ToUpper(l), p, 1, at},
		{"uppercase fingerprint", l, strings.ToUpper(p), 1, at},
		{"zero epoch", l, p, 0, at},
		{"overflow epoch", l, p, identity.MaxCounter + 1, at},
		{"negative timestamp", l, p, 1, time.UnixMilli(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.AcceptBinding(BindingRequest{Lineage: tc.lineage, Epoch: tc.epoch, Person: tc.person, At: tc.at})
			if !errors.Is(err, ErrBindingRequest) {
				t.Fatalf("got %v, want malformed binding refusal", err)
			}
		})
	}
}
