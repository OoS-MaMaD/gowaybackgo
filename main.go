package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

const defaultExclude = "js,css,png,jpg,jpeg,gif,svg,webp,ico,bmp,tif,tiff,woff,woff2,ttf,eot,mp4,mp3,wav,avi,mov,mkv,zip,rar,7z,pdf"

func main() {
	cfg, err := ParseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR:", err)
		flag.Usage()
		os.Exit(1)
	}

	runner, err := NewRunner(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR initializing runner:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := runner.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR:", err)
		os.Exit(1)
	}
}

// normalizeURL ensures the pattern ends with * if missing
func normalizeURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if !strings.Contains(u, "*") {
		u += "*"
	}
	return u
}

// normalizeURLForCDX prepares a URL pattern for CDX queries.
// If subs==true we return a leading-wildcard base like "*.example.com" to get
// subdomain captures. Otherwise we append a trailing '*' unless the user
// already provided a wildcard.
// Only the host portion is kept so that paths/ports don't corrupt the query.
func normalizeURLForCDX(u string, subs bool) string {
	s := strings.TrimSpace(u)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")

	// Strip path, port, and any query/fragment so only the host (+ existing
	// wildcard) is sent to the CDX API.
	if idx := strings.IndexAny(s, "/:?#"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.ReplaceAll(s, "*", "")
	s = strings.Trim(s, " .")

	if subs {
		return "*." + s
	}
	return s + "*"
}
