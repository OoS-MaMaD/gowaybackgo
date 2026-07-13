# gowaybackgo

![Go version](https://img.shields.io/github/go-mod/go-version/OoS-MaMaD/gowaybackgo)
![License](https://img.shields.io/github/license/OoS-MaMaD/gowaybackgo)
![Latest release](https://img.shields.io/github/v/release/OoS-MaMaD/gowaybackgo?sort=semver)

A fast, dependency-free CLI that mines the **Internet Archive (Wayback Machine) CDX API** for every historical URL of a target, then filters and reshapes the results for recon, wordlists, and automation.

Point it at a domain and get back a clean, de-duplicated list of archived URLs — or just the subdomains, path segments, query keys, or structured JSON records you actually want.

```bash
gowaybackgo -u "*.example.com" --exclude-defaults -o urls.txt
```

## Why gowaybackgo

- **Concurrent by design** — parallel CDX page fetchers feed a worker pool, with tunable concurrency and an optional request-rate cap to stay under the archive's limits.
- **Purpose-built filtering** — extension include/exclude, capture-date windows, and server-side status/MIME filters, so you download less and grep less.
- **Multiple output shapes** — full URLs, unique subdomains, unique path segments, query strings, query keys, or JSONL — one flag each.
- **Pipe-safe & terminal-safe** — the progress bar renders on `/dev/tty`, leaving stdout clean for pipes; untrusted archived URLs are stripped of terminal control characters before printing.
- **Resilient** — automatic retries with exponential back-off on `429`/`5xx`, and graceful `Ctrl-C` that drains in-flight work and closes the output file cleanly.
- **Zero dependencies** — pure Go standard library; a single static binary.

## Install

**With Go** (installs to `$GOBIN`/`$GOPATH/bin`):

```bash
go install -v github.com/OoS-MaMaD/gowaybackgo@latest
```

**From a release** — grab a prebuilt binary from the [Releases](https://github.com/OoS-MaMaD/gowaybackgo/releases) page.

**From source:**

```bash
git clone https://github.com/OoS-MaMaD/gowaybackgo.git
cd gowaybackgo
go build -o gowaybackgo
```

Requires Go 1.24+ (standard library only).

Verify it works and check the version:

```bash
gowaybackgo --version
```

Release binaries report their tag (e.g. `gowaybackgo v1.2.3`); builds from source report `dev`.

## Quick start

```bash
# Every archived URL for a domain
gowaybackgo -u example.com

# All subdomains seen in the archive
gowaybackgo -u example.com --subs

# Interesting URLs only: drop static assets, save to file
gowaybackgo -u example.com --exclude-defaults -o urls.txt

# Structured records, successful HTML pages only
gowaybackgo -u example.com --json --status 200 --mime text/html
```

## Options

`-u` is required unless you pipe targets with `--stdin`. Flags accept either one or two dashes (`-json` == `--json`).

### Target & input

| Flag | Description |
|------|-------------|
| `-u <pattern>` | Target URL or domain pattern: `example.com`, `example.com/api/v1`, `*.example.com`, `https://example.com/path`. Scheme is stripped and a trailing `*` is appended when needed. |
| `--stdin` | Read targets from stdin, one per line (`#` starts a comment). Each is processed sequentially so results don't interleave. |

### Output

`-o` composes with any mode. The **modes** below are mutually exclusive — passing more than one is an error. With none set, the default is to print full URLs.

| Flag | Description |
|------|-------------|
| `-o <file>` | Also write results to `<file>` (still prints to stdout). Works with any mode. |
| `--only-query` | Mode: print only full query strings, e.g. `foo=1&bar=2`. |
| `--only-query-keys` | Mode: print only unique query parameter keys, e.g. `foo`, `bar`. |
| `--extract-paths` | Mode: print unique path segments, one per line. |
| `--subs` | Mode: print unique subdomains of the target domain. |
| `--json` | Mode: emit JSONL — one object per line with `url`, `timestamp`, `status`, `mime`. |
| `--no-query` | Transform on the default mode: strip the `?query` portion from output URLs. Ignored (with a warning) if a mode above is set. |

### Filtering

| Flag | Description |
|------|-------------|
| `--exclude-ext <exts>` | Comma-separated extensions to drop, e.g. `js,css,png`. |
| `--exclude-defaults` | Drop a built-in list of common static extensions (see below). |
| `--include-ext <exts>` | Keep **only** these extensions (overrides any exclude). |
| `--from <ts>` | Only captures at/after this time: `yyyy`, `yyyyMMdd`, or `yyyyMMddhhmmss`. |
| `--to <ts>` | Only captures at/before this time (same format). |
| `--status <re>` | Server-side CDX status filter, e.g. `200`, `2..`, `(200\|301)`. |
| `--mime <re>` | Server-side CDX MIME filter, e.g. `text/html`, `application/json`. |

### Performance & network

| Flag | Default | Description |
|------|---------|-------------|
| `--rate <n>` | `0` (unlimited) | Max CDX requests/sec. `5`–`10` is recommended to avoid `429`s. |
| `--page-workers <n>` | `10` | Concurrent CDX page fetchers. |
| `--workers <n>` | `20` | Concurrent URL processors. |
| `--timeout <sec>` | `80` | Per-request HTTP timeout. |
| `--proxy <url>` | — | Route requests through `http://`, `https://`, or `socks5://` proxy. Falls back to `HTTP_PROXY`/`HTTPS_PROXY` when unset. |

### Misc

| Flag | Description |
|------|-------------|
| `--version` | Print the version and exit. |
| `-h`, `--help` | Show the help/usage. |

### Extension filtering rules

Default excluded extensions (used with `--exclude-defaults`, or when `--exclude-ext` is passed with an empty value):

```
js css png jpg jpeg gif svg webp ico bmp tif tiff woff woff2 ttf eot
mp4 mp3 wav avi mov mkv zip rar 7z pdf
```

- Provide extensions **without** a leading dot (`jpg,css`, not `.jpg,.css`). Matching is case-insensitive against the URL path.
- Omitting `--exclude-ext` entirely means "no excludes" unless `--exclude-defaults` is set.
- `--include-ext` switches to include-mode and takes precedence over any exclude.
- Extension filtering is skipped in `--subs` mode so subdomains aren't dropped by a path extension.

## Recipes

**Bug bounty: harvest parameters for fuzzing**

```bash
gowaybackgo -u target.com --only-query-keys | sort -u > params.txt
```

**Feed a scope list, drop noise, save per run**

```bash
cat scope.txt | gowaybackgo --stdin --exclude-defaults -o wayback.txt
```

**Recent activity only, as JSON for a pipeline**

```bash
gowaybackgo -u target.com --json --from 2023 | jq -r 'select(.status=="200") | .url'
```

**Route through Burp / mitmproxy for review**

```bash
gowaybackgo -u target.com --proxy http://127.0.0.1:8080
```

**Map the directory structure**

```bash
gowaybackgo -u target.com --extract-paths | sort -u
```

**Discover subdomains from historical captures**

```bash
gowaybackgo -u target.com --subs
```

**Be gentle on the archive (avoid rate limiting)**

```bash
gowaybackgo -u target.com --rate 5 --page-workers 5
```

## How it works

1. A single request asks the CDX API how many result pages exist for the (filtered) query.
2. Page numbers are dispatched to a pool of `--page-workers` fetchers, optionally rate-limited. Each page fetch retries up to 3 times with exponential back-off on `429`/`5xx`.
3. Fetched CDX lines flow into `--workers` processors that apply extension/query filters and transforms.
4. A single printer goroutine de-duplicates and writes results, keeping writes serialized and memory bounded.
5. A TTY-aware progress bar renders on `/dev/tty` (or logs to stderr when there's no TTY), so stdout stays a clean data stream.

## Behavior notes

- **Output file:** with `-o`, results stream to both stdout and the file; on completion you'll see `✔ Saved results to <path>`. With `--stdin`, the file spans all domains and is closed once at the end.
- **Empty results:** if CDX reports no pages, the tool prints `No pages reported by CDX; nothing to do.` and exits 0.
- **Interrupting:** `Ctrl-C` (SIGINT/SIGTERM) cancels cleanly — in-flight fetches stop, buffered output is flushed, and the file is closed.
- **Safe output:** archived URLs are untrusted input; control/escape bytes are stripped before printing so a crafted archived URL can't tamper with your terminal.

## Troubleshooting

- **No output?** Confirm `-u` is set and the pattern actually has archived captures. Broad patterns like `*.example.com` help.
- **HTTP 429 / slow?** The archive is rate-limiting you — lower `--page-workers` and add `--rate 5`. Retries and back-off are automatic.
- **Progress characters in a pipe?** They shouldn't appear (the bar uses `/dev/tty`), but if your environment lacks a TTY, redirect with `-o` instead of shell redirection.

## Contributing

Issues and pull requests are welcome. The codebase is small, standard-library only, and covered by table-driven tests — run them with:

```bash
go test ./...
```

## License

Licensed under the **GNU General Public License v3.0**. See [LICENSE](LICENSE) for the full text.
