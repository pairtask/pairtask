package pairbook

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// AuthScheme is the Authorization scheme of a signed request:
//
//	Authorization: Pairbook key=<k>, at=<ms>, nonce=<n>, sig=<s>
//
// k, n and s are unpadded base64url (32, 16 and 64 bytes) and at is decimal
// Unix milliseconds, in exactly this order and form.
const AuthScheme = "Pairbook"

// headerFields are the header's field names, in their only order.
var headerFields = [...]string{"key", "at", "nonce", "sig"}

var b64 = base64.RawURLEncoding.Strict()

// Header is the auth as an Authorization header value.
func (a HTTPAuth) Header() string {
	return fmt.Sprintf("%s key=%s, at=%d, nonce=%s, sig=%s", AuthScheme, b64.EncodeToString(a.Key), a.At, b64.EncodeToString(a.Nonce[:]), b64.EncodeToString(a.Sig))
}

// ParseHeader reads an Authorization header value strictly and returns the
// auth with its Method, Target and BodyHash taken from r. r.Nonce is ignored:
// the header carries the nonce. The result still needs Verify.
//
// Strict means the value is exactly what Header writes for the fields it
// holds: one canonical form, so no two headers carry the same auth.
func ParseHeader(value string, r HTTPRequest) (HTTPAuth, error) {
	values, ok := splitHeader(value)
	if !ok {
		return HTTPAuth{}, ErrInvalid
	}
	a, ok := decodeHeader(values)
	if !ok {
		return HTTPAuth{}, ErrInvalid
	}
	a.Method, a.Target, a.BodyHash = r.Method, r.Target, sha256.Sum256(r.Body)
	if a.Header() != value {
		return HTTPAuth{}, ErrInvalid
	}
	return a, nil
}

// splitHeader is the header's field values, in headerFields order.
func splitHeader(value string) ([len(headerFields)]string, bool) {
	var values [len(headerFields)]string
	rest, ok := strings.CutPrefix(value, AuthScheme+" ")
	parts := strings.Split(rest, ", ")
	if !ok || len(parts) != len(headerFields) {
		return values, false
	}
	for i, name := range headerFields {
		values[i], ok = strings.CutPrefix(parts[i], name+"=")
		if !ok {
			return values, false
		}
	}
	return values, true
}

func decodeHeader(values [len(headerFields)]string) (HTTPAuth, bool) {
	var a HTTPAuth
	key, keyOK := decodeSized(values[0], ed25519.PublicKeySize)
	at, atErr := strconv.ParseInt(values[1], 10, 64)
	nonce, nonceOK := decodeSized(values[2], len(a.Nonce))
	sig, sigOK := decodeSized(values[3], ed25519.SignatureSize)
	a.Key, a.At, a.Sig = key, at, sig
	copy(a.Nonce[:], nonce)
	return a, keyOK && atErr == nil && at >= 0 && nonceOK && sigOK
}

// decodeSized decodes unpadded base64url of exactly size bytes.
func decodeSized(s string, size int) ([]byte, bool) {
	b, err := b64.DecodeString(s)
	return b, err == nil && len(b) == size
}
