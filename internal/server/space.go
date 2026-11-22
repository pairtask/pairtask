package server

import (
	"context"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/space"
)

// spaceConnect answers one member's space connect (Pairbook): its request
// frame, then the hub's acceptance or refusal, under the pairing bound.
func (p *PairingServer) spaceConnect(netCtx context.Context, conn *firstcontact.Conn, ep hub.PairingEndpoint) {
	if !take(p.slots) {
		p.log.Warn("space connect refused: too many")
		return
	}
	defer release(p.slots)
	frames := conn.Frames(netCtx)
	frame, err := frames.Recv()
	if err != nil {
		return
	}
	answer := space.ConnectAnswer{Reason: "malformed connect request"}
	if req, err := space.DecodeConnectRequest(frame); err == nil {
		answer = ep.SpaceConnect(netCtx, conn.Connection().PeerLeaf, req)
	}
	if err := frames.Send(answer.Encode()); err != nil {
		p.log.Info("space connect answer not delivered", "cause", hub.PairingCause(err))
	}
}
