package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCDX mimics the Wayback CDX API: a showNumPages reply, then two pages of
// results. JSON mode (fl contains "timestamp") returns multi-column lines;
// otherwise just the original URL. Page 0 includes a filtered extension and a
// duplicate so the pipeline's filtering and dedup are exercised.
func fakeCDX(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if q.Get("showNumPages") == "true" {
			fmt.Fprintln(w, "2")
			return
		}
		jsonMode := strings.Contains(q.Get("fl"), "timestamp")
		row := func(url, ts, st, mime string) {
			if jsonMode {
				fmt.Fprintf(w, "%s %s %s %s\n", url, ts, st, mime)
			} else {
				fmt.Fprintln(w, url)
			}
		}
		switch q.Get("page") {
		case "0":
			row("http://example.com/a", "20200101", "200", "text/html")
			row("http://example.com/skip.js", "20200101", "200", "application/javascript")
			row("http://example.com/a", "20200102", "200", "text/html") // duplicate URL
		case "1":
			row("http://example.com/c", "20210101", "301", "text/html")
			row("http://sub.example.com/d", "20210101", "200", "text/html")
		}
	}))
}

// newPipelineRunner wires a Runner to the fake server, capturing output in out.
func newPipelineRunner(t *testing.T, srv *httptest.Server, cfg *Config, out *bytes.Buffer) *Runner {
	t.Helper()
	if cfg.Workers == 0 {
		cfg.Workers = 3
	}
	if cfg.PageWorkers == 0 {
		cfg.PageWorkers = 2
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.URLPattern == "" {
		cfg.URLPattern = "example.com"
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}
	re, includeMode, err := CompileExtRegex(cfg.IncludeExt, cfg.EffectiveExclude())
	if err != nil {
		t.Fatalf("CompileExtRegex: %v", err)
	}
	return &Runner{
		cfg:            cfg,
		client:         srv.Client(),
		baseURL:        srv.URL,
		log:            newLogger(cfg.Silent, false),
		extRegex:       re,
		includeMode:    includeMode,
		currentPattern: cfg.URLPattern,
		baseDomain:     baseDomainOf(cfg.URLPattern),
		outWriter:      out,
	}
}

func outputLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestPipelineDefault(t *testing.T) {
	srv := fakeCDX(t)
	defer srv.Close()

	var buf bytes.Buffer
	r := newPipelineRunner(t, srv, &Config{ExcludeDefaults: true}, &buf)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := outputLines(buf.String())
	want := map[string]int{
		"http://example.com/a":     0,
		"http://example.com/c":     0,
		"http://sub.example.com/d": 0,
	}
	for _, l := range got {
		if strings.Contains(l, "skip.js") {
			t.Errorf("excluded extension leaked into output: %q", l)
		}
		if _, ok := want[l]; !ok {
			t.Errorf("unexpected output line: %q", l)
			continue
		}
		want[l]++
	}
	for url, n := range want {
		if n != 1 {
			t.Errorf("%q appeared %d times, want exactly 1 (dedup)", url, n)
		}
	}
}

func TestPipelineJSON(t *testing.T) {
	srv := fakeCDX(t)
	defer srv.Close()

	var buf bytes.Buffer
	r := newPipelineRunner(t, srv, &Config{JSON: true, ExcludeDefaults: true}, &buf)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := outputLines(buf.String())
	seen := map[string]jsonRecord{}
	for _, l := range got {
		var rec jsonRecord
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Fatalf("invalid JSON line %q: %v", l, err)
		}
		if strings.Contains(rec.URL, "skip.js") {
			t.Errorf("excluded extension leaked into JSON: %q", rec.URL)
		}
		seen[rec.URL] = rec
	}
	if len(got) != 3 || len(seen) != 3 {
		t.Errorf("got %d lines / %d unique urls, want 3/3: %v", len(got), len(seen), got)
	}
	if rec := seen["http://example.com/c"]; rec.Status != "301" || rec.Timestamp != "20210101" || rec.Mime != "text/html" {
		t.Errorf("record for /c = %+v, want status 301 / ts 20210101 / mime text/html", rec)
	}
}

func TestPipelineSubs(t *testing.T) {
	srv := fakeCDX(t)
	defer srv.Close()

	var buf bytes.Buffer
	r := newPipelineRunner(t, srv, &Config{Subs: true}, &buf)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := outputLines(buf.String())
	if len(got) != 1 || got[0] != "sub.example.com" {
		t.Errorf("subs output = %v, want [sub.example.com]", got)
	}
}

// TestPipelineCancelMidFlight drives the pipeline against a server that blocks
// on each page request, then cancels — exercising the fetch/dispatch/shutdown
// cancellation paths under the race detector. It must return, not hang.
func TestPipelineCancelMidFlight(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("showNumPages") == "true" {
			fmt.Fprintln(w, "5")
			return
		}
		// Block until the client cancels (or a safety timeout) so pages are
		// genuinely in flight when the run is cancelled.
		select {
		case <-req.Context().Done():
		case <-time.After(8 * time.Second):
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	r := newPipelineRunner(t, srv, &Config{PageWorkers: 2}, &buf)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(150 * time.Millisecond) // let fetchers get into page requests
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation — possible deadlock")
	}
}

// streamWriter forwards each Write to a channel so a test can observe output as
// it is produced, rather than only after the run completes.
type streamWriter struct{ ch chan string }

func (w *streamWriter) Write(p []byte) (int, error) {
	w.ch <- string(p)
	return len(p), nil
}

// TestPipelineStreamsOutput proves results are flushed as they are found: page 0
// returns a URL immediately while page 1 blocks, and the early result must reach
// the writer before the run finishes. With buffered (non-streaming) output the
// single short line would sit in the buffer and never arrive in time.
func TestPipelineStreamsOutput(t *testing.T) {
	var once sync.Once
	release := make(chan struct{})
	rel := func() { once.Do(func() { close(release) }) }
	defer rel() // free the blocked handler on any exit path

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if q.Get("showNumPages") == "true" {
			fmt.Fprintln(w, "2")
			return
		}
		switch q.Get("page") {
		case "0":
			fmt.Fprintln(w, "http://example.com/early")
		case "1":
			<-release // hold page 1 until the test has seen page 0's output
			fmt.Fprintln(w, "http://example.com/late")
		}
	}))
	defer srv.Close()

	sw := &streamWriter{ch: make(chan string, 16)}
	r := newPipelineRunner(t, srv, &Config{PageWorkers: 1, Workers: 1}, nil)
	r.outWriter = sw

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	select {
	case got := <-sw.ch:
		if !strings.Contains(got, "early") {
			t.Fatalf("first streamed chunk = %q, want the early URL", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output streamed before the run finished — output is being buffered")
	}

	rel() // let page 1 complete
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not complete")
	}
}
