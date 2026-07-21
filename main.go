package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
)

const defaultExclude = "js,css,png,jpg,jpeg,gif,svg,webp,ico,bmp,tif,tiff,woff,woff2,ttf,eot,mp4,mp3,wav,avi,mov,mkv,zip,rar,7z,pdf"

// version is set at release time via -ldflags "-X main.version=<tag>". When
// empty, appVersion falls back to Go's embedded build info.
var version string

// appVersion resolves the version to report. Precedence: the release ldflag,
// then the module version (set for `go install <module>@vX.Y.Z`), then the VCS
// revision embedded for local builds, then "dev".
func appVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		var rev string
		var dirty bool
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	return "dev"
}

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
		// Run already reported the failure(s) via its logger; just set the code.
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
		// Honor an explicit wildcard instead of prepending another (mirrors the
		// non-subs branch below): "*.example.com" must stay "*.example.com", not
		// become "*.*.example.com".
		if strings.Contains(host, "*") {
			return host
		}
		return "*." + host
	}

	// For normal queries: preserve the full path, just append * if no wildcard.
	if !strings.Contains(s, "*") {
		return s + "*"
	}
	return s
}
