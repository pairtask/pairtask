package space_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
	"net/http"
	"testing"
)

func TestReviewV1JoinWire(t *testing.T) {
	token := sha256.Sum256([]byte("fixture"))
	ch := pairbook.SignedChange{Change: pairbook.Change{Action: pairbook.ChangeJoin, TokenHash: sha256.Sum256(token[:])}}
	calls := 0
	c := client(t, clientKey("workspace"), func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/spaces/"+spaceID+"/changes" {
			t.Fatal(r.Method, r.URL.Path)
		}
		payload := decode[map[string]json.RawMessage](t, readRequest(t, r))
		if decode[string](t, payload["token"]) != base64.RawURLEncoding.EncodeToString(token[:]) {
			t.Fatal("token encoding")
		}
		return response(202, nil), nil
	})
	wrong := [32]byte{}
	for _, bad := range []*[32]byte{nil, &wrong} {
		if err := c.SubmitChange(clientContext, pin(), ch, bad); !errors.Is(err, space.ErrChangeToken) {
			t.Fatal(err)
		}
	}
	requireOK(t, c.SubmitChange(clientContext, pin(), ch, &token))
	ch.Action = pairbook.ChangeLeave
	if err := c.SubmitChange(clientContext, pin(), ch, &token); !errors.Is(err, space.ErrChangeToken) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestReviewV1MapWire(t *testing.T) {
	c := client(t, clientKey("workspace"), func(r *http.Request) (*http.Response, error) {
		if r.Method != "PUT" || r.URL.Path != "/v1/spaces/"+spaceID+"/map" {
			t.Fatal(r.Method, r.URL.Path)
		}
		payload := decode[map[string]json.RawMessage](t, readRequest(t, r))
		if string(payload["applied"]) != "[]" || payload["base"] == nil || payload["map"] == nil {
			t.Fatal(payload)
		}
		return response(409, map[string]string{"code": "conflict"}), nil
	})
	var refused *space.Refusal
	if err := c.PutMap(clientContext, pin(), space.MapPut{}); !errors.As(err, &refused) || refused.Status != 409 {
		t.Fatal(err)
	}
}
