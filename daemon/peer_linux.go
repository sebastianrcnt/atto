package daemon

import (
	"net"

	"golang.org/x/sys/unix"
)

func checkPeer(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var uid uint32
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		peerErr = err
		if err == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	return peerUID(uid)
}
