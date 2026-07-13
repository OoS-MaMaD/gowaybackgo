//go:build !linux && !darwin

package main

// ttyColumns has no portable terminal-size ioctl off linux/darwin, so callers
// fall back to the $COLUMNS environment variable or a default width.
func ttyColumns(fd uintptr) int { return 0 }

// isTerminal cannot be determined portably off linux/darwin; assume not a
// terminal so color is disabled (the safe default).
func isTerminal(fd uintptr) bool { return false }
