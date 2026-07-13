//go:build linux || darwin

package main

import (
	"syscall"
	"unsafe"
)

// winsize mirrors the C struct winsize filled by the TIOCGWINSZ ioctl. The
// stdlib syscall package exposes the constants but not this struct (it lives in
// golang.org/x/sys/unix), so declare it here to stay dependency-free.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// ttyColumns returns the terminal width (columns) for fd via the TIOCGWINSZ
// ioctl, or 0 if it can't be determined (fd is not a terminal, or the call
// fails). Stdlib-only; kept behind a build tag so the Windows release target
// (which has no such ioctl) uses the fallback in tty_other.go instead.
func ttyColumns(fd uintptr) int {
	ws := &winsize{}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}
