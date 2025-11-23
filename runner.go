package main

import (
	"bufio"
	"context"
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

// Runner encapsulates the orchestration needed to fetch CDX pages and process results.
type Runner struct {
	cfg         *Config
	client      *http.Client
	extRegex    *regexp.Regexp
	includeMode bool
	baseDomain  string
	outFile     *os.File
	outWriter   io.Writer
	pbar        *PBar
}

// NewRunner builds a Runner with compiled filters and output writers prepared.
func NewRunner(cfg *Config) (*Runner, error) {
	effectiveExclude, _ := cfg.EffectiveExclude()
	extRegex, includeMode, err := CompileExtRegex(cfg.IncludeExt, effectiveExclude)
	if err != nil {
		return nil, fmt.Errorf("compile extension regex: %w", err)
	}

	r := &Runner{
		cfg:         cfg,
		client:      &http.Client{Timeout: cfg.Timeout},
		extRegex:    extRegex,
		includeMode: includeMode,
		baseDomain:  cfg.NormalizeBaseDomain(),
		outWriter:   os.Stdout,
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
func (r *Runner) Run(ctx context.Context) error {
	pages, err := r.fetchPageCount(ctx)
	if err != nil {
		return err
	}

	if pages == 0 {
		fmt.Fprintln(os.Stderr, "No pages reported by CDX; nothing to do.")
		r.closeOutput()
		return nil
	}

	r.pbar = NewPBar(pages)
	r.pbar.Render(0)

	pageJobs := make(chan int, r.cfg.PageWorkers)
	jobs := make(chan string, 2000)
	resultsCh := make(chan string, 2000)

	var pagesCompleted int32
	fetchWg := r.startPageFetchers(ctx, pageJobs, jobs, &pagesCompleted)
	workerWg := r.startWorkers(jobs, resultsCh)

	printWg := r.startPrinter(resultsCh, &pagesCompleted)

	for p := 0; p < pages; p++ {
		pageJobs <- p
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

func (r *Runner) fetchPageCount(ctx context.Context) (int, error) {
	pagesURL := "http://web.archive.org/cdx/search/cdx?url=" + url.QueryEscape(normalizeURLForCDX(r.cfg.URLPattern, r.cfg.Subs)) + "&showNumPages=true"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pagesURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build page count request: %w", err)
	}

	resp, err := r.client.Do(req)
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
				pageURL := "https://web.archive.org/cdx/search/cdx?url=" + url.QueryEscape(normalizeURLForCDX(r.cfg.URLPattern, r.cfg.Subs)) + "&page=" + strconv.Itoa(p) + "&fl=original&collapse=urlkey"

				if ctx.Err() != nil {
					return
				}

				respP, ierr := r.fetchWithRetry(ctx, pageURL, pagesCompleted)
				if ierr != nil || respP == nil {
					msg := fmt.Sprintf("❌ ERROR fetching CDX page %d: %v", p, ierr)
					r.pbar.Log(msg, "\033[31m")
					atomic.AddInt32(pagesCompleted, 1)
					r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
					continue
				}

				sc := bufio.NewScanner(respP.Body)
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

func (r *Runner) fetchWithRetry(ctx context.Context, pageURL string, pagesCompleted *int32) (*http.Response, error) {
	var respP *http.Response
	var ierr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			ierr = err
			break
		}

		respP, ierr = r.client.Do(req)
		if ierr == nil && respP != nil && respP.StatusCode >= http.StatusOK && respP.StatusCode < http.StatusMultipleChoices {
			return respP, nil
		}

		if respP != nil {
			respP.Body.Close()
		}
		msg := fmt.Sprintf("⚠ retrying page fetch after error: %v", ierr)
		r.pbar.Log(msg, "\033[33m")
		if pagesCompleted != nil {
			r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return nil, ierr
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

	u, err := url.Parse(line)
	path := line
	if err == nil && u.Path != "" {
		path = u.Path
	}

	if r.extRegex != nil {
		match := r.extRegex.MatchString(path)
		if r.includeMode && !match {
			return nil
		} else if !r.includeMode && match {
			return nil
		}
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
	fmt.Fprintln(bufw, value)
	bufw.Flush()
	r.pbar.Render(int(atomic.LoadInt32(pagesCompleted)))
}

func (r *Runner) finishOutput(bufw *bufio.Writer) {
	if r.outFile != nil {
		bufw.Flush()
		r.outFile.Close()
		fmt.Fprintln(os.Stdout, "✔ Saved results to", r.cfg.OutputFile)
	}
}

func (r *Runner) closeOutput() {
	if r.outFile != nil {
		r.outFile.Close()
	}
}
