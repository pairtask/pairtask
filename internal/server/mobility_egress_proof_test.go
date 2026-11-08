//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Builder proof for #284 (not a frozen acceptance test): the production
// egress forwarder, with its resolution and forbidden-target policy, carries
// a broker's message dials and candidate probes. The resolver's answers and
// the forwarder's dial mapping are synthetic: documentation addresses stand
// in for a tailnet, and every permitted dial reaches one local relay.

type egressLab struct {
	mu      sync.Mutex
	answers map[string][]netip.Addr
	dials   []string
	conns   []net.Conn
	target  string
}

func (l *egressLab) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.answers[host]; ok {
		return a, nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	return nil, errors.New("synthetic NXDOMAIN")
}

// labAnswer is what the synthetic resolver says for one name; no
// addresses means it can't resolve the name.
type labAnswer struct {
	host  string
	addrs []string
}

func (l *egressLab) answer(a labAnswer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(a.addrs) == 0 {
		delete(l.answers, a.host)
		return
	}
	l.answers[a.host] = nil
	for _, addr := range a.addrs {
		l.answers[a.host] = append(l.answers[a.host], netip.MustParseAddr(addr))
	}
}

func (l *egressLab) dial(ctx context.Context, network, address string) (net.Conn, error) {
	l.mu.Lock()
	l.dials = append(l.dials, address)
	target := l.target
	l.mu.Unlock()
	conn, err := (&net.Dialer{}).DialContext(ctx, network, target)
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, conn)
		l.mu.Unlock()
	}
	return conn, err
}

// drop ends every link the forwarder carries, so the next sync dials, and
// resolves, again.
func (l *egressLab) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.Close()
	}
	l.conns = nil
}

func (l *egressLab) dialled() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.dials)
}

func egressLabPair(t *testing.T) (*instantPair, *egressLab) {
	t.Helper()
	inviter := newRedeemOperator(t, redeemConfig(), "")
	lab := &egressLab{answers: map[string][]netip.Addr{}, target: relayToPairing(t, inviter)}
	lab.answer(labAnswer{"home.tailnet.example", []string{"192.0.2.10"}})
	lab.answer(labAnswer{"evil.example", []string{"127.0.0.1"}})
	sock := filepath.Join(t.TempDir(), "egress.sock")
	ln, err := net.Listen("unix", sock)
	operatorNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := egress.NewForwarder(egress.Config{Resolver: lab, Dial: lab.dial,
		LocalAddrs: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("198.51.100.1")}, nil }})
	go f.Serve(ctx, ln)
	cfg := redeemConfig()
	cfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		return egress.Dial(ctx, sock, destination)
	}
	joiner := newRedeemOperator(t, cfg, "")
	inviterEP, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	joinerEP, err := joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	messageConfirm(inviter, joinerEP.Identity, 1)
	messageJoinedContact(joiner, inviterEP.Identity.Fingerprint(), "home.tailnet.example:7445")
	return &instantPair{inviter: inviter, joiner: joiner, inviterKey: inviterEP.Identity.Fingerprint(), joinerKey: joinerEP.Identity.Fingerprint()}, lab
}

func egressLabSynced(t *testing.T, p *instantPair, after time.Time) {
	t.Helper()
	p.joiner.h.SyncMessages(p.inviterKey)
	mobilityAwait(t, func() bool { return p.joiner.h.MessageSyncStatus(p.inviterKey).LastSuccess.After(after) })
}

// labCandidates is one candidates operation: its ID and endpoints.
type labCandidates struct {
	id         string
	candidates []string
}

func egressLabCandidates(t *testing.T, p *instantPair, op labCandidates) []protocol.CandidateResult {
	t.Helper()
	probe := &sessionProbe{operatorPeer: operatorDial(t, p.joiner.operator)}
	probe.send(map[string]any{"type": protocol.TypeOperatorContact, "contact_op": "candidates", "message_id": op.id, "contact_candidates": op.candidates})
	response := probe.frame()
	operatorEqual(t, "candidates answered", response.text("type"), protocol.TypeOperatorContactResult)
	var results []protocol.CandidateResult
	operatorNoError(t, json.Unmarshal(response["contact_candidates_result"], &results))
	return results
}

func TestMobilityEgressProofConfiguredNameSurvivesChangedAnswer(t *testing.T) {
	p, lab := egressLabPair(t)
	egressLabSynced(t, p, time.Time{})
	lab.answer(labAnswer{"home.tailnet.example", []string{"192.0.2.20"}})
	lab.drop()
	mobilityAwait(t, func() bool { return !p.joiner.h.MessageSyncStatus(p.inviterKey).Live })
	egressLabSynced(t, p, time.Now())
	operatorEqual(t, "configured name kept", mobilityContact(t, p).Destination, "home.tailnet.example:7445")
	operatorEqual(t, "new answer dialled", slices.Contains(lab.dialled(), "192.0.2.20:7445"), true)
	operatorEqual(t, "working route browses nothing", mobilityLease(t, p.joiner)["browse"], any(false))
}

func TestMobilityEgressProofForbiddenAnswersNeverDialledAndOverlayFallbackAdopted(t *testing.T) {
	p, lab := egressLabPair(t)
	egressLabSynced(t, p, time.Time{})
	lab.answer(labAnswer{host: "home.tailnet.example"}) // the connector's DNS loses the name
	lab.drop()
	mobilityAwait(t, func() bool { return !p.joiner.h.MessageSyncStatus(p.inviterKey).Live })
	p.joiner.h.SyncMessages(p.inviterKey)
	mobilityAwait(t, func() bool { return p.joiner.h.MessageSyncStatus(p.inviterKey).LastFailure != "" })
	results := egressLabCandidates(t, p, labCandidates{"forbidden", []string{"evil.example:7445", "198.51.100.1:7445"}})
	for _, r := range results {
		operatorEqual(t, "forbidden target refused: "+r.Candidate, r.Outcome, protocol.CandidateRefused)
	}
	for _, d := range lab.dialled() {
		if d == "127.0.0.1:7445" || d == "198.51.100.1:7445" {
			t.Fatalf("forbidden answer reached the dialer: %s", d)
		}
	}
	results = egressLabCandidates(t, p, labCandidates{"fallback", []string{"home.tailnet.example:7445", "100.70.1.2:7445"}})
	// Egress refuses a destination its lookup fails for, as for a forbidden one.
	operatorEqual(t, "unresolvable name refused by egress", results[0].Outcome, protocol.CandidateRefused)
	operatorEqual(t, "overlay address adopted", results[1].Outcome, protocol.CandidateAdopted)
	operatorEqual(t, "contact moved to the overlay address", mobilityContact(t, p).Destination, "100.70.1.2:7445")
}
