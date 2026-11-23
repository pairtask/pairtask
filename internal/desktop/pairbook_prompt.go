package desktop

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// startPairbook schedules startup work while Start still holds the node lock.
func (n *Node) startPairbook() {
	if n.cfg.Pairbook != "" && pairbookOrigin(n.cfg.Pairbook) == "" {
		n.cfg.Log.Warn("the Pairbook setting isn't an https://HOST address; ignored", "setting", n.cfg.Pairbook)
	}
	if company := pairbookOrigin(n.cfg.Pairbook); company != "" && n.cfg.Notify != nil {
		n.spawn(func() { n.promptPairbook(company) })
	}
	if n.own != nil {
		go n.resumeJoins() // runs once Start has returned and released mu
	}
}

// promptPairbook is local onboarding. The user chooses whether to start enrollment.
func (n *Node) promptPairbook(company origin) {
	ctx, cancel := context.WithTimeout(n.joins.life, 5*time.Second)
	defer cancel()
	spaces, err := n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList})
	if err != nil {
		return
	}
	for _, pin := range spaces.Pins {
		if pairbookOrigin(pin.Directory) == company {
			return
		}
	}
	if err := n.notifyPairbook(ctx, company); err != nil {
		n.cfg.Log.Warn("could not show Pairbook connection prompt", "error", err)
	}
}

func (n *Node) notifyPairbook(ctx context.Context, company origin) error {
	// Desktop may open several servers. Reserve one notice for this origin across processes and restarts.
	path := filepath.Join(n.cfg.Dir, fmt.Sprintf("pairbook-prompt-%x", sha256.Sum256([]byte(company))))
	marker, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	marker.Close()
	err = n.cfg.Notify(ctx, "Connect to your company Pairbook", "Open "+string(company)+"/join to choose a space, or paste a company join link into Claude.")
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
