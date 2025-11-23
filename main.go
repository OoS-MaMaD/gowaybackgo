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
// subdomain captures. Otherwise we behave like normalizeURL (append a trailing
// '*') unless the user already provided a wildcard.
func normalizeURLForCDX(u string, subs bool) string {
	s := strings.TrimSpace(u)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	// remove any path or port
	if idx := strings.IndexAny(s, "/:\\\\"); idx >= 0 {
		s = s[:idx]
	}
	s = strings.ReplaceAll(s, "*", "")
	s = strings.Trim(s, " .")
	if subs {
		// request leading wildcard for subdomain enumeration
		return "*." + s
	}
	if !strings.Contains(u, "*") {
		return s + "*"
	}
	return u
}
