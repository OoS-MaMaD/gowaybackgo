package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

	// Cancel the run cleanly on Ctrl-C / SIGTERM. The fetch/retry/print paths
	// already honour ctx cancellation, so this drains in-flight work and closes
	// the output file instead of leaving a half-written terminal or file.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR:", err)
		os.Exit(1)
	}
}

// normalizeURLForCDX prepares a URL pattern for CDX queries.
// If subs==true we return a leading-wildcard base like "*.example.com" to get
// subdomain captures. Otherwise we behave like normalizeURL (append a trailing
// '*') unless the user already provided a wildcard.
// NOTE: path/port stripping is intentionally not done here — the full URL
// pattern (including any path) is passed through to the CDX API as-is.
func normalizeURLForCDX(u string, subs bool) string {
	s := strings.TrimSpace(u)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")

	if subs {
		// Request leading wildcard for subdomain enumeration.
		// Strip to host only for subs mode so *.example.com/path doesn't break.
		host := s
		if idx := strings.IndexAny(host, "/:?#"); idx >= 0 {
			host = host[:idx]
		}
		host = strings.Trim(host, " .")
		return "*." + host
	}

	// For normal queries: preserve the full path, just append * if no wildcard.
	if !strings.Contains(s, "*") {
		return s + "*"
	}
	return s
}
