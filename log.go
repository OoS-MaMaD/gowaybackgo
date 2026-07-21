package main

import (
	"fmt"
	"io"
	"os"
)

// logLevel orders messages by severity. In silent mode everything below error
// is suppressed.
type logLevel int

const (
	levelInfo logLevel = iota
	levelWarn
	levelError
)

// logger writes ProjectDiscovery-style leveled lines ([INF]/[WRN]/[ERR]) to
// stderr, so stdout stays a clean stream of results.
type logger struct {
	w      io.Writer
	silent bool
	color  bool
}

func newLogger(silent, color bool) *logger {
	return &logger{w: os.Stderr, silent: silent, color: color}
}

func (l *logger) info(format string, a ...any) { l.emit(levelInfo, format, a...) }
func (l *logger) warn(format string, a ...any) { l.emit(levelWarn, format, a...) }
func (l *logger) errf(format string, a ...any) { l.emit(levelError, format, a...) }

func (l *logger) emit(level logLevel, format string, a ...any) {
	// Silent shows only errors; results still go to stdout untouched.
	if l.silent && level != levelError {
		return
	}
	label, color := "INF", "\033[36m"
	switch level {
	case levelWarn:
		label, color = "WRN", "\033[33m"
	case levelError:
		label, color = "ERR", "\033[31m"
	}
	msg := fmt.Sprintf(format, a...)
	if l.color {
		fmt.Fprintf(l.w, "[%s%s\033[0m] %s\n", color, label, msg)
	} else {
		fmt.Fprintf(l.w, "[%s] %s\n", label, msg)
	}
}

// levelColor maps a level to the ANSI color used for on-progress-bar status.
func levelColor(level logLevel) string {
	switch level {
	case levelWarn:
		return colorYellow
	case levelError:
		return colorRed
	default:
		return "\033[36m"
	}
}
