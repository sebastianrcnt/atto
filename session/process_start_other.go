//go:build !darwin && !linux && !windows

package session

import "time"

func pidStartTime(int) (time.Time, bool) { return time.Time{}, false }
