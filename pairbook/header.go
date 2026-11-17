package pairbook

// AuthScheme is the Authorization scheme of a signed request:
//
//	Authorization: Pairbook key=<k>, at=<ms>, nonce=<n>, sig=<s>
//
// k, n and s are unpadded base64url (32, 16 and 64 bytes) and at is decimal
// Unix milliseconds, in exactly this order and form.
const AuthScheme = "Pairbook"

// Header is the auth as an Authorization header value.
func (a HTTPAuth) Header() string { return "" }

// ParseHeader reads an Authorization header value strictly and returns the
// auth with its Method, Target and BodyHash taken from r. r.Nonce is ignored:
// the header carries the nonce. The result still needs Verify.
func ParseHeader(value string, r HTTPRequest) (HTTPAuth, error) {
	return HTTPAuth{}, ErrNotImplemented
}
