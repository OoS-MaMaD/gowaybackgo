package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// PBar is a simple TTY-aware progress bar.
// When /dev/tty is available the bar is rendered there so stdout remains safe
// to pipe. If no TTY is available rendering is disabled and Log writes colored
// messages to stderr instead.
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
	last        int
}

func NewPBar(total int) *PBar {
	p := &PBar{
		Total:      total,
		Width:      40,
		DoneStr:    "#",
		OngoingStr: ".",
		start:      time.Now(),
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		p.out = tty
	}
	return p
}

// Render updates the progress bar to the given current value.
// No-op when no TTY is available.
func (p *PBar) Render(curr int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renderLocked(curr)
}

// renderLocked is the internal render implementation. Caller must hold p.mu.
func (p *PBar) renderLocked(curr int) {
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

	green := "\033[32m"
	dim := "\033[37m"
	reset := "\033[0m"
	donePart := strings.Repeat(p.DoneStr, done)
	remPart := strings.Repeat(p.OngoingStr, p.Width-done)
	coloredBar := fmt.Sprintf("%s%s%s%s%s", green, donePart, reset, dim, remPart)

	fmt.Fprint(p.out, "\r\033[K")

	status := p.status
	if len(status) > 60 {
		status = status[:57] + "..."
	}

	percent := float64(curr) / float64(p.Total) * 100
	if status != "" {
		if p.statusColor != "" {
			fmt.Fprintf(p.out, "[%s] %d/%d (%.1f%%) [%s%s%s]", coloredBar, curr, p.Total, percent, p.statusColor, status, reset)
		} else {
			fmt.Fprintf(p.out, "[%s] %d/%d (%.1f%%) [%s]", coloredBar, curr, p.Total, percent, status)
		}
	} else {
		fmt.Fprintf(p.out, "[%s] %d/%d (%.1f%%)", coloredBar, curr, p.Total, percent)
	}

	p.last = curr
}

// ClearLine erases the current progress line so other output can be printed.
func (p *PBar) ClearLine() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	fmt.Fprint(p.out, "\r\033[K")
}

// Log sets a short status message shown after the progress bar.
// Falls back to stderr when no TTY is available.
func (p *PBar) Log(msg string, color string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = msg
	p.statusColor = color
	if p.out == nil {
		reset := "\033[0m"
		if color == "" {
			fmt.Fprintln(os.Stderr, msg)
		} else {
			fmt.Fprintln(os.Stderr, color+msg+reset)
		}
		return
	}
	// Re-render with the updated status using the last known progress value.
	// We already hold the lock so call the internal implementation directly.
	p.renderLocked(p.last)
}

// Finish moves to the next line and closes /dev/tty if opened.
func (p *PBar) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	fmt.Fprintln(p.out, "")
	_ = p.out.Close()
	p.out = nil
}
