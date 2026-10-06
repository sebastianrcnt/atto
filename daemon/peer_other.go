//go:build !windows && !linux && !darwin && !freebsd

package daemon

import "net"

// The private directory is the trust boundary on platforms without a
// supported peer-credential API.
func checkPeer(*net.UnixConn) error { return nil }
