package main

import (
	"bufio"
	"flag"
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

const defaultExclude = "js,css,png,jpg,jpeg,gif,svg,webp,ico,bmp,tif,tiff,woff,woff2,ttf,eot,mp4,mp3,wav,avi,mov,mkv,zip,rar,7z,pdf"

func main() {
	// Flags
	urlFlag := flag.String("u", "", "Target URL pattern (e.g. *.example.com)")
	outputFile := flag.String("o", "", "Output file (also prints to stdout)")
	onlyQuery := flag.Bool("only-query", false, "Output only full query strings")
	onlyQueryKeys := flag.Bool("only-query-keys", false, "Output only query parameter keys")
	noQuery := flag.Bool("no-query", false, "Remove query strings from URLs")
	excludeExt := flag.String("exclude-ext", defaultExclude, "Comma-separated list of extensions to exclude")
	includeExt := flag.String("include-ext", "", "Comma-separated list of extensions to include (overrides exclude)")
	workers := flag.Int("workers", 20, "Number of concurrent processing workers (for URL lines)")
	pageWorkers := flag.Int("page-workers", 5, "Number of concurrent page fetchers (CDX pages)")
	timeout := flag.Int("timeout", 80, "HTTP timeout in seconds")
	flag.Parse()

	if *urlFlag == "" {
		fmt.Fprintln(os.Stderr, "❌ ERROR: -u <url> is required")
		flag.Usage()
		os.Exit(1)
	}

	client := &http.Client{Timeout: time.Duration(*timeout) * time.Second}

	// 1) Request number of pages
	pagesURL := "http://web.archive.org/cdx/search/cdx?url=" + url.QueryEscape(normalizeURL(*urlFlag)) + "&showNumPages=true"
	resp, err := client.Get(pagesURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR fetching page count from CDX:", err)
		os.Exit(1)
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
	resp.Body.Close()
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR reading page-count response:", err)
		os.Exit(1)
	}

	pages := 0
	if numStr == "" {
		pages = 0
	} else {
		if n, err := strconv.Atoi(numStr); err == nil {
			pages = n
		} else {
			fmt.Fprintln(os.Stderr, "⚠ WARNING: could not parse page count (", numStr, "), defaulting to 1 page")
			pages = 1
		}
	}

	// Compile extension filters
	extRegex, includeMode, err := CompileExtRegex(*includeExt, *excludeExt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ ERROR compiling extension regex:", err)
		os.Exit(1)
	}

	// Prepare output writer
	var outFile *os.File
	var outWriter io.Writer = os.Stdout
	if *outputFile != "" {
		f, err := os.Create(*outputFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "❌ ERROR creating output file:", err)
			os.Exit(1)
		}
		outFile = f
		outWriter = io.MultiWriter(os.Stdout, outFile)
	}

	if pages == 0 {
		// nothing to fetch; exit gracefully
		fmt.Fprintln(os.Stderr, "No pages reported by CDX; nothing to do.")
		if outFile != nil {
			outFile.Close()
		}
		return
	}

	// Create progress bar
	pbar := NewPBar(pages)
	pbar.Render(0)

	// Channels
	pageJobs := make(chan int, *pageWorkers)
	jobs := make(chan string, 2000)
	resultsCh := make(chan string, 2000)

	var pagesCompleted int32 = 0

	// Page fetchers
	var fetchWg sync.WaitGroup
	pageConcurrency := *pageWorkers
	if pageConcurrency < 1 {
		pageConcurrency = 1
	}
	if pages < pageConcurrency {
		pageConcurrency = pages
	}
	maxRetries := 3
	for i := 0; i < pageConcurrency; i++ {
		fetchWg.Add(1)
		go func() {
			defer fetchWg.Done()
			for p := range pageJobs {
				pageURL := "https://web.archive.org/cdx/search/cdx?url=" + url.QueryEscape(normalizeURL(*urlFlag)) + "&page=" + strconv.Itoa(p) + "&fl=original&collapse=urlkey"
				var respP *http.Response
				var ierr error
				for attempt := 1; attempt <= maxRetries; attempt++ {
					respP, ierr = client.Get(pageURL)
					if ierr == nil && respP != nil && respP.StatusCode >= 200 && respP.StatusCode < 300 {
						break
					}
					if respP != nil {
						respP.Body.Close()
					}
					// Print retry message (show as status on the progress bar so stdout isn't interrupted)
					msg := fmt.Sprintf("⚠ retrying page %d (attempt %d) after error: %v", p, attempt, ierr)
					pbar.Log(msg, "\033[33m")
					pbar.Render(int(atomic.LoadInt32(&pagesCompleted)))
					// backoff
					time.Sleep(time.Duration(attempt) * time.Second)
				}
				if ierr != nil || respP == nil {
					msg := fmt.Sprintf("❌ ERROR fetching CDX page %d: %v", p, ierr)
					pbar.Log(msg, "\033[31m")
					atomic.AddInt32(&pagesCompleted, 1)
					pbar.Render(int(atomic.LoadInt32(&pagesCompleted)))
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
					pbar.Log(msg, "\033[33m")
					pbar.Render(int(atomic.LoadInt32(&pagesCompleted)))
				}
				respP.Body.Close()
				atomic.AddInt32(&pagesCompleted, 1)
				pbar.Render(int(atomic.LoadInt32(&pagesCompleted)))
			}
		}()
	}

	// Processing workers
	var workerWg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			for line := range jobs {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				u, err := url.Parse(line)
				path := line
				if err == nil && u.Path != "" {
					path = u.Path
				}

				// extension filtering
				if extRegex != nil {
					match := extRegex.MatchString(path)
					if includeMode && !match {
						continue
					} else if !includeMode && match {
						continue
					}
				}

				// query modes
				if *onlyQuery {
					if err == nil && u.RawQuery != "" {
						resultsCh <- u.RawQuery
					}
					continue
				}
				if *onlyQueryKeys {
					if err == nil && u.RawQuery != "" {
						pairs := strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == ';' })
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
							resultsCh <- k
						}
					}
					continue
				}
				if *noQuery && err == nil {
					u.RawQuery = ""
					resultsCh <- u.String()
					continue
				}

				// default
				resultsCh <- line
			}
		}()
	}

	// Printer goroutine: dedupe and write immediately; keep progress bar at bottom
	var printWg sync.WaitGroup
	printWg.Add(1)
	go func() {
		defer printWg.Done()
		seen := make(map[string]struct{})
		bufw := bufio.NewWriter(outWriter)
		for r := range resultsCh {
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			// clear progress bar, print the data line, flush and redraw bar
			pbar.ClearLine()
			fmt.Fprintln(bufw, r)
			bufw.Flush()
			pbar.Render(int(atomic.LoadInt32(&pagesCompleted)))
		}
		if outFile != nil {
			bufw.Flush()
			outFile.Close()
			fmt.Fprintln(os.Stdout, "✔ Saved results to", *outputFile)
		}
	}()

	// Dispatch page numbers
	for p := 0; p < pages; p++ {
		pageJobs <- p
	}
	close(pageJobs)

	// Wait for fetchers to finish
	fetchWg.Wait()

	// Close jobs and wait for workers
	close(jobs)
	workerWg.Wait()

	// Close results and wait for printer
	close(resultsCh)
	printWg.Wait()

	// finish progress bar line
	pbar.Finish()
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

// processConcurrently remains for compatibility but is not used by main
func processConcurrently(lines []string, workers int, extRegex *regexp.Regexp, includeMode bool, onlyQuery, onlyQueryKeys, noQuery bool) []string {
	jobs := make(chan string)
	results := make(chan string, len(lines))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for line := range jobs {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				u, err := url.Parse(line)
				path := line
				if err == nil && u.Path != "" {
					path = u.Path
				}

				if extRegex != nil {
					match := extRegex.MatchString(path)
					if includeMode && !match {
						continue
					} else if !includeMode && match {
						continue
					}
				}

				if onlyQuery {
					if err == nil && u.RawQuery != "" {
						results <- u.RawQuery
					}
					continue
				}
				if onlyQueryKeys {
					if err == nil && u.RawQuery != "" {
						pairs := strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == ';' })
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
							results <- k
						}
					}
					continue
				}
				if noQuery && err == nil {
					u.RawQuery = ""
					results <- u.String()
					continue
				}

				results <- line
			}
		}()
	}

	go func() {
		for _, l := range lines {
			jobs <- l
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := []string{}
	for r := range results {
		out = append(out, r)
	}
	return out
}
