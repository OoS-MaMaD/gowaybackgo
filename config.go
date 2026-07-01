package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config collects all CLI options for the tool.
type Config struct {
	URLPattern      string
	URLList         []string // domains read from stdin when --stdin is set
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

const banner = `
  __ _  _____      ____ _ _   _| |__   __ _  ___| | ____ _  ___
 / _` + "`" + ` |/ _ \ \ /\ / / _` + "`" + ` | | | | '_ \ / _` + "`" + ` |/ __| |/ / _` + "`" + ` |/ _ \
| (_| | (_) \ V  V / (_| | |_| | |_) | (_| | (__|   < (_| | (_) |
 \__, |\___/ \_/\_/ \__,_|\__, |_.__/ \__,_|\___|_|\_\__, |\___/
  __/ |                    __/ |                      __/ |
 |___/                    |___/                      |___/

  Wayback Machine CDX URL fetcher — with more options
  github.com/OoS-MaMaD/gowaybackgo
`

func printUsage() {
	fmt.Fprint(os.Stderr, banner)
	fmt.Fprintln(os.Stderr, "USAGE")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u <target> [options]")
	fmt.Fprintln(os.Stderr, "  cat domains.txt | gowaybackgo --stdin [options]")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "TARGET")
	fmt.Fprintln(os.Stderr, "  -u <pattern>          Target URL or domain pattern")
	fmt.Fprintln(os.Stderr, "                          e.g.  example.com")
	fmt.Fprintln(os.Stderr, "                                example.com/api/v1")
	fmt.Fprintln(os.Stderr, "                                *.example.com")
	fmt.Fprintln(os.Stderr, "  --stdin               Read targets from stdin (one per line, # = comment)")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "OUTPUT")
	fmt.Fprintln(os.Stderr, "  -o <file>             Write results to file (also prints to stdout)")
	fmt.Fprintln(os.Stderr, "  --only-query          Print full query strings only   e.g. foo=1&bar=2")
	fmt.Fprintln(os.Stderr, "  --only-query-keys     Print query parameter keys only e.g. foo, bar")
	fmt.Fprintln(os.Stderr, "  --no-query            Strip query strings from output URLs")
	fmt.Fprintln(os.Stderr, "  --extract-paths       Print unique path segments (one per line)")
	fmt.Fprintln(os.Stderr, "  --subs                Print unique subdomains of the target domain")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "FILTERING")
	fmt.Fprintln(os.Stderr, "  --exclude-ext <exts>  Comma-separated extensions to exclude (e.g. js,css,png)")
	fmt.Fprintln(os.Stderr, "  --exclude-defaults    Exclude common static file extensions:")
	fmt.Fprintln(os.Stderr, "                          js css png jpg jpeg gif svg webp ico bmp tif tiff")
	fmt.Fprintln(os.Stderr, "                          woff woff2 ttf eot mp4 mp3 wav avi mov mkv zip rar 7z pdf")
	fmt.Fprintln(os.Stderr, "  --include-ext <exts>  Only include these extensions (overrides exclude)")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "PERFORMANCE")
	fmt.Fprintln(os.Stderr, "  --rate <n>            Max CDX requests/sec (default: 0 = unlimited)")
	fmt.Fprintln(os.Stderr, "                          Recommended: 5-10 to avoid hitting rate limits")
	fmt.Fprintln(os.Stderr, "  --page-workers <n>    Concurrent CDX page fetchers   (default: 10)")
	fmt.Fprintln(os.Stderr, "  --workers <n>         Concurrent URL processors      (default: 20)")
	fmt.Fprintln(os.Stderr, "  --timeout <sec>       HTTP timeout in seconds         (default: 80)")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "EXAMPLES")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com --exclude-defaults -o urls.txt")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com --subs")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com --only-query-keys")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com/api --include-ext json,xml")
	fmt.Fprintln(os.Stderr, "  gowaybackgo -u example.com --rate 5 --page-workers 5")
	fmt.Fprintln(os.Stderr, "  cat domains.txt | gowaybackgo --stdin --exclude-defaults")
	fmt.Fprintln(os.Stderr, "")
}

// ParseConfig reads command-line flags into a Config struct.
func ParseConfig() (*Config, error) {
	flag.Usage = printUsage

	urlFlag := flag.String("u", "", "")
	stdinFlag := flag.Bool("stdin", false, "")
	outputFile := flag.String("o", "", "")
	onlyQuery := flag.Bool("only-query", false, "")
	onlyQueryKeys := flag.Bool("only-query-keys", false, "")
	noQuery := flag.Bool("no-query", false, "")
	excludeExt := flag.String("exclude-ext", "", "")
	excludeDefaults := flag.Bool("exclude-defaults", false, "")
	includeExt := flag.String("include-ext", "", "")
	workers := flag.Int("workers", 20, "")
	extractPaths := flag.Bool("extract-paths", false, "")
	subs := flag.Bool("subs", false, "")
	pageWorkers := flag.Int("page-workers", 10, "")
	timeout := flag.Int("timeout", 80, "")
	rateLimit := flag.Int("rate", 0, "")
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
		return nil, fmt.Errorf("-u <url> is required (or use --stdin)")
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
