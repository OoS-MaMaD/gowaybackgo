package main

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// visibleWidth strips ANSI escapes and the carriage return, then counts runes.
func visibleWidth(s string) int {
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return utf8.RuneCountInString(s)
}

// captureDraw drives one draw() at curr and returns the raw bytes written.
func captureDraw(t *testing.T, p *PBar, curr int) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	p.out = w
	p.mu.Lock()
	p.curr = curr
	p.draw()
	p.mu.Unlock()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestDrawFitsTerminalWidth(t *testing.T) {
	t.Setenv("COLUMNS", "60")
	for _, cols := range []string{"60", "40", "24", "100"} {
		t.Run("cols="+cols, func(t *testing.T) {
			t.Setenv("COLUMNS", cols)
			p := &PBar{Total: 200, Width: 40, DoneStr: "#", OngoingStr: ".", start: time.Now().Add(-5 * time.Second)}
			p.status = "fetching page 50"
			out := captureDraw(t, p, 50)
			want, _ := strconv.Atoi(cols)
			if got := visibleWidth(out); got > want {
				t.Errorf("visible width %d exceeds terminal width %d\nline: %q", got, want, out)
			}
		})
	}
}

func TestDrawContent(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	p := &PBar{Total: 200, Width: 40, DoneStr: "#", OngoingStr: ".", start: time.Now().Add(-5 * time.Second)}
	out := captureDraw(t, p, 50)
	for _, want := range []string{"[", "]", "50/200", "25%", "eta"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw output missing %q\ngot: %q", want, out)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{-5 * time.Second, "0:00"},
		{5 * time.Second, "0:05"},
		{65 * time.Second, "1:05"},
		{9 * time.Minute, "9:00"},
		{time.Hour, "1:00:00"},
		{3725 * time.Second, "1:02:05"},
		{500 * time.Millisecond, "0:01"}, // rounds to nearest second
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"fits", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
		{"zero max", "hello", 0, ""},
		{"tiny max hard cut", "hello", 2, "he"},
		{"multibyte not split", "héllo wörld", 8, "héllo..."},
		{"multibyte fits", "héllo", 5, "héllo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
			// Result must be valid UTF-8 and never exceed max runes.
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
			if n := utf8.RuneCountInString(got); tt.max > 0 && n > tt.max {
				t.Errorf("result has %d runes, exceeds max %d", n, tt.max)
			}
		})
	}
}

func TestColumnsFallback(t *testing.T) {
	// out is nil here, so columns() skips the ioctl and uses $COLUMNS / default.
	p := &PBar{}

	t.Run("valid COLUMNS", func(t *testing.T) {
		t.Setenv("COLUMNS", "120")
		if got := p.columns(); got != 120 {
			t.Errorf("columns() = %d, want 120", got)
		}
	})
	t.Run("invalid COLUMNS falls back to 80", func(t *testing.T) {
		t.Setenv("COLUMNS", "not-a-number")
		if got := p.columns(); got != 80 {
			t.Errorf("columns() = %d, want 80", got)
		}
	})
	t.Run("empty COLUMNS falls back to 80", func(t *testing.T) {
		t.Setenv("COLUMNS", "")
		if got := p.columns(); got != 80 {
			t.Errorf("columns() = %d, want 80", got)
		}
	})
}

func TestTimingETA(t *testing.T) {
	// 2 of 10 done after ~2s → ~8s remaining. Assert the shape, not exact ms.
	p := &PBar{Total: 10, start: time.Now().Add(-2 * time.Second)}
	out := p.timing(2)
	if !containsETA(out) {
		t.Errorf("timing(2/10) = %q, expected an eta segment", out)
	}
	// Completed: no ETA once curr == Total.
	if got := p.timing(10); containsETA(got) {
		t.Errorf("timing(10/10) = %q, should not contain eta", got)
	}
}

func containsETA(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		if s[i:i+4] == " eta" {
			return true
		}
	}
	return false
}
