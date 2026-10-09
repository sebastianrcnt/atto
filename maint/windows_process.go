//go:build windows

package maint

import (
	"golang.org/x/sys/windows"
	"syscall"
	"unsafe"
)

func unsafeSizeProcessEntry() uintptr { return unsafe.Sizeof(windows.ProcessEntry32{}) }

var windowsSysProcAttr = syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW}
