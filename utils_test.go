package main

import (
	"strings"
	"testing"
)

func TestSanitizeForTerminal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain url unchanged", "http://example.com/a", "http://example.com/a"},
		{"unicode preserved", "http://例え.example.com/パス", "http://例え.example.com/パス"},
		{"strips ANSI escape", "http://example.com/\x1b[31mred\x1b[0m", "http://example.com/[31mred[0m"},
		{"strips CR/LF", "http://example.com/a\r\nEVIL", "http://example.com/aEVIL"},
		{"strips NUL and DEL", "abc\x00def\x7f", "abcdef"},
		{"strips C1 controls", "a\x9bxyz", "axyz"},
		{"strips tab", "a\tb", "ab"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeForTerminal(tt.in); got != tt.want {
				t.Errorf("sanitizeForTerminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeForTerminalNoControlBytesRemain(t *testing.T) {
	got := sanitizeForTerminal("\x00\x07\x1b\x7f\x9f-clean-\ttext")
	for i, r := range got {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control rune %#x survived at index %d in %q", r, i, got)
		}
	}
	if !strings.Contains(got, "clean") {
		t.Fatalf("legitimate text was dropped: %q", got)
	}
}

func TestCompileExtRegex(t *testing.T) {
	tests := []struct {
		name          string
		include       string
		exclude       string
		wantInclude   bool
		wantNil       bool
		shouldMatch   []string
		shouldNotMtch []string
	}{
		{
			name:          "exclude list",
			exclude:       "js,css,png",
			wantInclude:   false,
			shouldMatch:   []string{"/a.js", "/style.CSS", "http://x/y.png"},
			shouldNotMtch: []string{"/a.html", "/page", "/a.jsx"},
		},
		{
			name:          "include takes precedence over exclude",
			include:       "json,xml",
			exclude:       "js,css",
			wantInclude:   true,
			shouldMatch:   []string{"/data.json", "/feed.XML"},
			shouldNotMtch: []string{"/a.js", "/a.css", "/a.html"},
		},
		{
			name:    "empty produces nil regex",
			wantNil: true,
		},
		{
			name:        "leading dots and spaces tolerated",
			exclude:     " .js , .css ",
			wantInclude: false,
			shouldMatch: []string{"/a.js", "/a.css"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, includeMode, err := CompileExtRegex(tt.include, tt.exclude)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if includeMode != tt.wantInclude {
				t.Errorf("includeMode = %v, want %v", includeMode, tt.wantInclude)
			}
			if tt.wantNil {
				if re != nil {
					t.Errorf("expected nil regex, got %v", re)
				}
				return
			}
			if re == nil {
				t.Fatal("expected non-nil regex")
			}
			for _, s := range tt.shouldMatch {
				if !re.MatchString(s) {
					t.Errorf("expected %q to match %v", s, re)
				}
			}
			for _, s := range tt.shouldNotMtch {
				if re.MatchString(s) {
					t.Errorf("expected %q NOT to match %v", s, re)
				}
			}
		})
	}
}

func TestNormalizeURLForCDX(t *testing.T) {
	tests := []struct {
		in   string
		subs bool
		want string
	}{
		{"example.com", false, "example.com*"},
		{"https://example.com", false, "example.com*"},
		{"http://example.com/api/v1", false, "example.com/api/v1*"},
		{"example.com/api*", false, "example.com/api*"},
		{"  https://example.com  ", false, "example.com*"},
		{"example.com", true, "*.example.com"},
		{"https://example.com/path?q=1", true, "*.example.com"},
		{"https://example.com:8080/x", true, "*.example.com"},
	}
	for _, tt := range tests {
		got := normalizeURLForCDX(tt.in, tt.subs)
		if got != tt.want {
			t.Errorf("normalizeURLForCDX(%q, %v) = %q, want %q", tt.in, tt.subs, got, tt.want)
		}
	}
}

func TestNormalizeBaseDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"example.com", "example.com"},
		{"https://example.com", "example.com"},
		{"*.example.com", "example.com"},
		{"http://example.com/", "example.com/"},
	}
	for _, tt := range tests {
		c := &Config{URLPattern: tt.in}
		if got := c.NormalizeBaseDomain(); got != tt.want {
			t.Errorf("NormalizeBaseDomain(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
