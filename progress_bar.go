package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Simple TTY-aware progress bar. When a tty (/dev/tty) is available the bar
// is rendered there so stdout remains safe to pipe. If no tty is available
// (for example when stdout is redirected), rendering is disabled and Log will
// instead write colored messages to stderr so they don't mix with piped data.
type PBar struct {
	Total       int
	Width       int
	DoneStr     string
	OngoingStr  string
	mu          sync.Mutex
	out         *os.File // /dev/tty when available, otherwise nil (disabled)
	start       time.Time
	status      string
	statusColor string
}

func NewPBar(total int) *PBar {
	p := &PBar{
		Total:      total,
		Width:      40,
		DoneStr:    "#",
		OngoingStr: ".",
		start:      time.Now(),
	}
	// Prefer writing to /dev/tty so stdout remains pipable. If we can't open
	// /dev/tty then rendering is disabled (out == nil) to avoid interfering
	// with piped stdout.
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		p.out = tty
	} else {
		p.out = nil
	}
	return p
}

// Render updates the progress bar to the given current value. If no TTY is
// available Render is a no-op.
func (p *PBar) Render(curr int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	if p.Total <= 0 {
		fmt.Fprintf(p.out, "\rProgress: %d/%d", curr, p.Total)
		return
	}
	if curr > p.Total {
		curr = p.Total
	}
	done := int(float64(curr) * float64(p.Width) / float64(p.Total))
	if done > p.Width {
		done = p.Width
	}
	// old combined bar string removed; we build colored parts separately below

	// Colorize bar: done part green, remaining part dim
	green := "\033[32m"
	dim := "\033[90m"
	reset := "\033[0m"
	donePart := strings.Repeat(p.DoneStr, done)
	remPart := strings.Repeat(p.OngoingStr, p.Width-done)
	coloredBar := fmt.Sprintf("%s%s%s%s%s", green, donePart, reset, dim, remPart)

	// append status in brackets (trim if too long)
	status := p.status
	if status != "" {
		maxStatus := 60
		if len(status) > maxStatus {
			status = status[:maxStatus-3] + "..."
		}
	}

	// Format: [<bar>] curr/total (X.X%) [STATUS]
	percent := float64(curr) / float64(p.Total) * 100
	if status != "" {
		if p.statusColor != "" {
			fmt.Fprintf(p.out, "\r[%s] %d/%d (%.1f%%) [%s%s%s]", coloredBar, curr, p.Total, percent, p.statusColor, status, reset)
		} else {
			fmt.Fprintf(p.out, "\r[%s] %d/%d (%.1f%%) [%s]", coloredBar, curr, p.Total, percent, status)
		}
	} else {
		fmt.Fprintf(p.out, "\r[%s] %d/%d (%.1f%%)", coloredBar, curr, p.Total, percent)
	}
}

// ClearLine erases the current progress line so other output can be printed.
func (p *PBar) ClearLine() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	// ANSI escape to clear line
	fmt.Fprint(p.out, "\r\033[K")
}

// Log sets a short status message that will be shown after the progress bar.
// If no TTY is available it falls back to printing the colored message to
// stderr so piped stdout is not interrupted.
func (p *PBar) Log(msg string, color string) {
	p.mu.Lock()
	p.status = msg
	p.statusColor = color
	p.mu.Unlock()
	if p.out == nil {
		reset := "\033[0m"
		if color == "" {
			fmt.Fprintln(os.Stderr, msg)
		} else {
			fmt.Fprintln(os.Stderr, color+msg+reset)
		}
		return
	}
	// re-render to show updated status
	// Note: Render locks internally so it's safe to call here
	p.Render(0)
}

// Finish moves to the next line (call when done) and closes /dev/tty if we
// opened it.
func (p *PBar) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	fmt.Fprintln(p.out, "")
	// close if this is a file other than standard streams
	if p.out != nil {
		// best-effort close; ignore error
		_ = p.out.Close()
		p.out = nil
	}
}

func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	h := s / 3600
	m := (s % 3600) / 60
	sec := s % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}
