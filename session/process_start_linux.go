package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func pidStartTime(pid int) (time.Time, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, false
	}
	boot, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	return linuxStartTime(string(stat), string(boot))
}

func linuxStartTime(stat, boot string) (time.Time, bool) {
	// comm can contain spaces and parentheses; fields after it start at field 3.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) <= 19 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return time.Time{}, false
	}
	for line := range strings.SplitSeq(boot, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		// Linux exposes proc clock ticks at USER_HZ (100), not the kernel's HZ.
		return time.Unix(seconds+ticks/100, ticks%100*int64(time.Second)/100), true
	}
	return time.Time{}, false
}
