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
	JSON            bool // emit one JSON object per line (JSONL) instead of plain text
	Timeout         time.Duration
	RateLimit       int    // max CDX page requests per second (0 = unlimited)
	From            string // CDX from= timestamp filter (yyyy[MMdd[hhmmss]])
	To              string // CDX to= timestamp filter (yyyy[MMdd[hhmmss]])
	Status          string // CDX statuscode filter (e.g. 200, 2.., (200|301))
	Mime            string // CDX mimetype filter (e.g. text/html, application/json)
	Proxy           string // HTTP/HTTPS/SOCKS5 proxy URL; empty falls back to env

	// excludeFlagSet records whether --exclude-ext was passed on the command
	// line, captured at parse time so EffectiveExclude does not depend on the
	// global flag package state at call time.
	excludeFlagSet bool
}

const logo = `  __ _  _____      ____ _ _   _| |__   __ _  ___| | ____ _  ___
 / _` + "`" + ` |/ _ \ \ /\ / / _` + "`" + ` | | | | '_ \ / _` + "`" + ` |/ __| |/ / _` + "`" + ` |/ _ \
| (_| | (_) \ V  V / (_| | |_| | |_) | (_| | (__|   < (_| | (_) |
 \__, |\___/ \_/\_/ \__,_|\__, |_.__/ \__,_|\___|_|\_\__, |\___/
  __/ |                    __/ |                      __/ |
 |___/                    |___/                      |___/`

// usagePalette holds ANSI codes for the help output. All fields are empty when
// color is disabled (NO_COLOR set, or stderr is not a terminal).
type usagePalette struct{ reset, bold, dim, cyan, green string }

func newUsagePalette() usagePalette {
	if os.Getenv("NO_COLOR") != "" || !isTerminal(os.Stderr.Fd()) {
		return usagePalette{}
	}
	return usagePalette{
		reset: "\033[0m", bold: "\033[1m", dim: "\033[2m",
		cyan: "\033[36m", green: "\033[32m",
	}
}

func printUsage() {
	w := os.Stderr
	p := newUsagePalette()

	head := func(title string) {
		fmt.Fprintf(w, "\n%s%s%s%s\n", p.bold, p.cyan, title, p.reset)
	}
	// row prints a flag and its description, aligned in a fixed column.
	row := func(name, desc string) {
		fmt.Fprintf(w, "  %s%-21s%s %s\n", p.green, name, p.reset, desc)
	}
	// cont prints a dimmed continuation line under the description column.
	cont := func(text string) {
		fmt.Fprintf(w, "  %-21s %s%s%s\n", "", p.dim, text, p.reset)
	}
	ex := func(cmd string) {
		fmt.Fprintf(w, "  %s$%s %s\n", p.dim, p.reset, cmd)
	}

	// Logo with the version shown small (dimmed) beside the tagline.
	fmt.Fprintf(w, "\n%s%s%s\n\n", p.cyan, logo, p.reset)
	fmt.Fprintf(w, "  %sWayback Machine CDX URL fetcher%s  %s%s%s\n", p.bold, p.reset, p.dim, version, p.reset)
	fmt.Fprintf(w, "  %sgithub.com/OoS-MaMaD/gowaybackgo%s\n", p.dim, p.reset)

	head("USAGE")
	ex("gowaybackgo -u <target> [options]")
	ex("cat domains.txt | gowaybackgo --stdin [options]")

	head("TARGET")
	row("-u <pattern>", "Target URL or domain pattern")
	cont("e.g.  example.com   example.com/api/v1   *.example.com")
	row("--stdin", "Read targets from stdin (one per line, # = comment)")

	head("OUTPUT  (modes are mutually exclusive)")
	row("-o <file>", "Write results to file (also prints to stdout)")
	row("--only-query", "Print full query strings only    e.g. foo=1&bar=2")
	row("--only-query-keys", "Print query parameter keys only  e.g. foo, bar")
	row("--no-query", "Strip query strings from output URLs")
	row("--extract-paths", "Print unique path segments (one per line)")
	row("--subs", "Print unique subdomains of the target domain")
	row("--json", `Emit JSONL: {"url","timestamp","status","mime"}`)

	head("FILTERING")
	row("--exclude-ext <exts>", "Comma-separated extensions to exclude (e.g. js,css,png)")
	row("--exclude-defaults", "Exclude common static file extensions:")
	cont("js css png jpg jpeg gif svg webp ico bmp tif tiff")
	cont("woff woff2 ttf eot mp4 mp3 wav avi mov mkv")
	cont("zip rar 7z pdf")
	row("--include-ext <exts>", "Only include these extensions (overrides exclude)")
	row("--from <ts>", "Only captures at/after this time (yyyy[MMdd[hhmmss]])")
	row("--to <ts>", "Only captures at/before this time (yyyy[MMdd[hhmmss]])")
	row("--status <re>", "CDX statuscode filter   e.g. 200  2..  (200|301)")
	row("--mime <re>", "CDX mimetype filter     e.g. text/html  application/json")

	head("PERFORMANCE")
	row("--rate <n>", "Max CDX requests/sec (default: 0 = unlimited)")
	cont("recommended 5-10 to avoid hitting rate limits")
	row("--page-workers <n>", "Concurrent CDX page fetchers   (default: 10)")
	row("--workers <n>", "Concurrent URL processors      (default: 20)")
	row("--timeout <sec>", "HTTP timeout in seconds        (default: 80)")
	row("--proxy <url>", "Route via http/https/socks5 proxy")
	cont("falls back to HTTP_PROXY/HTTPS_PROXY env if unset")

	head("MISC")
	row("--version", "Print version and exit")
	row("-h, --help", "Show this help")

	head("EXAMPLES")
	ex("gowaybackgo -u example.com --exclude-defaults -o urls.txt")
	ex("gowaybackgo -u example.com --subs")
	ex("gowaybackgo -u example.com/api --include-ext json,xml")
	ex("gowaybackgo -u example.com --json --status 200 --mime text/html")
	ex("gowaybackgo -u example.com --from 2020 --to 2022 -o urls.txt")
	ex("gowaybackgo -u example.com --proxy http://127.0.0.1:8080")
	ex("cat domains.txt | gowaybackgo --stdin --exclude-defaults")
	fmt.Fprintln(w)
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
	jsonOut := flag.Bool("json", false, "")
	from := flag.String("from", "", "")
	to := flag.String("to", "", "")
	status := flag.String("status", "", "")
	mime := flag.String("mime", "", "")
	proxy := flag.String("proxy", "", "")
	versionFlag := flag.Bool("version", false, "")
	flag.Parse()

	if *versionFlag {
		fmt.Println("gowaybackgo", version)
		os.Exit(0)
	}

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
		JSON:            *jsonOut,
		Timeout:         time.Duration(*timeout) * time.Second,
		RateLimit:       *rateLimit,
		From:            strings.TrimSpace(*from),
		To:              strings.TrimSpace(*to),
		Status:          strings.TrimSpace(*status),
		Mime:            strings.TrimSpace(*mime),
		Proxy:           strings.TrimSpace(*proxy),
	}
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "exclude-ext" {
			cfg.excludeFlagSet = true
		}
	})

	if err := cfg.validate(); err != nil {
		return nil, err
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

// EffectiveExclude returns the active exclusion list following user flags:
// --exclude-defaults (or --exclude-ext with an empty value) selects the default
// list; --exclude-ext with a value selects that value; otherwise nothing is
// excluded.
func (c *Config) EffectiveExclude() string {
	switch {
	case c.ExcludeDefaults:
		return defaultExclude
	case !c.excludeFlagSet:
		return ""
	case c.ExcludeExt == "":
		return defaultExclude
	default:
		return c.ExcludeExt
	}
}

// NormalizeBaseDomain derives a clean domain for subdomain extraction.
func (c *Config) NormalizeBaseDomain() string {
	return baseDomainOf(c.URLPattern)
}

// baseDomainOf derives a clean base domain from a target pattern. Kept as a
// standalone function so callers can normalize an arbitrary pattern (e.g. the
// current --stdin domain) without mutating shared Config state.
func baseDomainOf(pattern string) string {
	baseDomain := normalizeURLForCDX(pattern, false)
	baseDomain = strings.TrimPrefix(baseDomain, "*.")
	baseDomain = strings.TrimSuffix(baseDomain, "*")
	return strings.Trim(baseDomain, " .")
}

// validate rejects mutually exclusive output modes and warns about
// combinations that are silently ignored, so users get a clear error up front
// instead of surprising output.
func (c *Config) validate() error {
	type mode struct {
		set  bool
		flag string
	}
	exclusive := []mode{
		{c.OnlyQuery, "--only-query"},
		{c.OnlyQueryKeys, "--only-query-keys"},
		{c.ExtractPaths, "--extract-paths"},
		{c.Subs, "--subs"},
		{c.JSON, "--json"},
	}
	var active []string
	for _, m := range exclusive {
		if m.set {
			active = append(active, m.flag)
		}
	}
	if len(active) > 1 {
		return fmt.Errorf("only one output mode may be set, but got %s", strings.Join(active, ", "))
	}

	// Numeric flags: reject values that are nonsensical (and would otherwise
	// panic later, e.g. a negative channel size or a zero ticker interval).
	if c.Workers < 1 {
		return fmt.Errorf("--workers must be >= 1, got %d", c.Workers)
	}
	if c.PageWorkers < 1 {
		return fmt.Errorf("--page-workers must be >= 1, got %d", c.PageWorkers)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("--timeout must be >= 1 second")
	}
	if c.RateLimit < 0 {
		return fmt.Errorf("--rate must be >= 0 (0 = unlimited), got %d", c.RateLimit)
	}

	// --no-query is a transform on default output; it does nothing under the
	// exclusive modes above. Warn rather than fail.
	if c.NoQuery && len(active) == 1 {
		fmt.Fprintf(os.Stderr, "⚠ WARNING: --no-query is ignored with %s\n", active[0])
	}
	if strings.TrimSpace(c.IncludeExt) != "" && strings.TrimSpace(c.ExcludeExt) != "" {
		fmt.Fprintln(os.Stderr, "⚠ WARNING: --include-ext takes precedence; --exclude-ext is ignored")
	}
	return nil
}
