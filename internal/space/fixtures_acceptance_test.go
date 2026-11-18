package space_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

const directory = "https://directory.example"
const spaceID = "0123456789abcdef0123456789abcdef"

var clientNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
var clientContext = context.Background()

func clientKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("space acceptance " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}
func pub(key ed25519.PrivateKey) ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }
func pin() space.Pin {
	return space.Pin{Space: spaceID, Owner: pairbook.KeyID(pub(clientKey("owner"))), Base: directory, JoinedAt: clientNow}
}
func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("untrusted input accepted")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}
func client(t *testing.T, signer crypto.Signer, fn transportFunc) *space.Client {
	t.Helper()
	c, err := space.NewClient(directory, signer, space.Options{HTTP: &http.Client{Transport: fn}, Now: func() time.Time { return clientNow }})
	requireOK(t, err)
	return c
}
func readRequest(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var data []byte
	var err error
	if r.Body != nil {
		data, err = io.ReadAll(r.Body)
		requireOK(t, err)
	}
	req := pairbook.HTTPRequest{Method: r.Method, Target: r.URL.RequestURI(), Body: data}
	a, err := pairbook.ParseHeader(r.Header.Get("Authorization"), req)
	requireOK(t, err)
	requireOK(t, a.Verify(req.Method, req.Target, data, clientNow))
	return data
}
func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var out T
	requireOK(t, json.Unmarshal(data, &out))
	return out
}
func signedMember(owner, node ed25519.PrivateKey, name string) pairbook.SignedMember {
	return pairbook.SignMember(owner, pairbook.Member{Space: spaceID, Node: pub(node), Name: name, Role: pairbook.RoleMember, IssuedAt: clientNow.UnixMilli(), ExpiresAt: clientNow.Add(time.Hour).UnixMilli()})
}
