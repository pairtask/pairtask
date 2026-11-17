package pairbook_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/pairbook"
)

func headerFixture() (pairbook.HTTPRequest, pairbook.HTTPAuth, string) {
	r := pairbook.HTTPRequest{Method: "POST", Target: "/v0/spaces/abc?name=a%20b", Body: []byte(`{"Name":"Alice"}`), Nonce: [16]byte{1, 2, 3}}
	a := pairbook.SignHTTP(contractKey("node"), r, contractNow)
	enc := base64.RawURLEncoding.EncodeToString
	header := fmt.Sprintf("Pairbook key=%s, at=%d, nonce=%s, sig=%s", enc(a.Key), a.At, enc(a.Nonce[:]), enc(a.Sig))
	return r, a, header
}

func TestHeaderAcceptanceExactFormatAndIndependentParse(t *testing.T) {
	r, a, expected := headerFixture()
	if pairbook.AuthScheme != "Pairbook" {
		t.Fatal("unexpected authorization scheme")
	}
	if got := a.Header(); got != expected {
		t.Fatalf("header format changed: got %q want %q", got, expected)
	}
	r.Nonce = [16]byte{99}
	parsed, err := pairbook.ParseHeader(expected, r)
	good(t, err)
	if !bytes.Equal(parsed.Key, a.Key) || !bytes.Equal(parsed.Sig, a.Sig) || parsed.At != a.At || parsed.Nonce != a.Nonce {
		t.Fatal("header fields changed")
	}
	if parsed.Method != r.Method || parsed.Target != r.Target || parsed.BodyHash != sha256.Sum256(r.Body) {
		t.Fatal("request fields not reconstructed")
	}
	good(t, parsed.Verify(r.Method, r.Target, r.Body, contractNow))
}

func TestHeaderAcceptanceReconstructedFieldsRemainSignatureBound(t *testing.T) {
	original, _, header := headerFixture()
	for name, change := range map[string]func(*pairbook.HTTPRequest){
		"method":         func(r *pairbook.HTTPRequest) { r.Method = "GET" },
		"escaped-target": func(r *pairbook.HTTPRequest) { r.Target = "/v0/spaces/abc?name=a+b" },
		"body":           func(r *pairbook.HTTPRequest) { r.Body = []byte(`{"Name":"Mallory"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			r := original
			change(&r)
			a, err := pairbook.ParseHeader(header, r)
			good(t, err)
			bad(t, a.Verify(r.Method, r.Target, r.Body, contractNow))
		})
	}
}

func TestHeaderAcceptanceRejectsAmbiguousOrMalformedFields(t *testing.T) {
	r, a, header := headerFixture()
	enc := base64.RawURLEncoding.EncodeToString
	key, nonce, sig := enc(a.Key), enc(a.Nonce[:]), enc(a.Sig)
	cases := map[string]string{
		"missing":          "",
		"wrong-scheme":     strings.Replace(header, "Pairbook ", "Bearer ", 1),
		"scheme-case":      strings.Replace(header, "Pairbook ", "pairbook ", 1),
		"trailing-space":   header + " ",
		"newline":          header + "\n",
		"duplicate":        header + ", key=" + key,
		"unknown":          header + ", extra=x",
		"separator":        strings.ReplaceAll(header, ", ", ","),
		"reordered":        fmt.Sprintf("Pairbook at=%d, key=%s, nonce=%s, sig=%s", a.At, key, nonce, sig),
		"padded-key":       strings.Replace(header, "key="+key, "key="+key+"=", 1),
		"padded-nonce":     strings.Replace(header, "nonce="+nonce, "nonce="+nonce+"==", 1),
		"padded-signature": strings.Replace(header, "sig="+sig, "sig="+sig+"==", 1),
		"short-key":        strings.Replace(header, "key="+key, "key="+enc(a.Key[:31]), 1),
		"short-nonce":      strings.Replace(header, "nonce="+nonce, "nonce="+enc(a.Nonce[:15]), 1),
		"short-signature":  strings.Replace(header, "sig="+sig, "sig="+enc(a.Sig[:63]), 1),
		"invalid-base64":   strings.Replace(header, "key="+key, "key=*", 1),
	}
	for _, at := range []string{"-1", "+1", "01", "1.5", "9223372036854775808", "", "now"} {
		cases["timestamp-"+at] = strings.Replace(header, fmt.Sprintf("at=%d", a.At), "at="+at, 1)
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) { _, err := pairbook.ParseHeader(value, r); bad(t, err) })
	}
}
