package main

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// CompileExtRegex builds a compiled regex for extension filtering.
// Returns (regex, includeMode, error). includeMode is true when
// includeCSV takes precedence over excludeCSV.
func CompileExtRegex(includeCSV, excludeCSV string) (*regexp.Regexp, bool, error) {
	includeMode := false
	csv := excludeCSV
	if strings.TrimSpace(includeCSV) != "" {
		includeMode = true
		csv = includeCSV
	}
	parts := []string{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = strings.TrimPrefix(p, ".")
		parts = append(parts, regexp.QuoteMeta(p))
	}
	if len(parts) == 0 {
		return nil, includeMode, nil
	}
	re, err := regexp.Compile(`(?i)\.(` + strings.Join(parts, "|") + `)$`)
	return re, includeMode, err
}

// sanitizeForTerminal strips control and escape characters from untrusted data
// before it is printed. Archived URLs come from the Wayback CDX API, which is
// attacker-influenced (anyone can archive a crafted URL), so a raw line could
// carry ANSI escape sequences or other control bytes that hijack the operator's
// terminal. Valid URLs never contain these bytes, so removing them is lossless
// for legitimate data. Removes C0 controls (0x00-0x1F), DEL (0x7F), and C1
// controls (0x80-0x9F); tab is dropped too since URLs never contain one.
func sanitizeForTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		// Drop invalid bytes outright — a lone 0x9b is the single-byte C1 CSI
		// introducer and never appears in valid UTF-8. Decoding rune-by-rune
		// (rather than byte-by-byte) keeps legitimate multibyte characters,
		// whose continuation bytes also fall in 0x80-0x9f, intact.
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r >= 0x20 && r != 0x7f && !(r >= 0x80 && r <= 0x9f) {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// readTargets reads non-empty, non-comment lines from r and returns them.
// Used by --stdin and --list to gather target domains.
func readTargets(r io.Reader) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
