package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// Config collects all CLI options for the tool.
type Config struct {
	URLPattern      string
	URLList         []string // domains read from stdin when -stdin is set
	OutputFile      string
	OnlyQuery       bool
	OnlyQueryKeys   bool
	NoQuery         bool
	ExcludeExt      string
	IncludeExt      string
	ExcludeDefaults bool
	Workers         int
	PageWorkers     int
	ExtractPaths    bool
	Subs            bool
	Timeout         time.Duration
	RateLimit       int // max CDX page requests per second (0 = unlimited)
	Stdin           bool
}

// ParseConfig reads command-line flags into a Config struct.
func ParseConfig() (*Config, error) {
	urlFlag := flag.String("u", "", "Target URL/domain pattern (e.g. example.com or *.example.com)")
	stdinFlag := flag.Bool("stdin", false, "Read target domains from stdin (one per line), ignores -u")
	outputFile := flag.String("o", "", "Output file (also prints to stdout)")
	onlyQuery := flag.Bool("only-query", false, "Output only full query strings")
	onlyQueryKeys := flag.Bool("only-query-keys", false, "Output only query parameter keys")
	noQuery := flag.Bool("no-query", false, "Remove query strings from URLs")
	excludeExt := flag.String("exclude-ext", "", "Comma-separated list of extensions to exclude. If the flag is omitted entirely no extensions will be excluded.")
	excludeDefaults := flag.Bool("exclude-defaults", false, "Use the default extension excludes (shorthand for -exclude-ext) [excludes: js,css,png,jpg,jpeg,gif,svg,webp,ico,bmp,tif,tiff,woff,woff2,ttf,eot,mp4,mp3,wav,avi,mov,mkv,zip,rar,7z,pdf]")
	includeExt := flag.String("include-ext", "", "Comma-separated list of extensions to include (overrides exclude)")
	workers := flag.Int("workers", 20, "Number of concurrent processing workers (for URL lines)")
	extractPaths := flag.Bool("extract-paths", false, "If set, extract unique path segments from each output URL and print each segment on its own line.")
	subs := flag.Bool("subs", false, "Only print unique subdomains for the provided base domain (e.g. example.com -> a.example.com, b.example.com)")
	pageWorkers := flag.Int("page-workers", 10, "Number of concurrent CDX page fetchers")
	timeout := flag.Int("timeout", 80, "HTTP timeout in seconds")
	rateLimit := flag.Int("rate", 0, "Max CDX page requests per second (0 = unlimited). Recommended: 5-10 to avoid rate-limiting.")
	flag.Parse()

	cfg := &Config{
		OutputFile:      *outputFile,
		OnlyQuery:       *onlyQuery,
		OnlyQueryKeys:   *onlyQueryKeys,
		NoQuery:         *noQuery,
		ExcludeExt:      *excludeExt,
		IncludeExt:      *includeExt,
		ExcludeDefaults: *excludeDefaults,
		Workers:         *workers,
		PageWorkers:     *pageWorkers,
		ExtractPaths:    *extractPaths,
		Subs:            *subs,
		Timeout:         time.Duration(*timeout) * time.Second,
		RateLimit:       *rateLimit,
		Stdin:           *stdinFlag,
	}

	if *stdinFlag {
		domains, err := readStdin()
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		if len(domains) == 0 {
			return nil, fmt.Errorf("no domains provided via stdin")
		}
		cfg.URLList = domains
		cfg.URLPattern = domains[0]
		return cfg, nil
	}

	if *urlFlag == "" {
		return nil, fmt.Errorf("-u <url> is required (or use -stdin)")
	}
	cfg.URLPattern = *urlFlag
	return cfg, nil
}

// EffectiveExclude determines the active exclusion list following user flags.
func (c *Config) EffectiveExclude() (string, bool) {
	var effectiveExclude string
	excludeProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "exclude-ext" {
			excludeProvided = true
		}
	})
	if c.ExcludeDefaults {
		effectiveExclude = defaultExclude
	} else if !excludeProvided {
		effectiveExclude = ""
	} else if c.ExcludeExt == "" {
		effectiveExclude = defaultExclude
	} else {
		effectiveExclude = c.ExcludeExt
	}
	return effectiveExclude, excludeProvided
}

// NormalizeBaseDomain derives a clean domain for subdomain extraction.
func (c *Config) NormalizeBaseDomain() string {
	baseDomain := c.URLPattern
	baseDomain = normalizeURLForCDX(baseDomain, false)
	baseDomain = strings.TrimPrefix(baseDomain, "*.")
	baseDomain = strings.TrimSuffix(baseDomain, "*")
	return strings.Trim(baseDomain, " .")
}
