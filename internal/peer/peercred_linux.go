//go:build linux

package peer

import (
	"net"
	"syscall"
)

// peerCredentials reads SO_PEERCRED from a Unix connection: the uid and gid
// the kernel recorded for the connecting process, as seen in this process's
// user namespace. Across isolated container idmaps that may be an unmapped
// id, which is exactly what the probe records.
func peerCredentials(conn net.Conn) (uid, gid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return 0, 0, false
	}
	return int(cred.Uid), int(cred.Gid), true
}
