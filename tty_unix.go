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

// winsizeIoctl runs TIOCGWINSZ on fd, returning the reported size and whether
// the call succeeded (i.e. fd is a terminal).
func winsizeIoctl(fd uintptr) (winsize, bool) {
	ws := &winsize{}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	return *ws, errno == 0
}

// ttyColumns returns the terminal width (columns) for fd, or 0 if it can't be
// determined (fd is not a terminal, or the terminal reports no width). Stdlib-
// only; kept behind a build tag so the Windows release target (which has no such
// ioctl) uses the fallback in tty_other.go instead.
func ttyColumns(fd uintptr) int {
	ws, ok := winsizeIoctl(fd)
	if !ok {
		return 0
	}
	return int(ws.Col)
}

// isTerminal reports whether fd refers to a terminal. Unlike ttyColumns it does
// not care about the reported width, so a terminal with an unset window size
// (e.g. some CI ptys) is still detected — the right primitive for deciding
// whether to emit color.
func isTerminal(fd uintptr) bool {
	_, ok := winsizeIoctl(fd)
	return ok
}
