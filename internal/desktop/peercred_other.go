//go:build !darwin && !linux

package desktop

import (
	"errors"
	"net"
)

// peerUID can't tell who is on the other end here, so sharing fails
// closed: every connection is refused.
func peerUID(net.Conn) (int, error) {
	return 0, errors.New("local peer credentials are supported only on macOS and Linux")
}
