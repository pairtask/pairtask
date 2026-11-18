package space_test

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

func TestClientAcceptanceJoinSendsTokenOnlyInBodyAndKeepsOwnerLocal(t *testing.T) {
	node := clientKey("node")
	inv, err := space.ParseInvite(inviteLink())
	requireOK(t, err)
	calls := 0
	c := client(t, node, func(r *http.Request) (*http.Response, error) {
		calls++
		data := readRequest(t, r)
		assertJoinPrivacy(t, r, data, inv)
		assertJoinBody(t, data, inv, node)
		return response(202, map[string]string{"status": "pending"}), nil
	})
	got, err := c.Join(clientContext, inv, "Alice")
	requireOK(t, err)
	clientEqual(t, calls, 1, "join calls")
	assertClientPin(t, got, spaceID, inv.Owner, directory)
	clientEqual(t, got.JoinedAt.Equal(clientNow), true, "join time")
}

func TestClientAcceptanceReturnsOnlyOwnerVerifiedMembers(t *testing.T) {
	owner, node, other := clientKey("owner"), clientKey("node"), clientKey("other")
	valid := signedMember(owner, node, "Alice")
	cross := signedMember(owner, other, "Cross")
	cross.Space = "fedcba9876543210fedcba9876543210"
	cross = pairbook.SignMember(owner, cross.Member)
	expired := signedMember(owner, other, "Expired")
	expired.IssuedAt = clientNow.Add(-time.Hour).UnixMilli()
	expired.ExpiresAt = clientNow.UnixMilli()
	expired = pairbook.SignMember(owner, expired.Member)
	roster := pairbook.Roster{Members: []pairbook.SignedMember{valid, signedMember(other, other, "Forged"), cross, expired}}
	c := client(t, node, func(r *http.Request) (*http.Response, error) { readRequest(t, r); return response(200, roster), nil })
	members, err := c.Members(clientContext, pin())
	requireOK(t, err)
	if len(members) != 1 || members[0].Name != "Alice" {
		t.Fatalf("untrusted members returned: %+v", members)
	}
	roster.Revocations = []pairbook.SignedRevocation{pairbook.SignRevocation(owner, pairbook.Revocation{Space: spaceID, Node: pub(node), By: pub(owner), At: valid.IssuedAt})}
	members, err = c.Members(clientContext, pin())
	requireOK(t, err)
	if len(members) != 0 {
		t.Fatal("revoked member returned")
	}
}

func TestClientAcceptanceAddressRequiresVerifiedMembershipAndMatchingNode(t *testing.T) {
	owner, node, other := clientKey("owner"), clientKey("node"), clientKey("other")
	roster := pairbook.Roster{Members: []pairbook.SignedMember{signedMember(owner, node, "Alice")}}
	record := pairbook.Address{Space: spaceID, Node: pub(node), Addrs: []string{"node.example:7445"}, IssuedAt: clientNow.UnixMilli(), ExpiresAt: clientNow.Add(time.Hour).UnixMilli()}
	address := pairbook.SignAddress(node, record)
	c := client(t, node, func(r *http.Request) (*http.Response, error) {
		readRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/members") {
			return response(200, roster), nil
		}
		return response(200, address), nil
	})
	got, err := c.Address(clientContext, pin(), pub(node))
	requireOK(t, err)
	if !bytes.Equal(got.Node, pub(node)) {
		t.Fatal("wrong address node")
	}
	address = pairbook.SignAddress(other, pairbook.Address{Space: spaceID, Node: pub(other), Addrs: record.Addrs, IssuedAt: record.IssuedAt, ExpiresAt: record.ExpiresAt})
	_, err = c.Address(clientContext, pin(), pub(node))
	requireError(t, err)
	address = pairbook.SignAddress(node, record)
	roster.Members = nil
	_, err = c.Address(clientContext, pin(), pub(node))
	requireError(t, err)
}

func TestClientAcceptanceDirectoryBindingBeforeNetwork(t *testing.T) {
	calls := 0
	c := client(t, clientKey("node"), func(r *http.Request) (*http.Response, error) { calls++; return response(200, pairbook.Roster{}), nil })
	wrong := pin()
	wrong.Base = "https://attacker.example"
	_, err := c.Members(clientContext, wrong)
	if !errors.Is(err, space.ErrDirectory) {
		t.Fatalf("wrong error: %v", err)
	}
	inv, err := space.ParseInvite(inviteLink())
	requireOK(t, err)
	inv.Base = wrong.Base
	_, err = c.Join(clientContext, inv, "Alice")
	if !errors.Is(err, space.ErrDirectory) {
		t.Fatalf("wrong error: %v", err)
	}
	if calls != 0 {
		t.Fatal("directory mismatch sent a signed request")
	}
}

type failingSigner struct{ ed25519.PrivateKey }

func (f failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("signer unavailable")
}

type failedEntropy struct{}

func (failedEntropy) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestClientAcceptanceSigningAndEntropyFailuresSendNothing(t *testing.T) {
	for name, opt := range map[string]struct {
		signer crypto.Signer
		random io.Reader
	}{
		"signer":  {failingSigner{clientKey("node")}, nil},
		"entropy": {clientKey("node"), failedEntropy{}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			h := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return response(200, pairbook.Roster{}), nil })}
			c, err := space.NewClient(directory, opt.signer, space.Options{HTTP: h, Nonce: opt.random, Now: func() time.Time { return clientNow }})
			requireOK(t, err)
			_, err = c.Members(clientContext, pin())
			requireError(t, err)
			if calls != 0 {
				t.Fatal("failed signature/nonce sent request")
			}
		})
	}
}

func TestClientAcceptanceRefusesRedirectWithoutMutatingSharedClient(t *testing.T) {
	calls, redirectCalls := 0, 0
	shared := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalls++; return nil }, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		out := response(307, nil)
		out.Header.Set("Location", "https://attacker.example/leak")
		return out, nil
	})}
	c, err := space.NewClient(directory, clientKey("node"), space.Options{HTTP: shared, Now: func() time.Time { return clientNow }})
	requireOK(t, err)
	_, err = c.Members(clientContext, pin())
	requireError(t, err)
	if calls != 1 || redirectCalls != 0 {
		t.Fatal("signed request followed redirect")
	}
	if shared.CheckRedirect == nil {
		t.Fatal("shared client modified")
	}
	requireOK(t, shared.CheckRedirect(nil, nil))
	if redirectCalls != 1 {
		t.Fatal("shared redirect callback replaced")
	}
}

func TestClientAcceptanceRejectsBadDirectoryResponses(t *testing.T) {
	for name, reply := range map[string]func() *http.Response{
		"status": func() *http.Response { return response(403, map[string]string{"error": "forbidden"}) },
		"malformed": func() *http.Response {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("not-json"))}
		},
		"oversize": func() *http.Response {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", space.MaxResponse+1) + "{}"))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := client(t, clientKey("node"), func(*http.Request) (*http.Response, error) { return reply(), nil })
			_, err := c.Members(clientContext, pin())
			requireError(t, err)
		})
	}
}

func assertJoinPrivacy(t *testing.T, r *http.Request, data []byte, inv space.Invite) {
	t.Helper()
	clientEqual(t, r.Method, "POST", "join method")
	clientEqual(t, r.URL.Path, "/v0/spaces/"+spaceID+"/requests", "join endpoint")
	clientEqual(t, r.URL.Fragment, "", "URL fragment")
	clientEqual(t, strings.Contains(r.URL.String(), "token="), false, "token in URL")
	clientEqual(t, strings.Contains(r.URL.String(), inv.Owner), false, "owner in URL")
	clientEqual(t, strings.Contains(string(data), inv.Owner), false, "owner in body")
	for _, values := range r.Header {
		for _, v := range values {
			clientEqual(t, strings.Contains(v, inv.Owner), false, "owner in header")
			clientEqual(t, strings.Contains(v, "token="), false, "token in header")
		}
	}
}
func assertJoinBody(t *testing.T, data []byte, inv space.Invite, node ed25519.PrivateKey) {
	t.Helper()
	body := decode[struct {
		Request pairbook.SignedJoinRequest `json:"request"`
		Token   string                     `json:"token"`
	}](t, data)
	clientEqual(t, body.Token, base64.RawURLEncoding.EncodeToString(inv.Token[:]), "join token")
	clientEqual(t, body.Request.TokenHash, sha256.Sum256(inv.Token[:]), "token hash")
	clientEqual(t, bytes.Equal(body.Request.Node, pub(node)), true, "joining identity")
	requireOK(t, body.Request.Verify(spaceID, clientNow))
}
func assertClientPin(t *testing.T, got space.Pin, id, owner, base string) {
	t.Helper()
	clientEqual(t, got.Space, id, "space")
	clientEqual(t, got.Owner, owner, "owner")
	clientEqual(t, got.Base, base, "directory")
}

func clientEqual[T comparable](t *testing.T, got, want T, field string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", field, got, want)
	}
}
