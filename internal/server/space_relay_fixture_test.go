//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

const fixtureRelayID = "00001111222233334444555566667777"

type relayOfferFixture struct {
	accepted chan *websocket.Conn
	done     chan struct{}
}
type spaceRelayFixture struct {
	server     *httptest.Server
	mu         sync.Mutex
	reach      map[string]pairbook.SignedRelayReach
	slots      map[string]*websocket.Conn
	offers     map[string]*relayOfferFixture
	initiators []string
}

func newSpaceRelayFixture(t *testing.T) *spaceRelayFixture {
	t.Helper()
	f := &spaceRelayFixture{reach: map[string]pairbook.SignedRelayReach{}, slots: map[string]*websocket.Conn{}, offers: map[string]*relayOfferFixture{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/relays", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string][]space.RelayInfo{"relays": {{ID: fixtureRelayID, URL: f.server.URL}}})
	})
	mux.HandleFunc("GET /v1/spaces/{id}/addresses/{fp}", relayFixtureNotFound)
	mux.HandleFunc("POST /v1/spaces/{id}/relay-grants", f.grant)
	mux.HandleFunc("/v1/spaces/{id}/relay-reach/{fp}", f.record)
	mux.HandleFunc("/relay/", f.socket)
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}
func relayFixtureNotFound(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"error": "not_found"})
}
func (f *spaceRelayFixture) grant(w http.ResponseWriter, r *http.Request) {
	var in struct{ To string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(space.RelayGrant{Token: in.To, RelayID: fixtureRelayID, URL: f.server.URL, ExpiresAt: time.Now().Add(5 * time.Minute).UnixMilli()})
}
func (f *spaceRelayFixture) record(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("fp")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == "GET" {
		record, ok := f.reach[key]
		if !ok {
			relayFixtureNotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(record)
		return
	}
	var record pairbook.SignedRelayReach
	if json.NewDecoder(r.Body).Decode(&record) != nil {
		w.WriteHeader(400)
		return
	}
	if record.Verify(autoSpaceID, time.Now()) != nil || pairbook.KeyID(record.Node) != key {
		w.WriteHeader(400)
		return
	}
	f.reach[key] = record
	w.WriteHeader(204)
}

func (f *spaceRelayFixture) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"pairbook-relay.v1"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	key, token, ok := fixtureCredentials(ctx, conn)
	if !ok {
		return
	}
	switch r.URL.Path {
	case "/relay/listen":
		f.listen(ctx, conn, key)
	case "/relay/dial":
		f.splice(ctx, conn, key, token)
	case "/relay/accept":
		f.accept(ctx, conn, r.URL.Query().Get("splice"))
	}
}

func fixtureCredentials(ctx context.Context, conn *websocket.Conn) (string, string, bool) {
	if wsjson.Write(ctx, conn, map[string]string{"kind": "challenge", "nonce": "fixture"}) != nil {
		return "", "", false
	}
	var auth struct{ Key, Token string }
	if wsjson.Read(ctx, conn, &auth) != nil {
		return "", "", false
	}
	pub, err := base64.RawURLEncoding.DecodeString(auth.Key)
	if err != nil {
		return "", "", false
	}
	return pairbook.KeyID(pub), auth.Token, true
}

func (f *spaceRelayFixture) listen(ctx context.Context, conn *websocket.Conn, key string) {
	f.mu.Lock()
	f.slots[key] = conn
	f.mu.Unlock()
	if wsjson.Write(ctx, conn, map[string]string{"kind": "ready"}) != nil {
		return
	}
	<-conn.CloseRead(ctx).Done()
}

func (f *spaceRelayFixture) accept(ctx context.Context, conn *websocket.Conn, id string) {
	f.mu.Lock()
	p := f.offers[id]
	f.mu.Unlock()
	if p != nil {
		select {
		case p.accepted <- conn:
			<-p.done
		case <-p.done:
		case <-ctx.Done():
		}
	}
}

// This fixture supplies the opaque byte connection. Both hubs run the
// production pinned handshake, contact installation and message protocol.
func (f *spaceRelayFixture) splice(ctx context.Context, dial *websocket.Conn, from, to string) {
	var nonce [16]byte
	rand.Read(nonce[:])
	id := hex.EncodeToString(nonce[:])
	offer := &relayOfferFixture{accepted: make(chan *websocket.Conn), done: make(chan struct{})}
	f.mu.Lock()
	slot := f.slots[to]
	f.offers[id] = offer
	f.initiators = append(f.initiators, from)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); delete(f.offers, id); f.mu.Unlock(); close(offer.done) }()
	if slot == nil {
		dial.Close(4404, "offline")
		return
	}
	if wsjson.Write(ctx, slot, map[string]string{"kind": "incoming", "splice": id, "from": from}) != nil {
		return
	}
	select {
	case accept := <-offer.accepted:
		ready := map[string]string{"kind": "ready"}
		if wsjson.Write(ctx, dial, ready) != nil || wsjson.Write(ctx, accept, ready) != nil {
			return
		}
		a := websocket.NetConn(ctx, dial, websocket.MessageBinary)
		b := websocket.NetConn(ctx, accept, websocket.MessageBinary)
		done := make(chan struct{}, 1)
		go func() { io.Copy(a, b); done <- struct{}{} }()
		go func() { io.Copy(b, a); done <- struct{}{} }()
		<-done
		dial.CloseNow()
		accept.CloseNow()
		<-done
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
}
