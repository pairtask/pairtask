package hub

import (
	"context"
	"crypto"
	"net/http"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
)

// SpaceConfig enables Pairbook spaces (operator_space).
type SpaceConfig struct {
	// Directory makes the client for one directory, signing as this
	// workspace. nil = space.NewClient(base, signer, space.Options{HTTP: HTTP, Now: Clock}).
	Directory func(base string, signer crypto.Signer) (space.Directory, error)
	HTTP      *http.Client
	Clock     func() time.Time
}

// SetSpaces enables operator_space; without it, every space operation is
// refused as not configured.
func (h *Hub) SetSpaces(cfg SpaceConfig) { h.spaces = &cfg }

// operatorSpace serves one space operation.
func (h *Hub) operatorSpace(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	return (&routingError{protocol.ErrSpace, "spaces not implemented"}).frame(req.msg.MessageID)
}
