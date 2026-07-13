package main

import (
	"reflect"
	"testing"
)

// newTestRunner builds a Runner wired only with the fields processLine reads,
// compiling the extension regex the same way NewRunner does.
func newTestRunner(t *testing.T, cfg *Config) *Runner {
	t.Helper()
	effectiveExclude, _ := cfg.EffectiveExclude()
	re, includeMode, err := CompileExtRegex(cfg.IncludeExt, effectiveExclude)
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
