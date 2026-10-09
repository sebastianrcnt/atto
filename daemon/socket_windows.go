//go:build windows

package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sebastianrcnt/atto/fsutil"
)

// Windows has AF_UNIX sockets (Windows 10 1803+) but no peer-credential
// query for them, so a connection is trusted by two independent checks:
//
//   - the directory holding the socket is private: owned by this user (or
//     the administrators), with a protected DACL that admits only this
//     user, so no other account can reach the socket file or read the
//     token file beside it (privateDir);
//   - the connecting side proves it can read that token: a random 32-byte
//     secret in "<socket>.tok", written before the socket exists and sent
//     as the first bytes of every connection. A listener drops connections
//     that do not start with it (tokenListener).
//
// A process of the same user can read the token; as on Unix, processes of
// the same user are trusted.

var errPeer = errors.New("daemon: untrusted peer")

// tokenLen is the size of a socket's secret, in bytes.
const tokenLen = 32

// tokenWait bounds how long a connection may take to present its token.
const tokenWait = 5 * time.Second

func tokenPath(sock string) string { return sock + ".tok" }

func writeToken(sock string) ([]byte, error) {
	b := make([]byte, tokenLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := fsutil.WriteAtomic(tokenPath(sock), b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func readToken(sock string) ([]byte, error) {
	b, err := os.ReadFile(tokenPath(sock))
	if err != nil {
		return nil, err
	}
	if len(b) != tokenLen {
		return nil, fmt.Errorf("socket token has %d bytes, want %d", len(b), tokenLen)
	}
	return b, nil
}

// trustedDial connects to sock and presents its token. A socket with no
// readable token is not one of ours.
func trustedDial(sock string) (net.Conn, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	// Read the token after connecting: the listener at the other end
	// wrote its own before it created the socket.
	tok, err := readToken(sock)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("%w: %v", errPeer, err)
	}
	_ = c.SetWriteDeadline(time.Now().Add(tokenWait))
	_, err = c.Write(tok)
	_ = c.SetWriteDeadline(time.Time{})
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// tokenListener accepts connections that present the socket's token.
type tokenListener struct {
	net.Listener
	sock  string
	token []byte
}

func (l *tokenListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tokenConn{Conn: c, token: l.token}, nil
}

func (l *tokenListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(tokenPath(l.sock))
	return err
}

// tokenConn is an accepted connection that must first send the token.
// Reads and writes check it themselves if authConn has not.
type tokenConn struct {
	net.Conn
	token []byte
	once  sync.Once
	err   error
}

func (c *tokenConn) verify() error {
	c.once.Do(func() {
		_ = c.Conn.SetReadDeadline(time.Now().Add(tokenWait))
		got := make([]byte, tokenLen)
		_, err := io.ReadFull(c.Conn, got)
		_ = c.Conn.SetReadDeadline(time.Time{})
		if err != nil || subtle.ConstantTimeCompare(got, c.token) != 1 {
			c.err = fmt.Errorf("%w: bad socket token", errPeer)
			c.Conn.Close()
		}
	})
	return c.err
}

func (c *tokenConn) Read(b []byte) (int, error) {
	if err := c.verify(); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *tokenConn) Write(b []byte) (int, error) {
	if err := c.verify(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

// listenSocket listens on sock, in a private directory, with a fresh token.
func listenSocket(sock string) (net.Listener, error) {
	tok, err := writeToken(sock)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.Remove(tokenPath(sock))
		return nil, err
	}
	return &tokenListener{Listener: ln, sock: sock, token: tok}, nil
}

// authConn is the listener's check of an accepted connection: it must
// start with the socket's token.
func authConn(c net.Conn) error {
	tc, ok := c.(*tokenConn)
	if !ok {
		return errPeer
	}
	return tc.verify()
}

// removeSocket deletes a socket, and its token, that this process created
// or found stale.
func removeSocket(sock string) {
	_ = os.Remove(sock)
	_ = os.Remove(tokenPath(sock))
}

// staleSocket reports whether a failed dial of a socket file means that no
// process listens there any more. The file of a dead listener refuses
// connections; anything but an authentication failure counts, since a
// socket file of ours that cannot be reached is useless.
func staleSocket(err error) bool { return err != nil && !errors.Is(err, errPeer) }

// socketBusy reports whether a process answers on sock, whoever it is.
func socketBusy(sock string) (bool, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return false, nil
	}
	c.Close()
	return true, nil
}

// cleanOrphanTokens removes the tokens beside sockets that no longer exist.
func cleanOrphanTokens(dir, prefix string) {
	toks, _ := filepath.Glob(filepath.Join(dir, prefix+"w-*.sock.tok"))
	for _, t := range toks {
		if _, err := os.Lstat(strings.TrimSuffix(t, ".tok")); errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(t)
		}
	}
}

// tempSocketBase is where sockets go when ATTO_DIR is too deep for a
// socket path. The temporary directory is already per-user and short.
func tempSocketBase() string { return os.TempDir() }

// privateTempName names the directory below the temporary directory.
func privateTempName() string { return "atto" }

// privateDir creates path if need be and makes sure only this user can use
// it: it must be a real directory (not a link) owned by this user, the
// administrators or the system, with a DACL that allows no one else. A
// directory of a trusted owner with a looser DACL (one under a shared
// parent, say) is tightened to a protected DACL for this user alone; one
// owned by anyone else is refused.
func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || isReparsePoint(info) {
		return fmt.Errorf("daemon: untrusted socket directory %q (not a plain directory)", path)
	}
	tr, err := loadTrust()
	if err != nil {
		return err
	}
	trusted, err := dirTrusted(path, tr)
	if err != nil {
		return err
	}
	if trusted {
		return nil
	}
	owner, err := dirOwner(path)
	if err != nil {
		return err
	}
	if !tr.owner(owner) {
		return fmt.Errorf("daemon: untrusted socket directory %q (owned by %s, not this user)", path, owner)
	}
	if err := restrictDir(path, tr.user); err != nil {
		return fmt.Errorf("daemon: securing socket directory %q: %w", path, err)
	}
	if ok, err := dirTrusted(path, tr); err != nil || !ok {
		return fmt.Errorf("daemon: untrusted socket directory %q (its access list admits others)", path)
	}
	return nil
}

// isReparsePoint reports whether info is a symbolic link, junction or other
// reparse point.
func isReparsePoint(info os.FileInfo) bool {
	attr, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || attr.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.Mode()&os.ModeSymlink != 0
}

// trust is the set of principals a private directory may name.
type trust struct {
	user, system, admins, creator *windows.SID
}

func loadTrust() (trust, error) {
	tok, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return trust{}, err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return trust{}, err
	}
	var t trust
	if t.user, err = u.User.Sid.Copy(); err != nil {
		return trust{}, err
	}
	if t.system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid); err != nil {
		return trust{}, err
	}
	if t.admins, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err != nil {
		return trust{}, err
	}
	if t.creator, err = windows.CreateWellKnownSid(windows.WinCreatorOwnerSid); err != nil {
		return trust{}, err
	}
	return t, nil
}

// owner reports whether sid may own a private directory. An elevated
// process creates directories owned by the administrators group.
func (t trust) owner(sid *windows.SID) bool {
	return sid != nil && (sid.Equals(t.user) || sid.Equals(t.admins) || sid.Equals(t.system))
}

// principal reports whether sid may appear in a private directory's
// allow entries.
func (t trust) principal(sid *windows.SID) bool {
	return t.owner(sid) || sid.Equals(t.creator)
}

func dirOwner(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	return owner, nil
}

// dirTrusted reports whether path is owned by a trusted principal and its
// DACL allows only trusted principals.
func dirTrusted(path string, t trust) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	if !t.owner(owner) {
		return false, nil
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false, nil // no DACL, or a null one: everyone has access
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if !t.principal((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return false, nil
		}
	}
	return true, nil
}

// restrictDir gives path a protected DACL (nothing inherited from the
// parent) that grants full control to user alone, inherited by what the
// directory will contain.
func restrictDir(path string, user *windows.SID) error {
	var pin runtime.Pinner
	pin.Pin(user)
	defer pin.Unpin()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
