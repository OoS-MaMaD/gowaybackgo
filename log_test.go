package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	l := &logger{w: &buf, color: false}
	l.info("hello %d", 1)
	l.warn("careful")
	l.errf("boom")
	out := buf.String()
	for _, want := range []string{"[INF] hello 1", "[WRN] careful", "[ERR] boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLoggerSilent(t *testing.T) {
	var buf bytes.Buffer
	l := &logger{w: &buf, silent: true, color: false}
	l.info("info")
	l.warn("warn")
	if buf.Len() != 0 {
		t.Errorf("silent should suppress info/warn, got %q", buf.String())
	}
	l.errf("err")
	if !strings.Contains(buf.String(), "[ERR] err") {
		t.Errorf("silent must still surface errors, got %q", buf.String())
	}
}

func TestLoggerColor(t *testing.T) {
	var buf bytes.Buffer
	l := &logger{w: &buf, color: true}
	l.warn("x")
	out := buf.String()
	if !strings.Contains(out, "\033[33m") || !strings.Contains(out, "\033[0m") {
		t.Errorf("expected ANSI color codes, got %q", out)
	}
}
