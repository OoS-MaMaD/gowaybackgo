package main

import (
	"bufio"
	"os"
	"regexp"
	"strings"
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
	reStr := `(?i)\.( ` + strings.Join(parts, "|") + `)$`
	reStr = strings.ReplaceAll(reStr, "( ", "(")
	re, err := regexp.Compile(reStr)
	return re, includeMode, err
}

// readStdin reads non-empty lines from stdin and returns them as a slice.
// Used by --stdin mode to pipe in a list of target domains.
func readStdin() ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
