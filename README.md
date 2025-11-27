# gowaybackgo

A small command-line tool that queries the Internet Archive (Wayback Machine) CDX API to list historical URLs for a target pattern and optionally extract subdomains, path segments, or query parts. Perfect for creating wordlists and automating stuff.

## Quick summary

- Input: a URL pattern (required), e.g. `*.example.com` or `example.com`.
- Output: a list of unique URLs (or subdomains/path segments/query keys depending on flags) printed to stdout and optionally saved to a file.
- Progress: a TTY-aware progress bar is shown (written to `/dev/tty` if available) so normal stdout is safe to pipe to files.

## Build / Install

Requires Go (the project uses the standard library only).

Installation using go:

```bash
go install -v github.com/OoS-MaMaD/gowaybackgo@latest
```

Build a local binary:

```bash
git clone https://github.com/OoS-MaMaD/gowaybackgo.git
cd /path/to/gowaybackgo
go build -o gowaybackgo
```

Or run directly with `go run` during development:

```bash
go run main.go -- -u "*.example.com"
```

## Usage

All flags are defined in the program. At minimum pass `-u` with a target pattern:

```bash
gowaybackgo -u "*.example.com"
```

Common flags:

- `-u` (required): Target URL pattern (examples: `*.example.com`, `example.com`, `https://example.com/path`). The code normalizes the pattern (removes scheme and appends a trailing `*` when needed) before querying CDX.
- `-o`: Output file path — when provided results are written to this file and also printed to stdout.
- `-only-query`: Output only the full query string for URLs that have one (e.g. `a=1&b=2`).
- `-only-query-keys`: Output only query parameter keys (unique) from all URLs.
- `-no-query`: Strip query strings and print URLs without the `?` portion.
- `-exclude-ext`: Comma-separated extensions to exclude (example: `jpg,png,css`). If the flag is omitted entirely, no extensions will be excluded unless `-exclude-defaults` is set.
- `-exclude-defaults`: Use the tool's built-in default exclusion list (see below).
- `-include-ext`: Comma-separated extensions to include (overrides exclude behavior). When provided, the tool will switch to include-mode and only keep URLs matching those extensions.
- `-workers`: Number of concurrent URL processing workers (default `20`). Controls how many lines from CDX are processed concurrently.
- `-page-workers`: Number of concurrent CDX page fetchers (default `10`). Controls how many CDX pages are fetched in parallel.
- `-extract-paths`: Instead of printing whole URLs, extract unique path segments and print each segment on its own line.
- `-subs`: Print unique subdomains (requires a base domain/pattern). The tool derives a normalized base domain and prints discovered subdomains.
- `-timeout`: HTTP client timeout in seconds (default `80`).

Default excluded extensions (used when `-exclude-defaults` is set, or when `-exclude-ext` flag is present but empty):

`js,css,png,jpg,jpeg,gif,svg,webp,ico,bmp,tif,tiff,woff,woff2,ttf,eot,mp4,mp3,wav,avi,mov,mkv,zip,rar,7z,pdf`

Notes about exclude/include behavior:
- If you omit the `-exclude-ext` flag entirely (i.e. don’t pass it), the tool treats this as "no excludes" unless you explicitly pass `-exclude-defaults`.
- If you pass `-exclude-ext` with an empty value (e.g. `-exclude-ext=""`), the code treats that as using the default exclude list.
- If `-include-ext` is set (non-empty), include-mode is enabled and only URLs matching those extensions will be kept.

## How it works (implementation highlights)

- The tool queries the Wayback CDX API to determine the number of pages to fetch, using the normalized pattern produced by `normalizeURLForCDX`.
- It fetches CDX pages concurrently (`-page-workers`) and queues CDX lines (original URLs) into a worker pool.
- Each worker parses and filters the URL lines (extension filters, query/path options). Matching lines are sent to a printer goroutine.
- Printer goroutines deduplicate results and print them. If `-o` was provided, results are written to the file as well.
- A TTY-aware progress bar (`PBar`) writes to `/dev/tty` when available; log messages and warnings go to stderr if no TTY.
- Page fetches retry up to 3 times on failure (with brief backoff).

## Examples

Simple list of URLs for `example.com`:

```bash
gowaybackgo -u "example.com"
```

Save results to a file while still seeing results on the terminal:

```bash
gowaybackgo -u "*.example.com" -o results.txt
```

Extract unique path segments (useful to see common directories or names):

```bash
gowaybackgo -u "example.com" -extract-paths
```

List unique subdomains for a base domain:

```bash
gowaybackgo -u "example.com" -subs
```

Only print query parameter keys (unique):

```bash
gowaybackgo -u "example.com" -only-query-keys
```
Excluding default file extensions and removing query parameters:

```bash
gowaybackgo -u "example.com" -exclude-defaults -no-query
```

Pipe raw URLs to another command or file (progress bar will render on `/dev/tty` and not pollute stdout):

```bash
gowaybackgo -u "example.com" > urls.txt
```

## Expected outputs and behavior

- When no pages are returned by CDX the program prints: `No pages reported by CDX; nothing to do.` and exits.
- The program prints colored warnings and retries to the progress bar or stderr depending on TTY availability.
- When output file is used the file handle is closed and a confirmation `✔ Saved results to <path>` is printed to stdout when done.

## Contract (inputs / outputs / error modes)

- Inputs: `-u` URL pattern string (required); optional flags as listed above.
- Outputs: newline-separated strings printed to stdout. The content depends on flags: full URLs, query strings, query keys, subdomains, or path segments.
- Error modes: network failures when fetching CDX pages will be retried; final network errors or malformed CDX responses are reported to stderr and the progress bar shows warnings.

## Edge cases and notes

- If a user provides a URL containing an explicit wildcard (`*`) the code tries to preserve that when forming the CDX query (but also strips wildcards where appropriate for domain extraction).
- The extension filter uses a case-insensitive regex that matches the URL path's extension. Provide extensions without a leading dot (e.g. `jpg,css`). If you need to include only certain extensions, use `-include-ext` to switch to include-mode.
- Large result sets: CDX pages are fetched concurrently and results are buffered; you can tune `-page-workers` and `-workers` to optimize throughput vs. resource usage.

## Troubleshooting

- If you see no output, ensure the `-u` flag was provided and that the pattern resolves to archived items on the Wayback CDX API.
- If progress seems stuck, try increasing `-page-workers` or verify network connectivity to `web.archive.org`.
- If your stdout consumer shows progress characters, ensure the terminal supports `/dev/tty` or redirect the output file explicitly with `-o`.

## License

See the `LICENSE` file in this repository for licensing details.

---

If you want, I can additionally add a few usage examples as shell scripts or small integration tests demonstrating the main workflows.
