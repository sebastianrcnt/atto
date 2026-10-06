//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("daemon: untrusted socket directory %q (must be owned by this user and mode 0700)", path)
	}
	return nil
}

var errPeer = errors.New("daemon: untrusted peer")

func peerUID(uid uint32) error {
	if uid != uint32(os.Getuid()) {
		return fmt.Errorf("%w: uid %d", errPeer, uid)
	}
	return nil
}

func trustedDial(sock string) (net.Conn, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := checkPeer(c.(*net.UnixConn)); err != nil {
		c.Close()
		return nil, fmt.Errorf("%w: %v", errPeer, err)
	}
	return c, nil
}
