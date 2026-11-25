//go:build linux

package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
	"github.com/pairtask/pairtask/pairbook"
	"github.com/pairtask/pairtask/pairbook/owner"
)

func requireDaemonOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Seed a valid pinned map for this workspace, as it would be on the next
// startup after enrollment. Close the store before the daemon takes it.
func seedDaemonSpace(t *testing.T, dir, base string) pairbook.SignedSpaceMap {
	t.Helper()
	requireDaemonOK(t, os.MkdirAll(dir, 0700))
	st, err := store.Open(filepath.Join(dir, "broker.db"))
	requireDaemonOK(t, err)
	defer st.Close()
	ctx := context.Background()
	w, err := peer.LoadOrCreateIdentity(ctx, st.PeerDir(), st, rand.Reader)
	requireDaemonOK(t, err)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	requireDaemonOK(t, err)
	now := time.Now().UTC()
	anchor, err := owner.NewAnchor(key, now)
	requireDaemonOK(t, err)
	me, err := owner.New(key, anchor)
	requireDaemonOK(t, err)
	const id = "00112233445566778899aabbccddeeff"
	first, err := owner.NewSpace(me, owner.Genesis{Space: id, Name: "Engineering"}, now)
	requireDaemonOK(t, err)
	m := first.SpaceMap
	m.Format = pairbook.MapFormatOrg
	person := pairbook.PersonID{1}
	m.People = append(m.People, pairbook.MapPerson{Kind: pairbook.KindOrg, PersonID: person, Name: "Ada", Role: pairbook.RoleMember})
	proof := pairbook.OrgProof{Space: id, Workspace: w.Fingerprint(), Nonce: [16]byte{1}, At: now.UnixMilli()}
	sig, err := w.Sign(rand.Reader, proof.SigningBytes(), crypto.Hash(0))
	requireDaemonOK(t, err)
	signedProof := pairbook.SignedOrgProof{OrgProof: proof, Sig: sig}
	m.Bindings = []pairbook.Binding{{Kind: pairbook.KindOrg, PersonID: person, WorkspaceKey: w.PublicKey(), Proof: signedProof.Marshal()}}
	signed := pairbook.SignSpaceMap(key, m)
	requireDaemonOK(t, st.PinSpace(ctx, space.Pin{Base: base, Space: id, Owner: me.KeyID(), JoinedAt: now}))
	_, err = st.KeepSpaceMap(ctx, id, signed, now)
	requireDaemonOK(t, err)
	return signed
}

// Delay map refresh until the last client exits, then observe the heartbeat.
func serveDaemonPresence(mux *http.ServeMux, m pairbook.SignedSpaceMap, allowRefresh <-chan struct{}, presence chan<- space.Presence) {
	mux.HandleFunc("GET /v1/spaces/"+m.Space+"/map", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-allowRefresh:
			_ = json.NewEncoder(w).Encode(m)
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("PUT /v1/spaces/"+m.Space+"/agents", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("PUT /v1/spaces/"+m.Space+"/presence", func(w http.ResponseWriter, r *http.Request) {
		var beat space.Presence
		if err := json.NewDecoder(r.Body).Decode(&beat); err != nil {
			http.Error(w, "invalid presence", 400)
			return
		}
		select {
		case presence <- beat:
		default:
		}
		_, _ = w.Write([]byte("{}"))
	})
}

func TestDaemonPublishesPresenceAfterMCPClientExits(t *testing.T) {
	dir := sharedProcessDir(t)
	mux := http.NewServeMux()
	site := httptest.NewTLSServer(mux)
	t.Cleanup(site.Close)
	cert := filepath.Join(t.TempDir(), "root.pem")
	requireDaemonOK(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: site.Certificate().Raw}), 0600))
	t.Setenv("SSL_CERT_FILE", cert)
	m := seedDaemonSpace(t, dir, site.URL)
	allowRefresh := make(chan struct{})
	presence := make(chan space.Presence, 1)
	serveDaemonPresence(mux, m, allowRefresh, presence)
	d := startDaemonProcess(t, dir)
	d.waitOwner(t, dir)
	client := startMCPProcess(t, dir, "mcp")
	if got := processWhoami(t, client); got.Identity != m.Bindings[0].Workspace() {
		t.Fatal("unexpected identity", got)
	}
	client.close()
	close(allowRefresh)
	select {
	case got := <-presence:
		if got.Mark == nil || got.Mark.Version != m.Version {
			t.Fatalf("heartbeat omitted current map mark: %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no presence after chat client exited: %s", d.log.String())
	}
	d.terminate(t, dir)
}
