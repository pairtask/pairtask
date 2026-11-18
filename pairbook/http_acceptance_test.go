package pairbook_test

import (
	"bytes"
	"crypto/ed25519"
	"github.com/pairtask/pairtask/pairbook"
	"math"
	"testing"
	"time"
)

func TestHTTPAcceptanceBindsExactRequestAndFreshness(t *testing.T) {
	key := contractKey("node")
	body := []byte(`{"space":"s"}`)
	target := "/v1/spaces/s?a=1&b=2"
	a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "POST", Target: target, Body: body, Nonce: [16]byte{1}}, contractNow)
	good(t, a.Verify("POST", target, body, contractNow))
	// Stateless verification intentionally does not implement the service nonce store.
	good(t, a.Verify("POST", target, body, contractNow))
	bad(t, a.Verify("GET", target, body, contractNow))
	bad(t, a.Verify("POST", "/v1/spaces/t?a=1&b=2", body, contractNow))
	bad(t, a.Verify("POST", "/v1/spaces/s?b=2&a=1", body, contractNow))
	bad(t, a.Verify("POST", target, []byte(`{"space":"t"}`), contractNow))
	for _, delta := range []time.Duration{-pairbook.RequestSkew, pairbook.RequestSkew} {
		good(t, a.Verify("POST", target, body, contractNow.Add(delta)))
	}
	for _, delta := range []time.Duration{-pairbook.RequestSkew - time.Millisecond, pairbook.RequestSkew + time.Millisecond} {
		bad(t, a.Verify("POST", target, body, contractNow.Add(delta)))
	}
	for name, mutate := range map[string]func(*pairbook.HTTPAuth){
		"nonce":     func(a *pairbook.HTTPAuth) { a.Nonce[0]++ },
		"timestamp": func(a *pairbook.HTTPAuth) { a.At++ },
		"key":       func(a *pairbook.HTTPAuth) { a.Key = public(contractKey("other")) },
	} {
		t.Run(name, func(t *testing.T) { c := a; mutate(&c); bad(t, c.Verify("POST", target, body, contractNow)) })
	}
	for _, n := range []int{0, 1, 31, 33, 64} {
		c := a
		c.Key = bytes.Repeat([]byte{1}, n)
		bad(t, c.Verify("POST", target, body, contractNow))
	}
	for _, n := range []int{0, 1, 63, 65} {
		c := a
		c.Sig = make([]byte, n)
		bad(t, c.Verify("POST", target, body, contractNow))
	}
}

func TestHTTPAcceptanceCanonicalEscapedTarget(t *testing.T) {
	key := contractKey("node")
	for _, target := range []string{"relative", "https://host/v1", "//host/v1", "/v1//members", "/v1/./members", "/v1/../members", "/v1/%2e%2e/members", "/v1/.%2E/members", "/v1/%2f/members", "/v1/%zz", "/v1/members#fragment"} {
		t.Run(target, func(t *testing.T) {
			a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: target, Nonce: [16]byte{1}}, contractNow)
			bad(t, a.Verify("GET", target, nil, contractNow))
		})
	}
	target := "/v1/spaces/hello%20world?filter=a%2Fb"
	a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: target, Nonce: [16]byte{1}}, contractNow)
	good(t, a.Verify("GET", target, nil, contractNow))
	bad(t, a.Verify("GET", "/v1/spaces/hello%20world?filter=a/b", nil, contractNow))
}

func TestHTTPAcceptanceReplayIdentityAndRetention(t *testing.T) {
	key := contractKey("node")
	nonce := [16]byte{1}
	a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: nonce}, contractNow)
	b := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "POST", Target: "/v1/b", Body: []byte("changed"), Nonce: nonce}, contractNow.Add(time.Minute))
	if a.ReplayKey() == "" || a.ReplayKey() != b.ReplayKey() {
		t.Fatal("nonce reuse must share identity regardless of signed payload")
	}
	for _, c := range []pairbook.HTTPAuth{
		pairbook.SignHTTP(contractKey("other"), pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: nonce}, contractNow),
		pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: [16]byte{2}}, contractNow),
	} {
		if a.ReplayKey() == c.ReplayKey() {
			t.Fatal("distinct signer or nonce collided")
		}
	}
	future := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: nonce}, contractNow.Add(pairbook.RequestSkew))
	good(t, future.Verify("GET", "/v1/a", nil, contractNow))
	if !future.RetainUntil().Equal(contractNow.Add(2 * pairbook.RequestSkew)) {
		t.Fatal("future-dated nonce retention truncated")
	}
	good(t, future.Verify("GET", "/v1/a", nil, future.RetainUntil()))
	bad(t, future.Verify("GET", "/v1/a", nil, future.RetainUntil().Add(time.Millisecond)))
}

func TestFreshnessAcceptanceRejectsSignedNegativeAndOverflowTimes(t *testing.T) {
	key := contractKey("node")
	for _, at := range []int64{-1, math.MinInt64, math.MaxInt64} {
		a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: [16]byte{1}}, time.UnixMilli(at))
		bad(t, a.Verify("GET", "/v1/a", nil, contractNow))
		j := pairbook.SignJoinRequest(key, pairbook.JoinRequest{Space: "s", Node: public(key), Name: "Node", At: at})
		bad(t, j.Verify("s", contractNow))
	}
	a := pairbook.SignHTTP(key, pairbook.HTTPRequest{Method: "GET", Target: "/v1/a", Nonce: [16]byte{1}}, time.UnixMilli(0))
	bad(t, a.Verify("GET", "/v1/a", nil, time.UnixMilli(math.MinInt64)))
	j := pairbook.SignJoinRequest(key, pairbook.JoinRequest{Space: "s", Node: public(key), Name: "Node", At: 0})
	bad(t, j.Verify("s", time.UnixMilli(math.MinInt64)))
	// A valid signature must still reject malformed semantic fields.
	a.Key = public(key)
	a.At = -1
	a.Sig = ed25519.Sign(key, a.SigningBytes())
	bad(t, a.Verify("GET", "/v1/a", nil, contractNow))
}
