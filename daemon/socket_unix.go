//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// On Unix the trust boundary is the owner-only socket directory plus the
// kernel's peer credentials: a connection is accepted only from this user.

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

// listenSocket listens on sock, a path no one else may connect to.
func listenSocket(sock string) (net.Listener, error) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// authConn is the listener's check of an accepted connection.
func authConn(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errPeer
	}
	return checkPeer(uc)
}

// removeSocket deletes a socket this process created or found stale.
func removeSocket(sock string) { _ = os.Remove(sock) }

// staleSocket reports whether a failed dial of a socket file means that no
// process listens there any more.
func staleSocket(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
}

// socketBusy reports whether a process answers on sock.
func socketBusy(sock string) (bool, error) {
	c, err := trustedDial(sock)
	if err == nil {
		c.Close()
		return true, nil
	}
	if errors.Is(err, errPeer) {
		return false, err
	}
	return false, nil
}

// cleanOrphanTokens: Unix sockets have no token files.
func cleanOrphanTokens(string, string) {}

// tempSocketBase is where sockets go when ATTO_DIR is too deep for a
// socket path: short, unlike macOS's per-user temporary directory.
func tempSocketBase() string { return "/tmp" }

// privateTempName names the per-user directory below the temporary
// directory.
func privateTempName() string { return "atto-" + strconv.Itoa(os.Getuid()) }
