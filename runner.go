package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// statsInterval is how often --stats prints a progress line.
const statsInterval = 5 * time.Second

// userAgent is sent on every CDX request. web.archive.org throttles or blocks
// the default Go client UA, so identify the tool explicitly.
const userAgent = "gowaybackgo (+github.com/OoS-MaMaD/gowaybackgo)"

// cdxBaseURL is the Wayback CDX endpoint. Held as a Runner field (defaulting
// here) so tests can point the whole pipeline at a local server.
const cdxBaseURL = "https://web.archive.org/cdx/search/cdx"

const (
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
)

// sleepCtx waits for d or until ctx is cancelled. Returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// notify surfaces a transient message. When an interactive progress bar is
// active it shows on the bar's status line; otherwise it goes to the leveled
// logger (which honors --silent). This keeps the pretty on-bar status for
// interactive runs and clean [WRN]/[ERR] lines when piped or silent.
func (r *Runner) notify(level logLevel, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if level != levelError && r.pbar != nil && r.pbar.active() {
		r.pbar.Log(msg, levelColor(level))
		return
	}
	r.log.emit(level, "%s", msg)
}

// Runner encapsulates the orchestration needed to fetch CDX pages and process results.
type Runner struct {
	cfg            *Config
	client         *http.Client
	baseURL        string // CDX endpoint; overridable in tests
	log            *logger
	color          bool // ANSI color enabled for progress/logs
	extRegex       *regexp.Regexp
	includeMode    bool
	currentPattern string // target currently being processed (per --stdin domain)
	baseDomain     string
	outFile        *os.File
	outWriter      io.Writer
	pbar           *PBar
	found          int64            // results emitted for the current target (atomic)
	rateLimiter    <-chan time.Time // nil when no rate limiting
}

// NewRunner builds a Runner with compiled filters and output writers prepared.
func NewRunner(cfg *Config) (*Runner, error) {
	extRegex, includeMode, err := CompileExtRegex(cfg.IncludeExt, cfg.EffectiveExclude())
	if err != nil {
		return nil, fmt.Errorf("compile extension regex: %w", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	// An explicit --proxy wins; otherwise the default transport already honours
	// HTTP_PROXY/HTTPS_PROXY from the environment.
	if cfg.Proxy != "" {
		pu, perr := url.Parse(cfg.Proxy)
		if perr != nil {
			return nil, fmt.Errorf("parse proxy URL %q: %w", cfg.Proxy, perr)
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
	}

	// Bar renders to /dev/tty (always a terminal), so its color depends only on
	// NO_COLOR/--nc. Logs go to stderr, so they additionally require stderr to be
	// a terminal.
	noColor := cfg.NoColor || os.Getenv("NO_COLOR") != ""
	r := &Runner{
		cfg:            cfg,
		client:         client,
		baseURL:        cdxBaseURL,
		log:            newLogger(cfg.Silent, !noColor && isTerminal(os.Stderr.Fd())),
		color:          !noColor,
		extRegex:       extRegex,
		includeMode:    includeMode,
		currentPattern: cfg.URLPattern,
		baseDomain:     baseDomainOf(cfg.URLPattern),
		outWriter:      os.Stdout,
	}

	// Set up rate limiter using a ticker channel if requested. Floor the interval
	// at 1ns so an extreme --rate (> 1e9) can't divide to a zero interval, which
	// would panic NewTicker; such a rate degrades to effectively unlimited.
	if cfg.RateLimit > 0 {
		interval := time.Second / time.Duration(cfg.RateLimit)
		if interval < 1 {
			interval = 1
		}
		r.rateLimiter = time.NewTicker(interval).C
	}

	if cfg.OutputFile != "" {
		f, err := os.Create(cfg.OutputFile)
		if err != nil {
			return nil, fmt.Errorf("create output file: %w", err)
		}
		r.outFile = f
		r.outWriter = io.MultiWriter(os.Stdout, f)
	}

	return r, nil
}

// Run executes the full fetch/process/print pipeline.
// If multiple URLList entries are set (via --stdin), each domain is processed
// sequentially so results are not interleaved.
func (r *Runner) Run(ctx context.Context) error {
	domains := r.cfg.URLList
	if len(domains) == 0 {
		domains = []string{r.cfg.URLPattern}
	}

	// The output file is opened once in NewRunner and closed once here, after
	// every domain has been processed. Closing per-domain (the old behaviour)
	// left later domains writing to a closed handle through the MultiWriter,
	// which silently dropped their output.
	defer r.closeOutput()

	var lastErr error
	failed := 0
	for _, domain := range domains {
		// Stop launching new domains once cancelled.
		if ctx.Err() != nil {
			break
		}
		// Point the run at the current domain without mutating shared Config.
		r.currentPattern = domain
		r.baseDomain = baseDomainOf(domain)
		if err := r.runSingle(ctx); err != nil {
			if ctx.Err() != nil {
				break // cancelled mid-domain: stop cleanly
			}
			// One domain failing shouldn't abandon the rest of a --stdin batch.
			r.log.errf("processing %q: %v", domain, err)
			lastErr = err
			failed++
		}
	}
	// Surface an error (non-zero exit) only when every domain failed; a partial
	// batch still exits 0 so the domains that succeeded are honored.
	if lastErr != nil && failed == len(domains) {
		return lastErr
	}
	return nil
}

func (r *Runner) runSingle(ctx context.Context) error {
	// Reset so the page-count phase (before the bar exists) logs to stderr rather
	// than through a previous domain's finished bar. found is per-target.
	r.pbar = nil
	atomic.StoreInt64(&r.found, 0)

	pages, err := r.fetchPageCount(ctx)
	if err != nil {
		return err
	}

	if pages <= 0 {
		r.log.info("no pages reported by CDX for %s; nothing to do", r.currentPattern)
		return nil
	}

	// The bar is for interactive runs; --silent and --stats suppress it.
	r.pbar = NewPBar(pages, !r.cfg.Silent && !r.cfg.Stats, r.color)
	r.pbar.Render(0)

	// Clamp defensively; validate() already rejects < 1, but this keeps a
	// directly-constructed Runner from panicking on a negative channel size.
	pageWorkers := r.cfg.PageWorkers
	if pageWorkers < 1 {
		pageWorkers = 1
	}

	// Size the jobs channel to twice the number of page workers so fetchers are
	// never blocked for long, but memory use stays bounded.
	jobsBuf := pageWorkers * 2
	if jobsBuf < 64 {
		jobsBuf = 64
	}

	pageJobs := make(chan int, pageWorkers)
	jobs := make(chan string, jobsBuf)
	resultsCh := make(chan string, jobsBuf)

	var pagesCompleted int32
	fetchWg := r.startPageFetchers(ctx, pageJobs, jobs, &pagesCompleted)
	workerWg := r.startWorkers(jobs, resultsCh)
	printWg := r.startPrinter(resultsCh, &pagesCompleted)

	// --stats prints periodic progress to stderr; stop it when the run ends.
	if r.cfg.Stats && !r.cfg.Silent {
		defer r.startStats(ctx, pages, &pagesCompleted)()
	}

	// Dispatch page numbers, but stop early if the run is cancelled. Without the
	// ctx.Done() case, a cancellation that drains all fetchers would leave this
	// send blocking forever once pageJobs fills, deadlocking shutdown.
dispatch:
	for p := 0; p < pages; p++ {
		select {
		case pageJobs <- p:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(pageJobs)

	fetchWg.Wait()
	close(jobs)
	workerWg.Wait()
	close(resultsCh)
	printWg.Wait()

	r.pbar.Finish()
	return nil
}

// startStats launches a goroutine that periodically reports progress on stderr
// (results→stdout stay clean). It returns a stop function; call it when the run
// ends. Used for --stats, typically when the interactive bar is not shown.
func (r *Runner) startStats(ctx context.Context, pages int, pagesCompleted *int32) func() {
	stop := make(chan struct{})
	start := time.Now()
	go func() {
		t := time.NewTicker(statsInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.log.info("progress: %d/%d pages, %d found, %s elapsed",
					atomic.LoadInt32(pagesCompleted), pages,
					atomic.LoadInt64(&r.found), formatDuration(time.Since(start)))
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// cdxFields returns the CDX fl= column list for the current output mode. JSON
// mode needs the extra metadata columns; every other mode only prints the URL.
func (r *Runner) cdxFields() string {
	if r.cfg.JSON {
		return "original,timestamp,statuscode,mimetype"
	}
	return "original"
}

// cdxFilters returns the CDX filter= params derived from --status/--mime.
func (r *Runner) cdxFilters() []string {
	var f []string
	if r.cfg.Status != "" {
		f = append(f, "statuscode:"+r.cfg.Status)
	}
	if r.cfg.Mime != "" {
		f = append(f, "mimetype:"+r.cfg.Mime)
	}
	return f
}

// cdxURL builds a CDX API request URL. When numPages is true it asks only for
// the page count; otherwise it requests a specific results page. from/to/status/
// mime filters apply to both so the page count matches the fetched results.
func (r *Runner) cdxURL(page int, numPages bool) string {
	v := url.Values{}
	v.Set("url", normalizeURLForCDX(r.currentPattern, r.cfg.Subs))
	if numPages {
		v.Set("showNumPages", "true")
	} else {
		v.Set("fl", r.cdxFields())
		v.Set("collapse", "urlkey")
		v.Set("page", strconv.Itoa(page))
	}
	if r.cfg.From != "" {
		v.Set("from", r.cfg.From)
	}
	if r.cfg.To != "" {
		v.Set("to", r.cfg.To)
	}
	for _, f := range r.cdxFilters() {
		v.Add("filter", f)
	}
	return r.baseURL + "?" + v.Encode()
}

func (r *Runner) fetchPageCount(ctx context.Context) (int, error) {
	// Retry like page fetches do; a transient 429/5xx on the count request must
	// not abort the domain. r.pbar is nil here, so retry messages go to stderr.
	resp, err := r.fetchWithRetry(ctx, r.cdxURL(0, true), nil)
	if err != nil {
		return 0, fmt.Errorf("fetch page count: %w", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	numStr := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			numStr = line
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read page-count response: %w", err)
	}

	if numStr == "" {
		return 0, nil
	}
	pages, err := strconv.Atoi(numStr)
	if err != nil {
		r.log.warn("could not parse page count (%s), defaulting to 1 page", sanitizeForTerminal(numStr))
		return 1, nil
	}
	return pages, nil
}

func (r *Runner) startPageFetchers(ctx context.Context, pageJobs <-chan int, jobs chan<- string, pagesCompleted *int32) *sync.WaitGroup {
	var fetchWg sync.WaitGroup
	pageConcurrency := r.cfg.PageWorkers
	if pageConcurrency < 1 {
		pageConcurrency = 1
	}

	fetchWg.Add(pageConcurrency)
	for i := 0; i < pageConcurrency; i++ {
		go func() {
			defer fetchWg.Done()
			for p := range pageJobs {
				// Honour context cancellation before dispatching each page.
				if ctx.Err() != nil {
					return
				}

				// Apply rate limiting if configured.
				if r.rateLimiter != nil {
					select {
					case <-r.rateLimiter:
					case <-ctx.Done():
						return
					}
				}

				pageURL := r.cdxURL(p, false)

				respP, ierr := r.fetchWithRetry(ctx, pageURL, pagesCompleted)
				if ierr != nil || respP == nil {
					r.notify(levelError, "fetching CDX page %d: %v", p, ierr)
					atomic.AddInt32(pagesCompleted, 1)
					r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
					continue
				}

				sc := bufio.NewScanner(respP.Body)
				sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
				for sc.Scan() {
					line := strings.TrimSpace(sc.Text())
					if line != "" {
						jobs <- line
					}
				}
				if err := sc.Err(); err != nil {
					r.notify(levelWarn, "error reading CDX page %d: %v", p, err)
					r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
				}
				respP.Body.Close()
				atomic.AddInt32(pagesCompleted, 1)
				r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
			}
		}()
	}
	return &fetchWg
}

// fetchWithRetry attempts up to cfg.Retries fetches with exponential back-off.
// It surfaces non-2xx HTTP status codes as errors and respects context
// cancellation between attempts.
func (r *Runner) fetchWithRetry(ctx context.Context, pageURL string, pagesCompleted *int32) (*http.Response, error) {
	var lastErr error
	maxRetries := r.cfg.Retries
	if maxRetries < 1 {
		maxRetries = 1
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Check context before every attempt.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err // non-retryable
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := r.client.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
			return resp, nil
		default:
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			resp.Body.Close()

			// 429 (rate limited) and 5xx are transient: long back-off then retry.
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				backoff := time.Duration(attempt*attempt) * time.Second // 1s, 4s, 9s
				r.notify(levelWarn, "HTTP %d on page fetch; backing off %s (attempt %d/%d)",
					resp.StatusCode, backoff, attempt, maxRetries)
				if !sleepCtx(ctx, backoff) {
					return nil, ctx.Err()
				}
				continue
			}
			// Other 4xx won't change on retry — fail fast.
			if resp.StatusCode >= 400 {
				return nil, lastErr
			}
		}

		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			r.notify(levelWarn, "retrying page fetch (attempt %d/%d): %v",
				attempt, maxRetries, lastErr)
			if pagesCompleted != nil && r.pbar != nil {
				r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
			}
			if !sleepCtx(ctx, backoff) {
				return nil, ctx.Err()
			}
		}
	}
	return nil, lastErr
}

func (r *Runner) startWorkers(jobs <-chan string, resultsCh chan<- string) *sync.WaitGroup {
	var workerWg sync.WaitGroup
	workerCount := r.cfg.Workers
	if workerCount < 1 {
		workerCount = 1
	}

	workerWg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workerWg.Done()
			for line := range jobs {
				for _, processed := range r.processLine(line) {
					resultsCh <- processed
				}
			}
		}()
	}
	return &workerWg
}

func (r *Runner) processLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// In JSON mode the CDX line carries several space-separated columns; the URL
	// is the first. Filter on it and pass the whole record through for printJSON.
	rawURL := line
	if r.cfg.JSON {
		if fields := strings.Fields(line); len(fields) > 0 {
			rawURL = fields[0]
		}
	}

	u, err := url.Parse(rawURL)
	path := rawURL
	if err == nil && u.Path != "" {
		path = u.Path
	}

	// Extension filter does not apply when in subdomain-only mode, to avoid
	// accidentally dropping valid subdomain URLs based on their path extension.
	if r.extRegex != nil && !r.cfg.Subs {
		match := r.extRegex.MatchString(path)
		if r.includeMode && !match {
			return nil
		} else if !r.includeMode && match {
			return nil
		}
	}

	if r.cfg.JSON {
		return []string{line}
	}

	if r.cfg.OnlyQuery {
		if err == nil && u.RawQuery != "" {
			return []string{u.RawQuery}
		}
		return nil
	}

	if r.cfg.OnlyQueryKeys {
		if err == nil && u.RawQuery != "" {
			pairs := strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == ';' })
			keys := make([]string, 0, len(pairs))
			for _, p := range pairs {
				if p == "" {
					continue
				}
				k := p
				if idx := strings.Index(p, "="); idx >= 0 {
					k = p[:idx]
				}
				if k == "" {
					continue
				}
				if un, err := url.QueryUnescape(k); err == nil {
					k = un
				}
				keys = append(keys, k)
			}
			return keys
		}
		return nil
	}

	if r.cfg.NoQuery && err == nil {
		u.RawQuery = ""
		return []string{u.String()}
	}

	return []string{line}
}

func (r *Runner) startPrinter(resultsCh <-chan string, pagesCompleted *int32) *sync.WaitGroup {
	var printWg sync.WaitGroup
	printWg.Add(1)

	go func() {
		defer printWg.Done()
		bufw := bufio.NewWriter(r.outWriter)

		if r.cfg.JSON {
			r.printJSON(bufw, resultsCh, pagesCompleted)
			return
		}

		if r.cfg.Subs {
			r.printSubdomains(bufw, resultsCh, pagesCompleted)
			return
		}

		if r.cfg.ExtractPaths {
			r.printPaths(bufw, resultsCh, pagesCompleted)
			return
		}

		r.printDefault(bufw, resultsCh, pagesCompleted)
	}()

	return &printWg
}

// jsonRecord is one JSONL output line. Empty metadata fields are omitted.
type jsonRecord struct {
	URL       string `json:"url"`
	Timestamp string `json:"timestamp,omitempty"`
	Status    string `json:"status,omitempty"`
	Mime      string `json:"mime,omitempty"`
}

// parseCDXRecord turns a CDX line (columns original,timestamp,statuscode,
// mimetype) into a jsonRecord. Untrusted string fields are sanitized; CDX uses
// "-" for a missing value, which is dropped. Returns ok=false for blank lines.
func parseCDXRecord(line string) (jsonRecord, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return jsonRecord{}, false
	}
	rec := jsonRecord{URL: sanitizeForTerminal(fields[0])}
	get := func(i int) string {
		if i < len(fields) && fields[i] != "-" {
			return sanitizeForTerminal(fields[i])
		}
		return ""
	}
	rec.Timestamp = get(1)
	rec.Status = get(2)
	rec.Mime = get(3)
	return rec, rec.URL != ""
}

func (r *Runner) printJSON(bufw *bufio.Writer, resultsCh <-chan string, pagesCompleted *int32) {
	enc := json.NewEncoder(bufw)
	enc.SetEscapeHTML(false)
	seen := make(map[string]struct{})
	for res := range resultsCh {
		rec, ok := parseCDXRecord(res)
		if !ok {
			continue
		}
		if _, dup := seen[rec.URL]; dup {
			continue
		}
		seen[rec.URL] = struct{}{}
		r.pbar.ClearLine()
		enc.Encode(rec) // Encode appends a newline, giving JSONL output
		bufw.Flush()    // stream each record live rather than buffering
		atomic.AddInt64(&r.found, 1)
		r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
	}
	r.finishOutput(bufw)
}

func (r *Runner) printSubdomains(bufw *bufio.Writer, resultsCh <-chan string, pagesCompleted *int32) {
	if r.baseDomain == "" {
		return
	}
	seenSubs := make(map[string]struct{})
	baseLower := strings.ToLower(r.baseDomain)
	for res := range resultsCh {
		u, err := url.Parse(res)
		if err != nil {
			continue
		}
		host := u.Host
		if idx := strings.Index(host, ":"); idx >= 0 {
			host = host[:idx]
		}
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || host == baseLower {
			continue
		}
		if strings.HasSuffix(host, "."+baseLower) {
			if _, ok := seenSubs[host]; ok {
				continue
			}
			seenSubs[host] = struct{}{}
			r.writeWithProgress(bufw, host, pagesCompleted)
		}
	}
	r.finishOutput(bufw)
}

func (r *Runner) printPaths(bufw *bufio.Writer, resultsCh <-chan string, pagesCompleted *int32) {
	seenSeg := make(map[string]struct{})
	for res := range resultsCh {
		u, err := url.Parse(res)
		if err != nil || u.Path == "" {
			continue
		}
		segs := strings.Split(u.Path, "/")
		for _, seg := range segs {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			if _, ok := seenSeg[seg]; ok {
				continue
			}
			seenSeg[seg] = struct{}{}
			r.writeWithProgress(bufw, seg, pagesCompleted)
		}
	}
	r.finishOutput(bufw)
}

func (r *Runner) printDefault(bufw *bufio.Writer, resultsCh <-chan string, pagesCompleted *int32) {
	seen := make(map[string]struct{})
	for res := range resultsCh {
		if _, ok := seen[res]; ok {
			continue
		}
		seen[res] = struct{}{}
		r.writeWithProgress(bufw, res, pagesCompleted)
	}
	r.finishOutput(bufw)
}

func (r *Runner) writeWithProgress(bufw *bufio.Writer, value string, pagesCompleted *int32) {
	r.pbar.ClearLine()
	fmt.Fprintln(bufw, sanitizeForTerminal(value))
	// Flush each result so output streams live (pipeable, watchable) instead of
	// sitting in the buffer until it fills or the run ends. A persistent write
	// error is surfaced once by finishOutput; a per-line error is ignored here.
	bufw.Flush()
	atomic.AddInt64(&r.found, 1)
	r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
}

// finishOutput flushes the per-domain buffered writer. The underlying file is
// left open so subsequent domains can keep appending; it is closed once by
// closeOutput after the whole run completes. A flush error (e.g. disk full or a
// closed pipe) is surfaced rather than silently dropped.
func (r *Runner) finishOutput(bufw *bufio.Writer) {
	if err := bufw.Flush(); err != nil {
		r.log.warn("error writing output: %v", err)
	}
}

// closeOutput closes the output file once, at the end of the run, and reports
// where results were saved (to stderr, so stdout stays a clean result stream).
// Safe to call when no output file is configured.
func (r *Runner) closeOutput() {
	if r.outFile != nil {
		r.outFile.Close()
		r.outFile = nil
		r.log.info("saved results to %s", r.cfg.OutputFile)
	}
}
