//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
)

const connectSpaceID = "0123456789abcdef0123456789abcdef"
const connectDirectory = "https://directory.example"

type connectDirectoryFixture struct {
	mu        sync.Mutex
	roster    pairbook.Roster
	addresses map[string]pairbook.SignedAddress
}
type connectHTTPTransport struct{ directory *connectDirectoryFixture }

func (tr connectHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.directory.mu.Lock()
	defer tr.directory.mu.Unlock()
	var value any = tr.directory.roster
	status := 200
	if strings.Contains(r.URL.Path, "/addresses/") {
		key := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		address, found := tr.directory.addresses[key]
		value = address
		if !found {
			status = 404
		}
	}
	data, err := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, err
}

type connectNode struct {
	h        *hub.Hub
	st       *store.Store
	ctx      context.Context
	identity *peer.WorkspaceIdentity
	address  string
	dials    atomic.Int32
	db       *sql.DB
	dropNext atomic.Bool
	dropped  chan space.ConnectRequest
}

func newConnectNode(t *testing.T, directory *connectDirectoryFixture) *connectNode {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.db")
	st, err := store.Open(path)
	operatorNoError(t, err)
	log := slog.New(slog.DiscardHandler)
	h := hub.New(st, 0, log)
	n := &connectNode{h: h, st: st, db: openFixtureInspection(t, path), dropped: make(chan space.ConnectRequest, 1)}
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	cfg.Dial = func(ctx context.Context, address string) (net.Conn, error) {
		n.dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	h.SetPairing(cfg)
	h.SetSpaces(hub.SpaceConfig{Directory: func(base string, signer crypto.Signer) (space.Directory, error) {
		return space.NewClient(base, signer, space.Options{HTTP: &http.Client{Transport: connectHTTPTransport{directory}}})
	}})
	ctx, cancel := context.WithCancel(context.Background())
	n.ctx = ctx
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done(); st.Close(); n.db.Close() })
	// The public operator status path loads the workspace identity.
	out := h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: "status"})
	operatorEqual(t, "identity status", out.Type, protocol.TypeOperatorPairingResult)
	ep, err := h.PairingEndpoint()
	operatorNoError(t, err)
	n.identity = ep.Identity
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	operatorNoError(t, err)
	n.address = ln.Addr().String()
	server := NewPairing("", h, log)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serve(server, raw)
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	return n
}

type connectPair struct {
	a, b      *connectNode
	directory *connectDirectoryFixture
	owner     ed25519.PrivateKey
}

func newConnectPair(t *testing.T) *connectPair {
	t.Helper()
	seed := sha256.Sum256([]byte("space integration owner"))
	owner := ed25519.NewKeyFromSeed(seed[:])
	d := &connectDirectoryFixture{addresses: make(map[string]pairbook.SignedAddress)}
	p := &connectPair{directory: d, owner: owner}
	p.a = newConnectNode(t, d)
	p.b = newConnectNode(t, d)
	now := time.Now()
	for i, n := range []*connectNode{p.a, p.b} {
		name := []string{"Alice", "Bob"}[i]
		member := pairbook.Member{Space: connectSpaceID, Node: n.identity.PublicKey(), Name: name, Role: pairbook.RoleMember, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
		d.roster.Members = append(d.roster.Members, pairbook.SignMember(owner, member))
		// WorkspaceIdentity is a crypto.Signer; its own signature authenticates the address.
		record := pairbook.Address{Space: connectSpaceID, Node: n.identity.PublicKey(), Addrs: []string{n.address}, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
		sig, err := n.identity.Sign(nil, record.SigningBytes(), crypto.Hash(0))
		operatorNoError(t, err)
		signed := pairbook.SignedAddress{Address: record, Sig: sig}
		d.addresses[n.identity.Fingerprint()] = signed
		operatorNoError(t, n.st.PinSpace(n.ctx, space.Pin{Space: connectSpaceID, Base: connectDirectory, Owner: pairbook.KeyID(owner.Public().(ed25519.PublicKey)), JoinedAt: now}))
	}
	return p
}
func (n *connectNode) connect(target string) protocol.Message {
	return n.h.SubmitOperator(n.ctx, 0, protocol.Message{Type: protocol.TypeOperatorSpace, SpaceRequest: &protocol.SpaceRequest{Op: protocol.SpaceOpConnect, Space: connectSpaceID, Node: target}})
}
func requireConnected(t *testing.T, out protocol.Message) *protocol.PairingContactView {
	t.Helper()
	operatorEqual(t, "connect response: "+out.Detail, out.Type, protocol.TypeOperatorSpaceResult)
	if out.SpaceResult == nil {
		t.Fatal("missing space result")
	}
	if out.SpaceResult.Contact == nil {
		t.Fatal("missing contact")
	}
	return out.SpaceResult.Contact
}
func assertNoConnectContact(t *testing.T, n *connectNode) {
	t.Helper()
	contacts, err := n.st.Contacts(n.ctx)
	operatorNoError(t, err)
	operatorEqual(t, "contacts", len(contacts), 0)
}

func (n *connectNode) serve(server *PairingServer, raw net.Conn) {
	if !n.dropNext.Swap(false) {
		server.serve(n.ctx, raw)
		return
	}
	defer raw.Close()
	ep, err := n.h.PairingEndpoint()
	if err != nil {
		return
	}
	conn, err := firstcontact.ServerWith(n.ctx, raw, ep.Identity, time.Now, firstcontact.ServerOptions{SpacePins: ep.SpacePins})
	if err != nil {
		return
	}
	defer conn.Close()
	frame, err := conn.Frames(n.ctx).Recv()
	if err != nil {
		return
	}
	request, err := space.DecodeConnectRequest(frame)
	if err != nil {
		return
	}
	answer := ep.SpaceConnect(n.ctx, conn.Connection().PeerLeaf, request)
	if answer.Accepted {
		n.dropped <- request
	}
	// Simulate connection loss after the responder committed acceptance.
}
func (n *connectNode) pairing(t *testing.T, key string) (int64, []byte) {
	t.Helper()
	var epoch int64
	var id []byte
	operatorNoError(t, n.db.QueryRow(`SELECT pairing_epoch,pairing_id FROM peer_contacts WHERE identity_key=?`, key).Scan(&epoch, &id))
	return epoch, id
}
func assertConnectNoGrants(t *testing.T, n *connectNode) {
	t.Helper()
	var count int
	operatorNoError(t, n.db.QueryRow(`SELECT count(*) FROM peer_grants`).Scan(&count))
	operatorEqual(t, "space connect must not grant agent access", count, 0)
}
