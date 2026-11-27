//go:build !linux && !darwin

package server

import (
	"errors"
	"net"
)

// Operator authentication depends on SO_PEERCRED; other platforms fail closed.
func peerUID(net.Conn) (int, error) {
	return 0, errors.New("operator peer credentials are supported only on Linux")
}
