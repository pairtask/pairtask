package space_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/pairbook"
)

func TestClientAcceptanceOwnerOperationsSignExpectedRecords(t *testing.T) {
	owner, node := clientKey("owner"), clientKey("node")
	calls := 0
	c := client(t, owner, func(r *http.Request) (*http.Response, error) {
		calls++
		data := readRequest(t, r)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v0/spaces":
			m := decode[pairbook.SignedMember](t, data)
			requireOK(t, m.Verify(spaceID, pin().Owner, clientNow))
			if m.Role != pairbook.RoleOwner || m.Name != "Office" || !bytes.Equal(m.Node, pub(owner)) {
				t.Fatal("invalid owner creation")
			}
			return response(201, struct{}{}), nil
		case strings.HasSuffix(r.URL.Path, "/invites"):
			inv := decode[pairbook.SignedInvite](t, data)
			requireOK(t, inv.Verify(spaceID, pin().Owner, clientNow))
			if inv.ExpiresAt != clientNow.Add(time.Hour).UnixMilli() {
				t.Fatal("wrong invitation expiry")
			}
			return response(201, struct{}{}), nil
		case strings.HasSuffix(r.URL.Path, "/members"):
			m := decode[pairbook.SignedMember](t, data)
			requireOK(t, m.Verify(spaceID, pin().Owner, clientNow))
			if !bytes.Equal(m.Node, pub(node)) || m.Name != "Alice" || m.Role != pairbook.RoleMember {
				t.Fatal("wrong admission")
			}
			return response(201, struct{}{}), nil
		case strings.Contains(r.URL.Path, "/addresses/"):
			a := decode[pairbook.SignedAddress](t, data)
			requireOK(t, a.Verify(spaceID, clientNow))
			if r.Method != "PUT" || !bytes.Equal(a.Node, pub(owner)) || !strings.HasSuffix(r.URL.Path, pin().Owner) {
				t.Fatal("wrong address publisher")
			}
			return response(204, nil), nil
		case strings.HasSuffix(r.URL.Path, "/revocations"):
			rev := decode[pairbook.SignedRevocation](t, data)
			requireOK(t, rev.Verify(spaceID, pin().Owner))
			if !bytes.Equal(rev.Node, pub(node)) {
				t.Fatal("wrong revoked member")
			}
			return response(201, struct{}{}), nil
		case r.Method == "DELETE":
			if r.URL.Path != "/v0/spaces/"+spaceID {
				t.Fatal("wrong erased space")
			}
			return response(204, nil), nil
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			return nil, nil
		}
	})
	p, err := c.Create(clientContext, spaceID, "Office")
	requireOK(t, err)
	if p.Space != spaceID || p.Owner != pin().Owner || p.Base != directory {
		t.Fatalf("wrong created pin: %+v", p)
	}
	inv, err := c.Invite(clientContext, p, time.Hour)
	requireOK(t, err)
	if inv.Owner != p.Owner || inv.Space != spaceID || inv.Base != directory {
		t.Fatal("invitation lost owner pin")
	}
	requireOK(t, c.Approve(clientContext, p, pairbook.JoinRequest{Space: spaceID, Node: pub(node), Name: "Alice"}, time.Hour))
	requireOK(t, c.Publish(clientContext, p, []string{"a.example:7445"}, time.Hour))
	requireOK(t, c.Revoke(clientContext, p, pub(node)))
	requireOK(t, c.Delete(clientContext, p))
	if calls != 6 {
		t.Fatalf("expected six signed operations: %d", calls)
	}
}

func TestClientAcceptanceRequestsDropUnverifiedEntries(t *testing.T) {
	node, other := clientKey("node"), clientKey("other")
	req := pairbook.JoinRequest{Space: spaceID, Node: pub(node), Name: "Alice", At: clientNow.UnixMilli()}
	cross := req
	cross.Space = "fedcba9876543210fedcba9876543210"
	older := req
	older.Name = "Offline request"
	older.At = clientNow.Add(-time.Hour).UnixMilli()
	stale := req
	stale.At = clientNow.Add(-pairbook.InviteValidity - time.Millisecond).UnixMilli()
	records := []pairbook.SignedJoinRequest{pairbook.SignJoinRequest(node, req), pairbook.SignJoinRequest(node, older), pairbook.SignJoinRequest(other, req), pairbook.SignJoinRequest(node, cross), pairbook.SignJoinRequest(node, stale)}
	c := client(t, clientKey("owner"), func(r *http.Request) (*http.Response, error) {
		readRequest(t, r)
		return response(200, map[string]any{"requests": records}), nil
	})
	got, err := c.Requests(clientContext, pin())
	requireOK(t, err)
	if len(got) != 2 || got[0].Name != "Alice" || got[1].Name != "Offline request" {
		t.Fatalf("unverified join requests returned: %+v", got)
	}
}

func TestClientAcceptanceApprovalRejectsRequestFromAnotherSpace(t *testing.T) {
	calls := 0
	c := client(t, clientKey("owner"), func(*http.Request) (*http.Response, error) { calls++; return response(201, struct{}{}), nil })
	request := pairbook.JoinRequest{Space: "fedcba9876543210fedcba9876543210", Node: pub(clientKey("node")), Name: "Alice"}
	requireError(t, c.Approve(clientContext, pin(), request, time.Hour))
	if calls != 0 {
		t.Fatal("cross-space request was countersigned and submitted")
	}
}
