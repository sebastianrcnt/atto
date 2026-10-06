package session

import (
	"time"

	"golang.org/x/sys/unix"
)

func pidStartTime(pid int) (time.Time, bool) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || p.Proc.P_pid != int32(pid) {
		return time.Time{}, false
	}
	start := p.Proc.P_starttime
	return time.Unix(start.Sec, int64(start.Usec)*1000), true
}
