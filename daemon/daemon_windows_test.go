//go:build windows

package daemon

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sebastianrcnt/atto/config"
	"github.com/sebastianrcnt/atto/shell"
)

// A connection is served only when it starts with the socket's token.
func TestSocketTokenIsRequired(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "t.sock")
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := os.Stat(tokenPath(sock)); err != nil {
		t.Fatalf("no token beside the socket: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if authConn(c) == nil {
					_, _ = io.WriteString(c, "welcome")
				}
			}()
		}
	}()
	read := func(c net.Conn) string {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		b, _ := io.ReadAll(c)
		return string(b)
	}

	good, err := trustedDial(sock)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(good); got != "welcome" {
		t.Fatalf("a client with the token got %q", got)
	}
	good.Close()

	wrong, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = wrong.Write(make([]byte, tokenLen))
	if got := read(wrong); got != "" {
		t.Fatalf("a client with the wrong token got %q", got)
	}
	wrong.Close()

	none, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	none.Close()

	// With the token gone, nothing is trusted: the socket is not ours.
	if err := os.Remove(tokenPath(sock)); err != nil {
		t.Fatal(err)
	}
	if c, err := trustedDial(sock); err == nil {
		c.Close()
		t.Fatal("dialed a socket with no token")
	}
}

// A socket's token goes with it, and one left behind by a crash is swept.
func TestSocketTokenRemovalAndSweep(t *testing.T) {
	t.Setenv(config.EnvDir, t.TempDir())
	sock, err := workerSocket()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if _, err := os.Stat(tokenPath(sock)); !os.IsNotExist(err) {
		t.Fatalf("a closed listener left its token: %v", err)
	}
	orphan := tokenPath(sock)
	if err := os.WriteFile(orphan, make([]byte, tokenLen), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanWorkerSockets()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("an orphan token was kept: %v", err)
	}
}

func userTrustee(t *testing.T, sid *windows.SID, mask windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: mask,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

// A directory of ours that others may use is tightened to this user alone.
func TestPrivateDirTightensLooseACL(t *testing.T) {
	tr, err := loadTrust()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var pin runtime.Pinner
	pin.Pin(tr.user)
	pin.Pin(everyone)
	defer pin.Unpin()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		userTrustee(t, tr.user, windows.GENERIC_ALL),
		userTrustee(t, everyone, windows.GENERIC_READ),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := dirTrusted(dir, tr); err != nil || ok {
		t.Fatalf("a directory open to everyone was trusted: %v %v", ok, err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	if ok, err := dirTrusted(dir, tr); err != nil || !ok {
		t.Fatalf("the directory was not tightened: %v %v", ok, err)
	}
	// What is made in it is private too.
	f := filepath.Join(dir, "x.tok")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := dirTrusted(f, tr); err != nil || !ok {
		t.Fatalf("a file in the directory is open to others: %v %v", ok, err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
}

// A directory that is a link elsewhere is not a place for sockets.
func TestPrivateDirRejectsJunction(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, t.TempDir()).CombinedOutput(); err != nil {
		t.Skipf("cannot make a junction: %v %s", err, out)
	}
	if err := privateDir(link); err == nil {
		t.Fatal("accepted a junction")
	}
}

// The daemon and its workers run on a hidden console of their own: it
// outlives the terminal that started them, and what they run has a console
// to inherit without a window appearing.
func TestDetachedProcessesHaveAHiddenConsole(t *testing.T) {
	cmd := exec.Command("atto", "_daemon")
	shell.Isolate(cmd)
	flags := cmd.SysProcAttr.CreationFlags
	if flags&windows.CREATE_NO_WINDOW == 0 || flags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("creation flags %#x", flags)
	}
	if flags&windows.DETACHED_PROCESS != 0 {
		t.Fatal("a detached process has no console for its children to share")
	}
}
