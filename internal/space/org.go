package space

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/pairtask/pairtask/pairbook"
)

// Organization spaces: a company's own Pairbook owns them. A person joins
// by enrolling: their workspace starts an enrollment, sends the person's
// signed join tied to its challenge, and the person confirms in a browser
// after signing in with the company's account.

var ErrOrgLink = errors.New("space: not an organization join link")

// OrgLink is an organization space's join link,
// https://HOST/join#space=ID&owner=KEYID. It holds no secret: it names the
// space and pins its owner, as an invite does.
type OrgLink struct {
	Base  string // https://HOST[:PORT]
	Space string
	Owner string // the space key's ID, sha256:HEX
}

const orgJoinPath = "/join"

// ParseOrgLink reads an organization join link, exactly as String writes it.
func ParseOrgLink(link string) (OrgLink, error) {
	head, fragment, _ := strings.Cut(link, "#")
	spaceField, ownerField, _ := strings.Cut(fragment, "&")
	l := OrgLink{
		Base:  strings.TrimSuffix(head, orgJoinPath),
		Space: strings.TrimPrefix(spaceField, "space="),
		Owner: strings.TrimPrefix(ownerField, "owner="),
	}
	// Canonical: only a link exactly as String writes it, so a missing
	// path, field name or a field out of order never reads as a link.
	if !l.valid() || l.String() != link {
		return OrgLink{}, ErrOrgLink
	}
	return l, nil
}

func (l OrgLink) valid() bool {
	return checkBase(l.Base) == nil && validSpaceID(l.Space) && validKeyID(l.Owner)
}

func (l OrgLink) String() string {
	return l.Base + orgJoinPath + "#space=" + l.Space + "&owner=" + l.Owner
}

// Invite is the link as the pin checks read it: its space, owner and
// directory, and no token.
func (l OrgLink) Invite() Invite { return Invite{Base: l.Base, Space: l.Space, Owner: l.Owner} }

// Enrollment is a started enrollment: its ID, the challenge the join's
// token hash is made from, and the code and URL the person confirms at.
type Enrollment struct {
	ID        string        `json:"enrollment"`
	Challenge []byte        `json:"challenge"`
	Code      string        `json:"code"`
	URL       string        `json:"url"`
	Base      pairbook.Mark `json:"base"`
	ExpiresAt int64         `json:"expires_at"`
}

// Enrollment states, as the Pairbook reports them.
const (
	EnrollPendingSignIn    = "pending_signin"
	EnrollAwaitingApproval = "awaiting_approval"
	EnrollStale            = "stale"
	EnrollAdmitted         = "admitted"
	EnrollDenied           = "denied"
	EnrollExpired          = "expired"
)

// EnrollState is an enrollment's state; Base is the map to re-sign the
// join on when the state is stale.
type EnrollState struct {
	State string         `json:"state"`
	Base  *pairbook.Mark `json:"base,omitempty"`
}

// Enroller is the enrollment calls of an organization's Pairbook.
type Enroller interface {
	StartEnroll(ctx context.Context, pin Pin, name string) (Enrollment, error)
	SubmitEnroll(ctx context.Context, pin Pin, id string, ch pairbook.SignedChange) (EnrollState, error)
	EnrollState(ctx context.Context, pin Pin, id string) (EnrollState, error)
}

var _ Enroller = (*Client)(nil)

// StartEnroll starts enrolling this workspace in pin's space as name.
func (c *Client) StartEnroll(ctx context.Context, pin Pin, name string) (Enrollment, error) {
	var out Enrollment
	body := struct {
		Name string `json:"name"`
	}{name}
	err := c.v1(ctx, pin, request{method: http.MethodPost, path: mapPath(pin, "/enrollments"), body: body}, &out)
	if err == nil && !validSpaceID(out.ID) {
		err = errors.New("space: the Pairbook answered with a malformed enrollment")
	}
	return out, err
}

// SubmitEnroll sends the person's join for enrollment id.
func (c *Client) SubmitEnroll(ctx context.Context, pin Pin, id string, ch pairbook.SignedChange) (EnrollState, error) {
	var out EnrollState
	body := struct {
		Change pairbook.SignedChange `json:"change"`
	}{ch}
	return out, c.v1(ctx, pin, request{method: http.MethodPut, path: enrollPath(pin, id), body: body}, &out)
}

// EnrollState is enrollment id's state now. One the Pairbook itself says
// it doesn't have (its own not_found: purged, or never this workspace's) is
// expired; any other failure is an error, and the enrollment may still be.
func (c *Client) EnrollState(ctx context.Context, pin Pin, id string) (EnrollState, error) {
	var out EnrollState
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: enrollPath(pin, id)}, &out)
	if notFound(err) {
		return EnrollState{State: EnrollExpired}, nil
	}
	return out, err
}

func enrollPath(pin Pin, id string) string { return mapPath(pin, "/enrollments/"+id) }
