package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// newTestRunner builds a Runner wired only with the fields processLine reads,
// compiling the extension regex the same way NewRunner does.
func newTestRunner(t *testing.T, cfg *Config) *Runner {
	t.Helper()
	re, includeMode, err := CompileExtRegex(cfg.IncludeExt, cfg.EffectiveExclude())
	if err != nil {
		t.Fatalf("CompileExtRegex: %v", err)
	}
	return &Runner{cfg: cfg, extRegex: re, includeMode: includeMode}
}

func TestProcessLine(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		line string
		want []string
	}{
		{
			name: "empty line dropped",
			cfg:  &Config{},
			line: "   ",
			want: nil,
		},
		{
			name: "default passes url through",
			cfg:  &Config{},
			line: "http://example.com/page",
			want: []string{"http://example.com/page"},
		},
		{
			name: "exclude extension filters match",
			cfg:  &Config{ExcludeExt: "js,css", ExcludeDefaults: true},
			line: "http://example.com/app.js",
			want: nil,
		},
		{
			name: "exclude extension keeps non-match",
			cfg:  &Config{ExcludeDefaults: true},
			line: "http://example.com/page.html",
			want: []string{"http://example.com/page.html"},
		},
		{
			name: "include extension keeps only listed",
			cfg:  &Config{IncludeExt: "json,xml"},
			line: "http://example.com/data.json",
			want: []string{"http://example.com/data.json"},
		},
		{
			name: "include extension drops others",
			cfg:  &Config{IncludeExt: "json,xml"},
			line: "http://example.com/app.js",
			want: nil,
		},
		{
			name: "only-query returns raw query",
			cfg:  &Config{OnlyQuery: true},
			line: "http://example.com/p?a=1&b=2",
			want: []string{"a=1&b=2"},
		},
		{
			name: "only-query with no query drops",
			cfg:  &Config{OnlyQuery: true},
			line: "http://example.com/p",
			want: nil,
		},
		{
			name: "only-query-keys extracts keys",
			cfg:  &Config{OnlyQueryKeys: true},
			line: "http://example.com/p?foo=1&bar=2",
			want: []string{"foo", "bar"},
		},
		{
			name: "only-query-keys decodes percent-encoded key",
			cfg:  &Config{OnlyQueryKeys: true},
			line: "http://example.com/p?%66oo=1",
			want: []string{"foo"},
		},
		{
			name: "no-query strips query string",
			cfg:  &Config{NoQuery: true},
			line: "http://example.com/p?a=1",
			want: []string{"http://example.com/p"},
		},
		{
			name: "subs mode skips extension filter",
			cfg:  &Config{Subs: true, ExcludeDefaults: true},
			line: "http://sub.example.com/app.js",
			want: []string{"http://sub.example.com/app.js"},
		},
		{
			name: "json mode passes full record through",
			cfg:  &Config{JSON: true},
			line: "http://example.com/p 20200101 200 text/html",
			want: []string{"http://example.com/p 20200101 200 text/html"},
		},
		{
			name: "json mode filters on url field",
			cfg:  &Config{JSON: true, ExcludeDefaults: true},
			line: "http://example.com/app.js 20200101 200 application/javascript",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRunner(t, tt.cfg)
			got := r.processLine(tt.line)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("processLine(%q) = %#v, want %#v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseCDXRecord(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   jsonRecord
		wantOK bool
	}{
		{"blank", "   ", jsonRecord{}, false},
		{"url only", "http://x/a", jsonRecord{URL: "http://x/a"}, true},
		{
			"full record",
			"http://x/a 20200101 200 text/html",
			jsonRecord{URL: "http://x/a", Timestamp: "20200101", Status: "200", Mime: "text/html"},
			true,
		},
		{
			"missing fields are dashes",
			"http://x/a - 200 -",
			jsonRecord{URL: "http://x/a", Status: "200"},
			true,
		},
		{
			"control chars stripped from url",
			"http://x/\x1b[31m 20200101 200 text/html",
			jsonRecord{URL: "http://x/[31m", Timestamp: "20200101", Status: "200", Mime: "text/html"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCDXRecord(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseCDXRecord(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("parseCDXRecord(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestCDXURL(t *testing.T) {
	r := &Runner{
		cfg: &Config{
			JSON:   true,
			From:   "2020",
			To:     "2022",
			Status: "200",
			Mime:   "text/html",
		},
		baseURL:        cdxBaseURL,
		currentPattern: "https://example.com/api",
	}

	t.Run("page request", func(t *testing.T) {
		u, err := url.Parse(r.cdxURL(3, false))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		q := u.Query()
		checks := map[string]string{
			"url":      "example.com/api*",
			"fl":       "original,timestamp,statuscode,mimetype",
			"collapse": "urlkey",
			"page":     "3",
			"from":     "2020",
			"to":       "2022",
		}
		for k, want := range checks {
			if got := q.Get(k); got != want {
				t.Errorf("query %q = %q, want %q", k, got, want)
			}
		}
		filters := q["filter"]
		wantFilters := map[string]bool{"statuscode:200": false, "mimetype:text/html": false}
		for _, f := range filters {
			if _, ok := wantFilters[f]; ok {
				wantFilters[f] = true
			}
		}
		for f, seen := range wantFilters {
			if !seen {
				t.Errorf("missing filter %q in %v", f, filters)
			}
		}
	})

	t.Run("page-count request omits paging fields", func(t *testing.T) {
		u, err := url.Parse(r.cdxURL(0, true))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		q := u.Query()
		if q.Get("showNumPages") != "true" {
			t.Errorf("showNumPages = %q, want true", q.Get("showNumPages"))
		}
		if q.Get("page") != "" || q.Get("fl") != "" {
			t.Errorf("page-count request should not set page/fl, got page=%q fl=%q", q.Get("page"), q.Get("fl"))
		}
		// Filters still apply so the count matches the fetched results.
		if q.Get("from") != "2020" || len(q["filter"]) != 2 {
			t.Errorf("page-count request should keep from/filter params, got from=%q filters=%v", q.Get("from"), q["filter"])
		}
	})
}

func TestFetchWithRetry(t *testing.T) {
	t.Run("success returns response without retry and sends UA", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			atomic.AddInt32(&hits, 1)
			if req.Header.Get("User-Agent") != userAgent {
				t.Errorf("User-Agent = %q, want %q", req.Header.Get("User-Agent"), userAgent)
			}
			fmt.Fprintln(w, "ok")
		}))
		defer srv.Close()
		r := &Runner{client: srv.Client(), cfg: &Config{Retries: 3}, log: newLogger(false, false)}
		resp, err := r.fetchWithRetry(context.Background(), srv.URL, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("hits = %d, want 1", got)
		}
	})

	t.Run("404 fails fast without retrying", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		r := &Runner{client: srv.Client(), cfg: &Config{Retries: 3}, log: newLogger(false, false)}
		if _, err := r.fetchWithRetry(context.Background(), srv.URL, nil); err == nil {
			t.Fatal("expected an error for HTTP 404")
		}
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("hits = %d, want 1 (a 404 must not be retried)", got)
		}
	})

	t.Run("already-cancelled context returns immediately", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &Runner{client: http.DefaultClient, cfg: &Config{Retries: 3}, log: newLogger(false, false)}
		if _, err := r.fetchWithRetry(ctx, "http://example.invalid", nil); err == nil {
			t.Fatal("expected an error on a cancelled context")
		}
	})
}

func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("sleepCtx should return true after the delay elapses")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("sleepCtx should return false when the context is cancelled")
	}
}
