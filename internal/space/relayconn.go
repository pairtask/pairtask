package space

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The relay client side of wire v1 (designs/pairbook/relay.md): one
// WebSocket per listen slot and per splice leg, each opened with a fresh
// challenge answered by this workspace's key over its grant.

const (
	relaySubprotocol = "pairbook-relay.v1"
	relayProofDomain = "pairbook-relay-proof-v1"
	maxRelayData     = 32 << 10 // bytes of peer stream in one binary message
	maxRelayControl  = 8 << 10  // bytes in one control message
	relayHandshake   = 5 * time.Second
	relayAcceptWait  = 20 * time.Second // a dial waits this long for the target's accept
	renewAhead       = time.Minute      // renew this long before a grant expires
	slotPing         = 25 * time.Second // keeps an idle listen slot open through ingress idle timeouts
)

// Relay close codes a caller can act on.
var (
	ErrRelayOffline  = errors.New("space: the workspace holds no slot on the relay") // 4404
	ErrRelayNoAccept = errors.New("space: the workspace didn't accept in time")      // 4408
	ErrRelayDeclined = errors.New("space: the workspace declined the connection")    // 4409
	ErrRelayReplaced = errors.New("space: another registration replaced this slot")  // 4001
	ErrRelayFull     = errors.New("space: the relay is at capacity")                 // 1013
	ErrRelayRefused  = errors.New("space: the relay refused the grant")              // 1008
)

var errMalformedControl = errors.New("space: malformed relay control message")

// GrantFunc gets a fresh grant on one relay for to (ListenGrant or a key ID).
type GrantFunc func(ctx context.Context, to string) (RelayGrant, error)

// RelaySigner signs relay proofs with this workspace's key.
type RelaySigner struct {
	Key  ed25519.PublicKey
	Sign func(msg []byte) ([]byte, error)
}

// RelayEnd is this workspace on one relay: how it gets grants there, how
// it proves its key, and the HTTP client that reaches the relay (the one
// that reaches its Pairbook, so a Box's egress applies). Nil HTTP is the
// default client.
type RelayEnd struct {
	Grant GrantFunc
	Self  RelaySigner
	HTTP  *http.Client
}

// RelayEnd is this workspace on relay, with grants from pin's Pairbook.
func (c *Client) RelayEnd(pin Pin, relay string) RelayEnd {
	return RelayEnd{
		Grant: func(ctx context.Context, to string) (RelayGrant, error) { return c.Grant(ctx, pin, to, relay) },
		Self:  RelaySigner{Key: c.self, Sign: c.sign},
		HTTP:  c.http,
	}
}

type relayControl struct {
	Kind      string `json:"kind"`
	Nonce     string `json:"nonce,omitempty"`
	Token     string `json:"token,omitempty"`
	Key       string `json:"key,omitempty"`
	Signature string `json:"signature,omitempty"`
	Splice    string `json:"splice,omitempty"`
	From      string `json:"from,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

// relaySocket is one authenticated socket: who it is for, and how to renew.
type relaySocket struct {
	ws    *websocket.Conn
	grant GrantFunc
	to    string
}

func (e RelayEnd) dialOptions() *websocket.DialOptions {
	return &websocket.DialOptions{HTTPClient: e.HTTP, Subprotocols: []string{relaySubprotocol}, CompressionMode: websocket.CompressionDisabled}
}

// openRelay opens target on the grant's relay and authenticates: challenge,
// then a proof over the relay ID, the exact target, the nonce and the
// token's digest.
func openRelay(ctx context.Context, end RelayEnd, to, target string) (*relaySocket, RelayGrant, error) {
	g, err := end.Grant(ctx, to)
	if err != nil {
		return nil, g, err
	}
	hctx, cancel := context.WithTimeout(ctx, relayHandshake)
	defer cancel()
	ws, _, err := websocket.Dial(hctx, strings.TrimRight(g.URL, "/")+target, end.dialOptions())
	if err != nil {
		return nil, g, err
	}
	ws.SetReadLimit(maxRelayData)
	s := &relaySocket{ws: ws, grant: end.Grant, to: to}
	if err := s.authenticate(hctx, g, target, end.Self); err != nil {
		ws.CloseNow()
		return nil, g, err
	}
	return s, g, nil
}

func (s *relaySocket) authenticate(ctx context.Context, g RelayGrant, target string, self RelaySigner) error {
	if s.ws.Subprotocol() != relaySubprotocol {
		return fmt.Errorf("space: relay spoke %q, not %s", s.ws.Subprotocol(), relaySubprotocol)
	}
	c, err := s.control(ctx)
	if err != nil {
		return err
	}
	if c.Kind != "challenge" || c.Nonce == "" {
		return errors.New("space: the relay sent no challenge")
	}
	sig, err := self.Sign([]byte(relayProof(g, target, c.Nonce)))
	if err != nil {
		return err
	}
	return s.send(ctx, relayControl{Kind: "auth", Token: g.Token, Key: b64.EncodeToString(self.Key), Signature: b64.EncodeToString(sig)})
}

// relayProof is the text a proof signs, fields joined by LF.
func relayProof(g RelayGrant, target, nonce string) string {
	digest := sha256.Sum256([]byte(g.Token))
	return strings.Join([]string{relayProofDomain, g.RelayID, target, nonce, hex.EncodeToString(digest[:])}, "\n")
}

// control reads the next message, which must be a control message.
func (s *relaySocket) control(ctx context.Context) (relayControl, error) {
	typ, data, err := s.ws.Read(ctx)
	if err != nil {
		return relayControl{}, relayClosed(err)
	}
	if typ != websocket.MessageText {
		return relayControl{}, errors.New("space: the relay sent data before ready")
	}
	return parseControl(data)
}

func parseControl(data []byte) (relayControl, error) {
	var c relayControl
	if len(data) > maxRelayControl {
		return c, errMalformedControl
	}
	if json.Unmarshal(data, &c) != nil || c.Kind == "" {
		return c, errMalformedControl
	}
	return c, nil
}

func (s *relaySocket) send(ctx context.Context, c relayControl) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// awaitReady waits for ready, refusing anything else.
func (s *relaySocket) awaitReady(ctx context.Context, d time.Duration) error {
	rctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	c, err := s.control(rctx)
	if err != nil {
		return err
	}
	if c.Kind != "ready" {
		return fmt.Errorf("space: the relay sent %q before ready", c.Kind)
	}
	return nil
}

// renewLoop sends a fresh grant before the current one expires, until ctx
// ends. A failed renewal closes the socket: the relay would anyway.
func (s *relaySocket) renewLoop(ctx context.Context, expires int64) {
	for {
		wait := time.Until(time.UnixMilli(expires).Add(-renewAhead))
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, time.Second)):
		}
		g, err := s.grant(ctx, s.to)
		if err == nil {
			err = s.send(ctx, relayControl{Kind: "renew", Token: g.Token})
		}
		if err != nil {
			s.ws.Close(websocket.StatusNormalClosure, "renewal failed")
			return
		}
		expires = g.ExpiresAt
	}
}

// relayClosed maps a relay's close code to an error a caller can act on.
func relayClosed(err error) error {
	switch websocket.CloseStatus(err) {
	case 4404:
		return ErrRelayOffline
	case 4408:
		return ErrRelayNoAccept
	case 4409:
		return ErrRelayDeclined
	case 4001:
		return ErrRelayReplaced
	case websocket.StatusTryAgainLater:
		return ErrRelayFull
	case websocket.StatusPolicyViolation:
		return ErrRelayRefused
	}
	return err
}

// DialRelay opens a splice to target (a key ID) through grant's relay and
// returns the peer byte stream once the target accepted. The peer protocol
// runs end to end over it, unchanged.
func DialRelay(ctx context.Context, end RelayEnd, target string) (net.Conn, error) {
	s, g, err := openRelay(ctx, end, target, "/relay/dial")
	if err != nil {
		return nil, err
	}
	if err := s.awaitReady(ctx, relayAcceptWait); err != nil {
		s.ws.CloseNow()
		return nil, err
	}
	return newSpliceConn(s, g.ExpiresAt), nil
}

// RelayOffer is one incoming connection on a listen slot.
type RelayOffer struct {
	Splice string
	From   string // the dialer's key ID
}

// RelayListener is this workspace's listen slot on one relay.
type RelayListener struct {
	s      *relaySocket
	end    RelayEnd
	cancel context.CancelFunc
	offers chan RelayOffer
	done   chan struct{}
	err    error
}

// ListenRelay registers this workspace's slot on end's relay. The same end
// gets the grants for accepts, one naming each dialer.
func ListenRelay(ctx context.Context, end RelayEnd) (*RelayListener, error) {
	s, g, err := openRelay(ctx, end, ListenGrant, "/relay/listen")
	if err != nil {
		return nil, err
	}
	if err := s.awaitReady(ctx, relayHandshake); err != nil {
		s.ws.CloseNow()
		return nil, err
	}
	lctx, cancel := context.WithCancel(ctx)
	l := &RelayListener{s: s, end: end, cancel: cancel, offers: make(chan RelayOffer, 8), done: make(chan struct{})}
	go s.renewLoop(lctx, g.ExpiresAt)
	go l.read(lctx)
	go l.keepAlive(lctx)
	return l, nil
}

// keepAlive pings the idle slot, so an ingress (nginx closes a quiet proxied
// connection after 60s by default) doesn't. Data legs need none: the peer
// link's own heartbeat runs every 15 seconds.
func (l *RelayListener) keepAlive(ctx context.Context) {
	tick := time.NewTicker(slotPing)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		pctx, cancel := context.WithTimeout(ctx, slotPing)
		err := l.s.ws.Ping(pctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			l.s.ws.Close(websocket.StatusGoingAway, "no pong")
			return
		}
	}
}

// Offers are incoming connections; the channel closes when the slot ends.
func (l *RelayListener) Offers() <-chan RelayOffer { return l.offers }

// Done is closed when the slot ends.
func (l *RelayListener) Done() <-chan struct{} { return l.done }

// Err is why the slot ended, once Offers is closed.
func (l *RelayListener) Err() error {
	<-l.done
	return l.err
}

func (l *RelayListener) read(ctx context.Context) {
	defer close(l.done)
	defer close(l.offers)
	defer l.cancel() // the slot is gone: stop renewing it
	for {
		c, err := l.s.control(ctx)
		if err != nil {
			l.err = err
			return
		}
		if c.Kind != "incoming" || !validSplice(c.Splice) {
			continue // renewed, or nothing this side acts on
		}
		select {
		case l.offers <- RelayOffer{Splice: c.Splice, From: c.From}:
		default:
			_ = l.s.send(ctx, relayControl{Kind: "cancel", Splice: c.Splice}) // too many waiting: decline
		}
	}
}

// Accept takes offer: it opens the accept leg with a grant naming the
// dialer, and returns the peer byte stream.
func (l *RelayListener) Accept(ctx context.Context, offer RelayOffer) (net.Conn, error) {
	s, g, err := openRelay(ctx, l.end, offer.From, "/relay/accept?splice="+offer.Splice)
	if err != nil {
		return nil, err
	}
	if err := s.awaitReady(ctx, relayHandshake); err != nil {
		s.ws.CloseNow()
		return nil, err
	}
	return newSpliceConn(s, g.ExpiresAt), nil
}

// Decline refuses offer on the slot.
func (l *RelayListener) Decline(ctx context.Context, offer RelayOffer) error {
	return l.s.send(ctx, relayControl{Kind: "cancel", Splice: offer.Splice})
}

// Close ends the slot.
func (l *RelayListener) Close() error {
	l.cancel()
	return l.s.ws.Close(websocket.StatusNormalClosure, "")
}

// validSplice: 16 bytes as 32 lowercase hex characters.
func validSplice(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && strings.ToLower(id) == id
}

// spliceConn is one splice leg as a net.Conn: binary messages carry the
// peer stream; control messages (renewed) are read past. As with
// websocket.NetConn, a deadline that passes mid-read ends the socket.
type spliceConn struct {
	s      *relaySocket
	cancel context.CancelFunc
	ctx    context.Context

	rmu  sync.Mutex
	rbuf []byte

	dmu                     sync.Mutex
	readDeadline, wDeadline time.Time
}

func newSpliceConn(s *relaySocket, expires int64) *spliceConn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &spliceConn{s: s, ctx: ctx, cancel: cancel}
	go s.renewLoop(ctx, expires)
	return c
}

func (c *spliceConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for len(c.rbuf) == 0 {
		if err := c.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, c.rbuf)
	c.rbuf = c.rbuf[n:]
	return n, nil
}

// next reads one message: data is buffered, control is checked and passed.
func (c *spliceConn) next() error {
	ctx, cancel := c.deadlineCtx(c.deadline(true))
	defer cancel()
	typ, data, err := c.s.ws.Read(ctx)
	if err != nil {
		defer c.cancel() // a failed read ends the socket: stop renewing it
		return c.readErr(ctx, err)
	}
	if typ == websocket.MessageBinary {
		c.rbuf = data
		return nil
	}
	_, err = parseControl(data)
	return err
}

func (c *spliceConn) readErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutError{}
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return io.EOF
	}
	return relayClosed(err)
}

func (c *spliceConn) Write(p []byte) (int, error) {
	ctx, cancel := c.deadlineCtx(c.deadline(false))
	defer cancel()
	written := 0
	for len(p) > 0 {
		n := min(len(p), maxRelayData)
		if err := c.s.ws.Write(ctx, websocket.MessageBinary, p[:n]); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return written, timeoutError{}
			}
			return written, relayClosed(err)
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *spliceConn) Close() error {
	c.cancel()
	return c.s.ws.Close(websocket.StatusNormalClosure, "")
}

func (c *spliceConn) deadline(read bool) time.Time {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	if read {
		return c.readDeadline
	}
	return c.wDeadline
}

func (c *spliceConn) deadlineCtx(t time.Time) (context.Context, context.CancelFunc) {
	if t.IsZero() {
		return context.WithCancel(c.ctx)
	}
	return context.WithDeadline(c.ctx, t)
}

func (c *spliceConn) SetDeadline(t time.Time) error {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	c.readDeadline, c.wDeadline = t, t
	return nil
}

func (c *spliceConn) SetReadDeadline(t time.Time) error {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	c.readDeadline = t
	return nil
}

func (c *spliceConn) SetWriteDeadline(t time.Time) error {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	c.wDeadline = t
	return nil
}

func (c *spliceConn) LocalAddr() net.Addr  { return relayAddr("local") }
func (c *spliceConn) RemoteAddr() net.Addr { return relayAddr("relay") }

type relayAddr string

func (a relayAddr) Network() string { return "pairbook-relay" }
func (a relayAddr) String() string  { return string(a) }

type timeoutError struct{}

func (timeoutError) Error() string   { return "space: relay i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
