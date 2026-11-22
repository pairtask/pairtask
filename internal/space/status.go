package space

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Status in a v1 space: this workspace's heartbeat, the agents it lists in
// the space, and who is online. The list advertises agents; who may
// message them is still each workspace's own sharing.

// ErrNoStatus: the Pairbook has no status calls (an older one). Membership
// still comes from the signed map; only status is unknown.
var ErrNoStatus = errors.New("space: this Pairbook doesn't keep status")

// Agent is one agent a workspace lists in a space.
type Agent struct {
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// WorkspaceStatus is one workspace's status as the Pairbook reports it.
type WorkspaceStatus struct {
	Workspace string    `json:"workspace"`
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	Agents    []Agent   `json:"agents,omitempty"`
}

// PersonStatus is one person and the workspaces of theirs shown to this
// workspace.
type PersonStatus struct {
	Person     string            `json:"person"`
	Name       string            `json:"name"`
	Role       string            `json:"role"`
	Workspaces []WorkspaceStatus `json:"workspaces"`
}

// Statuser is the status calls of a v1 Pairbook.
type Statuser interface {
	Heartbeat(ctx context.Context, pin Pin) error
	PublishAgents(ctx context.Context, pin Pin, agents []Agent) error
	Status(ctx context.Context, pin Pin) ([]PersonStatus, error)
}

var _ Statuser = (*Client)(nil)

// Heartbeat tells pin's Pairbook this workspace is online.
func (c *Client) Heartbeat(ctx context.Context, pin Pin) error {
	return statusErr(c.v1(ctx, pin, request{method: http.MethodPut, path: mapPath(pin, "/presence")}, nil))
}

// PublishAgents replaces the agents this workspace lists in pin's space.
func (c *Client) PublishAgents(ctx context.Context, pin Pin, agents []Agent) error {
	if agents == nil {
		agents = []Agent{}
	}
	body := struct {
		Agents []Agent `json:"agents"`
	}{agents}
	return statusErr(c.v1(ctx, pin, request{method: http.MethodPut, path: mapPath(pin, "/agents"), body: body}, nil))
}

// Status is the people this workspace's map lets it see, with status.
func (c *Client) Status(ctx context.Context, pin Pin) ([]PersonStatus, error) {
	var out []PersonStatus
	err := c.v1(ctx, pin, request{method: http.MethodGet, path: mapPath(pin, "/status")}, &out)
	return out, statusErr(err)
}

// statusErr reads a Pairbook without the status calls, which answers a
// route it doesn't know with a bare 404 or 405, as ErrNoStatus.
func statusErr(err error) error {
	var refused *Refusal
	if !errors.As(err, &refused) || refused.Code == "not_found" {
		return err
	}
	switch refused.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return ErrNoStatus
	}
	return err
}
