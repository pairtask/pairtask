package peer

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Gate runs inside secured. It terminates TLS on connections the relay
// forwards over a Unix socket, so the relay never sees plaintext and never
// holds a key, reads bounded records, and applies the contact rules. It is
// the only parser of peer bytes, and it speaks only the peer schema: operator
// and agent frames don't exist here.
type Gate struct {
	cfg    GateConfig
	tls    *tls.Config
	limits Limits
	live   liveConns

	admitMu sync.Mutex // serialises the pending-count check and the save, so the bound can't overshoot
}

// GateConfig is what a gate needs.
type GateConfig struct {
	Cert         tls.Certificate // the workspace's TLS identity (fixture-only until #115)
	Blocked      PinSet          // peers refused after the handshake
	Store        Store
	Discoverable func() bool // re-read for every contact request; a relay cache can't bypass it
	Limits       Limits
	Faults       *Faults // nil for none
	// OnConnection, when set, is told about each connection after its
	// handshake: who the kernel says connected to the Unix socket and which
	// certificate the peer presented. It is for the probe's log; nil otherwise.
	OnConnection func(ConnInfo)
}

// ConnInfo describes one accepted connection.
type ConnInfo struct {
	Fingerprint string
	UID, GID    int  // from SO_PEERCRED on the Unix socket
	HasCreds    bool // false when the listener isn't a Unix socket or the lookup failed
}

// Limits bound the gate; zero values take the defaults.
type Limits struct {
	MaxConns          int
	MaxPending        int // inbound requests awaiting a decision, in total
	MaxPendingPerPeer int
	IdleTimeout       time.Duration
}

var defaultLimits = Limits{MaxConns: 16, MaxPending: 64, MaxPendingPerPeer: 4, IdleTimeout: 30 * time.Second}

func (l Limits) withDefaults() Limits {
	if l.MaxConns <= 0 {
		l.MaxConns = defaultLimits.MaxConns
	}
	if l.MaxPending <= 0 {
		l.MaxPending = defaultLimits.MaxPending
	}
	if l.MaxPendingPerPeer <= 0 {
		l.MaxPendingPerPeer = defaultLimits.MaxPendingPerPeer
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = defaultLimits.IdleTimeout
	}
	return l
}

// PinSet is a set of certificate fingerprints.
type PinSet map[string]bool

// Faults are test hooks around persistence and the reply. A non-nil error
// drops the connection at that point, as a crash would, so tests can prove
// that retries converge.
type Faults struct {
	BeforePersist func(Record) error
	AfterPersist  func(Record) error
	BeforeReply   func(Record) error
	AfterReply    func(Record) error
}

func (f *Faults) at(hook func(Record) error, rec Record) error {
	if f == nil || hook == nil {
		return nil
	}
	return hook(rec)
}

// NewGate validates the configuration.
func NewGate(cfg GateConfig) (*Gate, error) {
	switch {
	case len(cfg.Cert.Certificate) == 0:
		return nil, errors.New("a gate needs its certificate")
	case cfg.Store == nil:
		return nil, errors.New("a gate needs a store")
	case cfg.Discoverable == nil:
		return nil, errors.New("a gate needs a discoverable check")
	}
	return &Gate{
		cfg:    cfg,
		limits: cfg.Limits.withDefaults(),
		tls: &tls.Config{
			Certificates: []tls.Certificate{cfg.Cert},
			MinVersion:   tls.VersionTLS13,
			// Any client certificate is accepted at the handshake: first
			// contact is from a peer nobody has seen. Who it is becomes the
			// certificate's fingerprint; what it may do is decided below.
			ClientAuth: tls.RequireAnyClientCert,
		},
	}, nil
}

// Serve accepts connections until the context ends or the listener fails.
// Ending the context closes the listener and every live connection, and
// Serve returns once their workers have finished. Over the connection
// bound, a connection is closed before any handshake.
func (g *Gate) Serve(ctx context.Context, ln net.Listener) error {
	return g.live.serve(ctx, ln, g.limits.MaxConns, g.serve)
}

// serve handles one connection: the handshake, then one record and one
// reply at a time, within the idle timeout.
func (g *Gate) serve(ctx context.Context, raw net.Conn) {
	conn := tls.Server(raw, g.tls)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(g.limits.IdleTimeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		return
	}
	peer, ok := peerOf(conn)
	if !ok {
		return
	}
	if g.cfg.OnConnection != nil {
		info := ConnInfo{Fingerprint: peer.fingerprint}
		info.UID, info.GID, info.HasCreds = peerCredentials(raw)
		g.cfg.OnConnection(info)
	}
	if g.cfg.Blocked[peer.fingerprint] {
		_ = WriteJSON(conn, Reply{Version: SchemaVersion, Status: StatusRefused, Reason: "blocked"})
		return
	}
	r := bufio.NewReader(conn)
	for {
		_ = conn.SetDeadline(time.Now().Add(g.limits.IdleTimeout))
		rec, err := ReadRecord(r)
		if err != nil {
			g.refuse(conn, rec, err) // malformed or oversized: refused, and the connection ends
			return
		}
		if !g.handle(ctx, conn, peer, rec) {
			return
		}
	}
}

// peerInfo is the connected peer as its certificate presents it.
type peerInfo struct {
	fingerprint string
	cert        *x509.Certificate
}

func peerOf(conn *tls.Conn) (peerInfo, bool) {
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return peerInfo{}, false
	}
	return peerInfo{fingerprint: Fingerprint(certs[0]), cert: certs[0]}, true
}

// handle applies one record and writes its reply; it reports whether the
// connection may carry more.
func (g *Gate) handle(ctx context.Context, conn *tls.Conn, peer peerInfo, rec Record) bool {
	reply, err := g.apply(ctx, peer, rec)
	if err != nil {
		return false // a fault hook ended the connection; nothing is written
	}
	if err := g.cfg.Faults.at(g.cfg.Faults.beforeReply(), rec); err != nil {
		return false
	}
	if err := WriteJSON(conn, reply); err != nil {
		return false
	}
	return g.cfg.Faults.at(g.cfg.Faults.afterReply(), rec) == nil
}

func (f *Faults) beforeReply() func(Record) error {
	if f == nil {
		return nil
	}
	return f.BeforeReply
}

func (f *Faults) afterReply() func(Record) error {
	if f == nil {
		return nil
	}
	return f.AfterReply
}

// apply is the contact rule for one record. An inbound contact_request is
// stored pending; a peer's contact_accept or contact_ignore can only settle
// a request we sent to that peer.
func (g *Gate) apply(ctx context.Context, peer peerInfo, rec Record) (Reply, error) {
	switch rec.Kind {
	case KindRequest:
		return g.applyRequest(ctx, peer, rec)
	case KindAccept:
		return g.applySettle(ctx, peer, rec, StatusAccepted)
	case KindIgnore:
		return g.applySettle(ctx, peer, rec, StatusIgnored)
	}
	return refused(rec, "unsupported kind"), nil
}

func (g *Gate) applyRequest(ctx context.Context, peer peerInfo, rec Record) (Reply, error) {
	if !g.cfg.Discoverable() {
		return refused(rec, "not discoverable"), nil
	}
	if err := g.cfg.Faults.at(faultHook(g.cfg.Faults, 0), rec); err != nil {
		return Reply{}, err
	}
	out, err := g.saveAdmitted(ctx, peer, rec)
	if reply, refusedNow := out.refusal(); refusedNow {
		return reply, nil
	}
	if errors.Is(err, ErrConflict) {
		return refused(rec, "request_id already used with different content"), nil
	}
	if err != nil {
		return refused(rec, "not stored"), nil
	}
	if err := g.cfg.Faults.at(faultHook(g.cfg.Faults, 1), rec); err != nil {
		return Reply{}, err
	}
	if out.Existing {
		return Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: StatusDuplicate, Reason: out.Status}, nil
	}
	return Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: StatusPending}, nil
}

// admitted is a save's result, or a refusal decided before saving.
type admitted struct {
	Outcome
	refusedReply *Reply
}

func (a admitted) refusal() (Reply, bool) {
	if a.refusedReply == nil {
		return Reply{}, false
	}
	return *a.refusedReply, true
}

// saveAdmitted checks the pending bounds and saves under one lock, so two
// connections can't both pass the check; a repeat of a stored request still
// gets its outcome.
func (g *Gate) saveAdmitted(ctx context.Context, peer peerInfo, rec Record) (admitted, error) {
	g.admitMu.Lock()
	defer g.admitMu.Unlock()
	if _, known, _ := g.cfg.Store.Get(ctx, peer.fingerprint, rec.RequestID); !known {
		if reply := g.pendingBound(ctx, peer, rec); reply != nil {
			return admitted{refusedReply: reply}, nil
		}
	}
	out, err := g.cfg.Store.SaveRequest(ctx, Request{Fingerprint: peer.fingerprint, Cert: peer.cert.Raw, Record: rec, ReceivedAt: time.Now()})
	return admitted{Outcome: out}, err
}

// pendingBound is the refusal for a new request past the pending limits,
// or nil.
func (g *Gate) pendingBound(ctx context.Context, peer peerInfo, rec Record) *Reply {
	total, forPeer, err := g.cfg.Store.Pending(ctx, peer.fingerprint)
	var reason string
	switch {
	case err != nil:
		reason = "not stored"
	case total >= g.limits.MaxPending:
		reason = "too many pending requests"
	case forPeer >= g.limits.MaxPendingPerPeer:
		reason = "too many pending requests from you"
	default:
		return nil
	}
	r := refused(rec, reason)
	return &r
}

func (g *Gate) applySettle(ctx context.Context, peer peerInfo, rec Record, status string) (Reply, error) {
	if err := g.cfg.Faults.at(faultHook(g.cfg.Faults, 0), rec); err != nil {
		return Reply{}, err
	}
	out, err := g.cfg.Store.Settle(ctx, peer.fingerprint, rec.RequestID, status)
	switch {
	case errors.Is(err, ErrUnknown):
		return refused(rec, "no such outbound request"), nil
	case errors.Is(err, ErrDecided):
		return refused(rec, "already settled as "+out.Status), nil
	case err != nil:
		return refused(rec, "not stored"), nil
	}
	if err := g.cfg.Faults.at(faultHook(g.cfg.Faults, 1), rec); err != nil {
		return Reply{}, err
	}
	if out.Existing {
		return Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: StatusDuplicate, Reason: out.Status}, nil
	}
	return Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: out.Status}, nil
}

// faultHook is the persistence hook before (0) or after (1) the store call.
func faultHook(f *Faults, i int) func(Record) error {
	if f == nil {
		return nil
	}
	if i == 0 {
		return f.BeforePersist
	}
	return f.AfterPersist
}

func refused(rec Record, reason string) Reply {
	return Reply{Version: SchemaVersion, RequestID: rec.RequestID, Status: StatusRefused, Reason: reason}
}

// refuse answers an unreadable record; a size overrun says so without
// echoing anything from the line.
func (g *Gate) refuse(conn *tls.Conn, rec Record, err error) {
	reason := "malformed record"
	if errors.Is(err, errRecordTooLarge) {
		reason = fmt.Sprintf("record exceeds %d bytes", MaxRecordBytes)
	} else if ve := new(validationError); errors.As(err, &ve) {
		reason = ve.Error()
	}
	_ = WriteJSON(conn, refused(rec, reason))
}
