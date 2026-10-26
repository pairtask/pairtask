//go:build !linux

package peer

import "net"

func peerCredentials(net.Conn) (uid, gid int, ok bool) { return 0, 0, false }
