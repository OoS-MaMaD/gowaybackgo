package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ANSI escape sequences used by the progress bar.
const (
	ansiGreen     = "\033[32m"
	ansiDim       = "\033[37m"
	ansiReset     = "\033[0m"
	ansiHideCur   = "\033[?25l"
	ansiShowCur   = "\033[?25h"
	ansiClearLine = "\r\033[K"
)

// minRenderInterval throttles progress redraws. Render is called once per
// completed page and once per printed line, which can be thousands of times a
// second; without throttling that floods the terminal with escape sequences
// and flickers. The final update (curr == Total) always draws.
const minRenderInterval = 60 * time.Millisecond

// PBar is a simple TTY-aware progress bar.
// When /dev/tty is available the bar is rendered there so stdout remains safe
// to pipe. If no TTY is available rendering is disabled and Log writes messages
// to stderr instead. The bar sizes itself to the terminal width and shows an
// elapsed/ETA readout.
type PBar struct {
	Total      int
	Width      int // preferred bar width; shrunk to fit the terminal
	DoneStr    string
	OngoingStr string

	mu          sync.Mutex
	out         *os.File // /dev/tty when available, otherwise nil (disabled)
	start       time.Time
	curr        int // latest value seen (may be undrawn due to throttling)
	status      string
	statusColor string
	lastDraw    time.Time
	noColor     bool // NO_COLOR set: never emit color
	colorStderr bool // stderr is a terminal and color is enabled
}

func NewPBar(total int) *PBar {
	noColor := os.Getenv("NO_COLOR") != ""
	p := &PBar{
		Total:       total,
		Width:       40,
		DoneStr:     "#",
		OngoingStr:  ".",
		start:       time.Now(),
		noColor:     noColor,
		colorStderr: !noColor && ttyColumns(os.Stderr.Fd()) > 0,
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		p.out = tty
		fmt.Fprint(p.out, ansiHideCur) // hidden until Finish restores it
	}
	return p
}

// Render updates the bar to curr. No-op without a TTY. Intermediate redraws are
// throttled to minRenderInterval; the terminal update for curr == Total always
// draws so the bar reliably reaches 100%.
func (p *PBar) Render(curr int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.curr = curr
	if p.out == nil {
		return
	}
	if curr < p.Total && time.Since(p.lastDraw) < minRenderInterval {
		return
	}
	p.draw()
}

// col wraps s in an ANSI color unless color is disabled or code is empty.
func (p *PBar) col(code, s string) string {
	if p.noColor || code == "" {
		return s
	}
	return code + s + ansiReset
}

// draw renders the bar for p.curr. Caller must hold p.mu and p.out must be set.
func (p *PBar) draw() {
	cols := p.columns()

	if p.Total <= 0 {
		fmt.Fprint(p.out, ansiClearLine, truncateRunes(fmt.Sprintf("Progress: %d", p.curr), cols))
		p.lastDraw = time.Now()
		return
	}

	curr := p.curr
	if curr > p.Total {
		curr = p.Total
	}
	percent := float64(curr) / float64(p.Total) * 100
	meta := fmt.Sprintf(" %d/%d (%.0f%%) %s", curr, p.Total, percent, p.timing(curr))

	// The numbers/ETA are the priority. Shrink the bar to fit around them; if
	// the terminal is too narrow for a bracketed bar at all, show meta alone
	// (truncated) so the line can never wrap.
	barW := p.Width
	if avail := cols - 2 - runeLen(meta); barW > avail {
		barW = avail
	}
	if barW < 1 {
		fmt.Fprint(p.out, ansiClearLine, truncateRunes(meta, cols))
		p.lastDraw = time.Now()
		return
	}

	done := int(float64(curr) * float64(barW) / float64(p.Total))
	if done > barW {
		done = barW
	}
	bar := p.col(ansiGreen, strings.Repeat(p.DoneStr, done)) +
		p.col(ansiDim, strings.Repeat(p.OngoingStr, barW-done))

	// Status gets whatever horizontal space is left; drop it if too tight.
	statusPart := ""
	if p.status != "" {
		if rem := cols - (2 + barW + runeLen(meta)) - 3; rem >= 4 { // 3 = " [" + "]"
			statusPart = " [" + p.col(p.statusColor, truncateRunes(p.status, rem)) + "]"
		}
	}

	fmt.Fprint(p.out, ansiClearLine, "[", bar, "]", meta, statusPart)
	p.lastDraw = time.Now()
}

// timing returns an elapsed readout, plus an ETA once progress is underway.
func (p *PBar) timing(curr int) string {
	elapsed := time.Since(p.start)
	out := formatDuration(elapsed)
	if curr > 0 && curr < p.Total {
		eta := (elapsed / time.Duration(curr)) * time.Duration(p.Total-curr)
		out += " eta " + formatDuration(eta)
	}
	return out
}

// columns returns the usable terminal width, falling back to $COLUMNS then 80.
func (p *PBar) columns() int {
	if p.out != nil {
		if c := ttyColumns(p.out.Fd()); c > 0 {
			return c
		}
	}
	if env := strings.TrimSpace(os.Getenv("COLUMNS")); env != "" {
		if c, err := strconv.Atoi(env); err == nil && c > 0 {
			return c
		}
	}
	return 80
}

// ClearLine erases the current progress line so other output can be printed.
func (p *PBar) ClearLine() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	fmt.Fprint(p.out, ansiClearLine)
}

// Log sets a short status message shown after the progress bar. Falls back to
// stderr when no TTY is available (colored only if stderr is a terminal).
func (p *PBar) Log(msg string, color string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = msg
	p.statusColor = color
	if p.out == nil {
		if p.colorStderr && color != "" {
			fmt.Fprintln(os.Stderr, color+msg+ansiReset)
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		return
	}
	p.draw() // forced redraw with the new status
}

// Finish draws the final state, moves to a new line, restores the cursor, and
// closes /dev/tty if it was opened.
func (p *PBar) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out == nil {
		return
	}
	p.draw()
	fmt.Fprint(p.out, "\n", ansiShowCur)
	_ = p.out.Close()
	p.out = nil
}

// runeLen counts visible runes (used to budget line width around ANSI codes).
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// truncateRunes shortens s to at most max visible runes, adding an ellipsis
// when truncated. Operates on runes so multibyte characters are never split.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max <= 3 {
		return string(r[:max]) // no room for an ellipsis
	}
	return string(r[:max-3]) + "..."
}

// formatDuration renders a duration as m:ss or h:mm:ss.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second) / time.Second)
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
