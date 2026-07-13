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

const maxRetries = 3

// userAgent is sent on every CDX request. web.archive.org throttles or blocks
// the default Go client UA, so identify the tool explicitly.
const userAgent = "gowaybackgo (+github.com/OoS-MaMaD/gowaybackgo)"

// Runner encapsulates the orchestration needed to fetch CDX pages and process results.
type Runner struct {
	cfg            *Config
	client         *http.Client
	extRegex       *regexp.Regexp
	includeMode    bool
	currentPattern string // target currently being processed (per --stdin domain)
	baseDomain     string
	outFile        *os.File
	outWriter      io.Writer
	pbar           *PBar
	rateLimiter    <-chan time.Time // nil when no rate limiting
}

// NewRunner builds a Runner with compiled filters and output writers prepared.
func NewRunner(cfg *Config) (*Runner, error) {
	effectiveExclude, _ := cfg.EffectiveExclude()
	extRegex, includeMode, err := CompileExtRegex(cfg.IncludeExt, effectiveExclude)
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

	r := &Runner{
		cfg:            cfg,
		client:         client,
		extRegex:       extRegex,
		includeMode:    includeMode,
		currentPattern: cfg.URLPattern,
		baseDomain:     baseDomainOf(cfg.URLPattern),
		outWriter:      os.Stdout,
	}

	// Set up rate limiter using a ticker channel if requested.
	if cfg.RateLimit > 0 {
		ticker := time.NewTicker(time.Second / time.Duration(cfg.RateLimit))
		r.rateLimiter = ticker.C
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

	for _, domain := range domains {
		// Stop launching new domains once cancelled.
		if ctx.Err() != nil {
			break
		}
		// Point the run at the current domain without mutating shared Config.
		r.currentPattern = domain
		r.baseDomain = baseDomainOf(domain)
		if err := r.runSingle(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) runSingle(ctx context.Context) error {
	pages, err := r.fetchPageCount(ctx)
	if err != nil {
		return err
	}

	if pages <= 0 {
		fmt.Fprintln(os.Stderr, "No pages reported by CDX; nothing to do.")
		return nil
	}

	r.pbar = NewPBar(pages)
	r.pbar.Render(0)

	// Size the jobs channel to twice the number of page workers so fetchers are
	// never blocked for long, but memory use stays bounded.
	jobsBuf := r.cfg.PageWorkers * 2
	if jobsBuf < 64 {
		jobsBuf = 64
	}

	pageJobs := make(chan int, r.cfg.PageWorkers)
	jobs := make(chan string, jobsBuf)
	resultsCh := make(chan string, jobsBuf)

	var pagesCompleted int32
	fetchWg := r.startPageFetchers(ctx, pageJobs, jobs, &pagesCompleted)
	workerWg := r.startWorkers(jobs, resultsCh)
	printWg := r.startPrinter(resultsCh, &pagesCompleted)

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
	return "https://web.archive.org/cdx/search/cdx?" + v.Encode()
}

func (r *Runner) fetchPageCount(ctx context.Context) (int, error) {
	pagesURL := r.cdxURL(0, true)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pagesURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build page count request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch page count: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("CDX page count returned HTTP %d", resp.StatusCode)
	}

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
		fmt.Fprintln(os.Stderr, "⚠ WARNING: could not parse page count (", numStr, "), defaulting to 1 page")
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
					msg := fmt.Sprintf("❌ ERROR fetching CDX page %d: %v", p, ierr)
					r.pbar.Log(msg, "\033[31m")
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
					msg := fmt.Sprintf("⚠ WARNING: error reading CDX page %d: %v", p, err)
					r.pbar.Log(msg, "\033[33m")
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

// fetchWithRetry attempts up to maxRetries fetches with exponential back-off.
// It surfaces non-2xx HTTP status codes as errors and respects context
// cancellation between attempts.
func (r *Runner) fetchWithRetry(ctx context.Context, pageURL string, pagesCompleted *int32) (*http.Response, error) {
	var lastErr error

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
		if err != nil {
			lastErr = err
		} else if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return resp, nil
		} else {
			// Treat non-2xx as a retryable error; surface the status code clearly.
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			resp.Body.Close()

			// For 429 (rate limited) or 5xx use a longer back-off.
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				backoff := time.Duration(attempt*attempt) * time.Second // 1s, 4s, 9s
				msg := fmt.Sprintf("⚠ HTTP %d on page fetch; backing off %s (attempt %d/%d)",
					resp.StatusCode, backoff, attempt, maxRetries)
				r.pbar.Log(msg, "\033[33m")
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
		}

		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			msg := fmt.Sprintf("⚠ retrying page fetch (attempt %d/%d): %v", attempt, maxRetries, lastErr)
			r.pbar.Log(msg, "\033[33m")
			if pagesCompleted != nil {
				r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
			}
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
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
	r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
}

// finishOutput flushes the per-domain buffered writer. The underlying file is
// left open so subsequent domains can keep appending; it is closed once by
// closeOutput after the whole run completes.
func (r *Runner) finishOutput(bufw *bufio.Writer) {
	bufw.Flush()
}

// closeOutput closes the output file once, at the end of the run, and reports
// where results were saved. Safe to call when no output file is configured.
func (r *Runner) closeOutput() {
	if r.outFile != nil {
		r.outFile.Close()
		r.outFile = nil
		fmt.Fprintln(os.Stdout, "✔ Saved results to", r.cfg.OutputFile)
	}
}
