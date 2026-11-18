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
			return ownerCreateResponse(t, r, data)
		case strings.HasSuffix(r.URL.Path, "/invites"):
			return ownerInviteResponse(t, r, data)
		case strings.HasSuffix(r.URL.Path, "/members"):
			return ownerApproveResponse(t, r, data)
		case strings.Contains(r.URL.Path, "/addresses/"):
			return ownerPublishResponse(t, r, data)
		case strings.HasSuffix(r.URL.Path, "/revocations"):
			return ownerRevokeResponse(t, r, data)
		case r.Method == "DELETE":
			return ownerDeleteResponse(t, r, data)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
			return nil, nil
		}
	})
	p, err := c.Create(clientContext, spaceID, "Office")
	requireOK(t, err)
	clientCheck(t, p.Space == spaceID, "wrong created pin: %+v", p)
	clientCheck(t, p.Owner == pin().Owner, "wrong created pin: %+v", p)
	clientCheck(t, p.Base == directory, "wrong created pin: %+v", p)
	inv, err := c.Invite(clientContext, p, time.Hour)
	requireOK(t, err)
	clientCheck(t, inv.Owner == p.Owner, "invitation lost owner pin")
	clientCheck(t, inv.Space == spaceID, "invitation lost owner pin")
	clientCheck(t, inv.Base == directory, "invitation lost owner pin")
	requireOK(t, c.Approve(clientContext, p, pairbook.JoinRequest{Space: spaceID, Node: pub(node), Name: "Alice"}, time.Hour))
	requireOK(t, c.Publish(clientContext, p, []string{"a.example:7445"}, time.Hour))
	requireOK(t, c.Revoke(clientContext, p, pub(node)))
	requireOK(t, c.Delete(clientContext, p))
	clientCheck(t, calls == 6, "expected six signed operations: %d", calls)
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
	clientCheck(t, len(got) == 2, "unverified join requests returned: %+v", got)
	clientCheck(t, got[0].Name == "Alice", "unverified join requests returned: %+v", got)
	clientCheck(t, got[1].Name == "Offline request", "unverified join requests returned: %+v", got)
}

func TestClientAcceptanceApprovalRejectsRequestFromAnotherSpace(t *testing.T) {
	calls := 0
	c := client(t, clientKey("owner"), func(*http.Request) (*http.Response, error) { calls++; return response(201, struct{}{}), nil })
	request := pairbook.JoinRequest{Space: "fedcba9876543210fedcba9876543210", Node: pub(clientKey("node")), Name: "Alice"}
	requireError(t, c.Approve(clientContext, pin(), request, time.Hour))
	clientCheck(t, calls == 0, "cross-space request was countersigned and submitted")
}

func clientCheck(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func ownerCreateResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	m := decode[pairbook.SignedMember](t, data)
	requireOK(t, m.Verify(spaceID, pin().Owner, clientNow))
	clientCheck(t, m.Role == pairbook.RoleOwner, "invalid owner creation")
	clientCheck(t, m.Name == "Office", "invalid owner creation")
	clientCheck(t, bytes.Equal(m.Node, pub(clientKey("owner"))), "invalid owner creation")
	return response(201, struct{}{}), nil
}

func ownerInviteResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	inv := decode[pairbook.SignedInvite](t, data)
	requireOK(t, inv.Verify(spaceID, pin().Owner, clientNow))
	clientCheck(t, inv.ExpiresAt == clientNow.Add(time.Hour).UnixMilli(), "wrong invitation expiry")
	return response(201, struct{}{}), nil
}

func ownerApproveResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	m := decode[pairbook.SignedMember](t, data)
	requireOK(t, m.Verify(spaceID, pin().Owner, clientNow))
	clientCheck(t, bytes.Equal(m.Node, pub(clientKey("node"))), "wrong admission")
	clientCheck(t, m.Name == "Alice", "wrong admission")
	clientCheck(t, m.Role == pairbook.RoleMember, "wrong admission")
	return response(201, struct{}{}), nil
}

func ownerPublishResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	a := decode[pairbook.SignedAddress](t, data)
	requireOK(t, a.Verify(spaceID, clientNow))
	clientCheck(t, r.Method == "PUT", "wrong address publisher")
	clientCheck(t, bytes.Equal(a.Node, pub(clientKey("owner"))), "wrong address publisher")
	clientCheck(t, strings.HasSuffix(r.URL.Path, pin().Owner), "wrong address publisher")
	return response(204, nil), nil
}

func ownerRevokeResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	rev := decode[pairbook.SignedRevocation](t, data)
	requireOK(t, rev.Verify(spaceID, pin().Owner))
	clientCheck(t, bytes.Equal(rev.Node, pub(clientKey("node"))), "wrong revoked member")
	return response(201, struct{}{}), nil
}

func ownerDeleteResponse(t *testing.T, r *http.Request, data []byte) (*http.Response, error) {
	t.Helper()
	clientCheck(t, r.URL.Path == "/v0/spaces/"+spaceID, "wrong erased space")
	return response(204, nil), nil
}
