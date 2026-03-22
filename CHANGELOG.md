# Changelog

---

## [2.2.0] — 2026-03-21

### Watch mode (daemon)

Poll-based daemon that monitors source directories and automatically triggers sync when new content arrives. A download finishes in the source directory, medialnk detects it, parses it, creates the symlinks. No manual intervention.

Polling is used instead of inotify because the primary deployment target (MergerFS) is a FUSE filesystem and does not reliably propagate inotify events. Polling against MergerFS is reliable — `os.ReadDir` returns correct results and `os.Stat` returns correct sizes.

**New package**
- `internal/watch/watch.go` — `Watcher` type with poll loop, debounce timer, file stability checks, and incomplete download marker detection. Auto-detects whether `media_root_host` or `media_root_container` is locally accessible via `detectLocalRoot()`, so the same config works on bare metal and inside Docker.

**Detection layers (defense in depth)**
1. **Debounce timer** — configurable delay after detection before processing (default 30s). Absorbs filesystem activity from download starts.
2. **File stability check** — after debounce, verifies total file/directory size has not changed between poll cycles. Resets debounce if size changed. Requires 2 consecutive stable checks.
3. **Incomplete file markers** — checks for `.!qB`, `.part`, `.aria2` suffixes and `_UNPACK_` prefix. Resets debounce if markers found.

**Watch command** (`cmd/watch.go`)
- Replaces the previous stub. Full daemon lifecycle:
  - Loads config, validates, checks `[watch] enabled = true`
  - Builds a `syncFunc` closure that runs a full idempotent sync in non-interactive mode (auto=true, nonInteractive=true)
  - Creates Watcher, starts optional HTTP health endpoint, installs signal handler (SIGTERM/SIGINT)
  - Blocks on `w.Run()` until signal received
- Health endpoint (optional): when `[health] port` is non-zero, starts HTTP server with `/health` endpoint. Returns 200 if last poll completed within 2x poll interval, 503 if stale. For Docker HEALTHCHECK and external monitoring.
- Graceful shutdown: catches SIGTERM and SIGINT, stops watcher cleanly.

**Config section**
```toml
[watch]
enabled = false                # must be true for `medialnk watch` to start
debounce_seconds = 30          # wait after detecting new content before processing
poll_interval_seconds = 60     # how often to scan source directories

[health]
port = 0                       # health HTTP endpoint port, 0 = disabled
```

- `[watch] enabled` defaults to false. Running `medialnk watch` with `enabled = false` exits with an error. Prevents accidental daemon starts from stale cron jobs or Docker compose files.
- `[health] port` added to existing health config struct for the HTTP health endpoint.

### Non-interactive sync mode

New `nonInteractive bool` parameter on `movies.Run()`, `tv.Run()`, `tv.resolveDupes()`, `tv.handleConflicts()`, and `common.ValidateOutputDir()`. When true, all interactive prompts are skipped. Used by watch mode.

**Precedence:** `nonInteractive` is checked before `auto` at every prompt site. Where they differ, `nonInteractive` takes priority.

| Site | `auto=true` | `nonInteractive=true` |
|------|------------|----------------------|
| Movies Part.N ambiguity | auto-route to movie | skip, log `[WATCH]`, flag |
| TV duplicate seasons | pick first | pick first (same) |
| TV conflict quality/missing | auto-convert | skip, log `[WATCH]` |
| TV conflict bare_dir | auto-add | auto-add (same) |
| ValidateOutputDir real files | N/A | log warning, continue |

`cmd/sync.go` passes `nonInteractive: false` to all call sites — normal CLI behavior unchanged.

### TMDB rate limiter

Added 250ms minimum delay between TMDB API calls in `internal/resolver/tmdb.go`. Caps at ~4 requests/second, well within TMDB's free tier limit of ~40 per 10 seconds. The mutex serializes the 8-goroutine concurrent movie TMDB resolution to safe levels.

### Design decisions

- **Full rescan, not scoped sync.** Each watch-triggered sync runs the full idempotent pipeline. Scoped sync was deferred because the TV pipeline's Pass 1 builds a grouping map of all season folders that Pass 2 depends on for show matching and conflict routing. Scoping Pass 1 would break Pass 2 context. Full rescan is correct, simple, and fast enough — existing symlinks hit `[SKIP]` immediately, TMDB lookups are in-memory cache hits after the first run.
- **`medialnk watch` as the daemon entry point.** Future long-running features (metrics, webhooks, Jellyfin triggers) attach to the same HTTP mux and goroutine group rather than requiring a separate daemon command.

---

## [2.1.2] — 2026-03-20

### Health checks

Pre-sync validation that source directories are actually populated. Catches silently unmounted mergerfs drives before sync runs against empty mount points.

**New package**
- `internal/health/health.go` — `Check()` function returns per-source-dir health results. Walks source dirs counting video files via `common.IsVideo()` with early termination once threshold is met. Optional sentinel file check.

**Config section**
```toml
[health]
enabled = true              # default true
min_source_files = 10       # abort if fewer video files found
sentinel_file = ""          # optional: path that must exist
```

- `enabled` uses `*bool` pattern (same as `confidence_check`) to distinguish "not set" from "explicitly false"
- `min_source_files` defaults to 10; set to 0 to disable count check while keeping sentinel check
- `sentinel_file` resolved relative to each source dir if not absolute

**Integration**
- `cmd/sync.go`: health check runs after PathGuard validation, before output dir validation. Failures abort with `[ERROR]` even in dry-run mode. Override via `[health] enabled = false`.
- `cmd/validate.go`: health check results reported as `[PASS]`/`[FAIL]` alongside existing checks.
- `Config.Summary()` now includes health check status line.

### Orphan scanner

Reports source video files that have no corresponding symlink in the output layer.

**New package**
- `internal/orphans/orphans.go` — `Scan()` walks output directories resolving symlinks back to host paths via `ContainerToHost()`, then diffs against source directory contents. Returns `Report` with per-pipeline orphan lists, coverage counts, and coverage percentage.

**Algorithm: output symlink walking (not state-file-only)**
- Walks ALL output directories (`movies_linked` + `tv_linked`) to build a "covered" set
- File symlinks: `os.Readlink` → `ContainerToHost` → add to covered
- Directory symlinks (season folders, passthroughs): resolve target, walk into it to collect all video files — correctly handles passthroughs not recorded in state
- Real directories (converted season dirs): WalkDir descends automatically, inner file symlinks resolved individually
- Scanning both output dirs catches miniseries routing (movies_source files linked into tv_linked)
- Sample files excluded from source set via `common.IsSample()`

**New command**
- `cmd/orphans.go` — `medialnk orphans` with `--json`, `-q`, `-v` flags
- Human-readable output groups orphans by source folder with file sizes
- JSON output includes per-pipeline orphan arrays, covered counts, total counts, coverage percentage
- Quiet mode prints counts only

**Sync integration**
- After sync completes, orphan scanner runs and prints `[ORPHANS] N source files unlinked (X% coverage)` at normal verbosity

### Auto-clean broken symlinks during sync

**Config section**
```toml
[sync]
clean_after_sync = false    # default false
```

When enabled, broken symlinks are removed from output directories after sync completes (before orphan count). Uses the same `CleanBrokenSymlinks()` as `medialnk clean`. Skipped in dry-run mode. Reports `[CLEAN] dir: removed N broken symlink(s)`.

### Infrastructure

**`ContainerToHost()` added to `internal/common/symlink.go`**
- Inverse of `HostToContainer()`: translates container paths back to host paths
- Returns path unchanged if container root is not a prefix (graceful fallback)
- `SymlinkTargetExists()` refactored to use it (was inline translation)

**New command registration**
- `cmd/root.go`: `orphansCmd` added via `rootCmd.AddCommand()`

---

## [2.1.1] — 2026-03-20

### Movie parsing fixes

**Recursive FindVideos**
- `FindVideos` now accepts a `recursive bool` parameter. When true, subdirectories are searched for video files. Scene releases with nested folder structures (e.g. `3. Movie/`) are now found correctly.
- All three movie pipeline call sites (`scan`, `routeMovie`, `tmdbResolve`) use recursive mode.

**Part.N false positive fix**
- Bare movie files with "Part N" in the title (e.g. `Harry.Potter.and.the.Deathly.Hallows.Part.1.mkv`) are no longer falsely skipped as miniseries. The bare-file episode check now uses `IsEpisodeFile(name, false)` which excludes Part.N patterns — only SxxExx and standalone episode notation triggers the skip.
- Movie folder video scanning no longer excludes episode-notation files. After `isMiniseries()` and `isAmbiguousParts()` checks have passed, all non-sample videos are considered. Fixes directories like `The Hunger Games Mockingjay - Part 1` where the video inside was excluded by the Part.N pattern.

---

## [2.1.0] — 2026-03-20

### State tracking (Phase 2.1 — complete)

JSON state file written to each output directory after every non-dry-run sync. Records what was linked, skipped, flagged, and unmatched, plus run metadata.

**New package**
- `internal/state/state.go` — `Collector` type with nil-safe Record methods, `WriteMovies`/`WriteTV` serializers. All Record methods are goroutine-safe (mutex-protected) and no-op on nil receiver.

**State file format**
- Two hidden files: `{MoviesLinked}/.medialnk-state.json` and `{TVLinked}/.medialnk-state.json`
- Entry types: `movie`, `tv_season`, `tv_episode`
- Each entry records: type, title/show metadata, quality, source path, link path, linked_at timestamp
- `linked` array: symlinks created this run. `skipped` array: symlinks that already existed.
- `flagged` array (movies only): entries that could not be parsed (name + reason)
- `unmatched` array: files that could not be matched to any show/movie
- `run` object: version, started_at, completed_at, duration_ms, dry_run
- Multi-episode `second_ep` uses `*int` with `omitempty` — nil omitted, non-nil serialized

**PathGuard**
- Added `WriteFile(SafePath, []byte, fs.FileMode)` to `pathguard.go` — state file writes go through PathGuard

**Pipeline changes**
- `movies.Run()`, `routeMovie()`, `tmdbResolve()`: accept `*state.Collector`, record at all 3 MakeSymlink sites + flagged + TMDB misses
- `tv.Run()`, `handleNew()`, `handleConflicts()`, `convertSeason()`: accept `*state.Collector`, record at all 9 link sites + unmatched
- Passthrough folders are not recorded in state (by design)
- `cmd/sync.go`: creates Collector, passes to both pipelines, writes state files after completion. Gated on `--dry-run` (no write) and `--tv-only`/`--movies-only` (only relevant file written).

**Verified**
- First run: all entries appear in `linked`, `skipped` is empty
- Second run: all entries move to `skipped`, `linked` is empty (idempotent)
- Dry-run: state files are not written or updated
- Multi-episode entries serialize `second_ep` correctly
- 12 total call sites instrumented across movies.go and tv.go

---

## [2.0.0] — 2026-03-20

### Go port (Phase 1 — complete)

Full rewrite from Python to Go. Feature parity confirmed via dry-run diff against Python version (zero output difference after path normalization).

**Core infrastructure**
- `SafePath` type with unexported field — compiler enforces source immutability. All filesystem write functions (`Symlink`, `Remove`, `MkdirAll`) accept only `SafePath`, never raw strings. Stronger than the Python runtime enforcement.
- `go.mod` — module `github.com/AnAngryGoose/medialnk`, go 1.22.12, two external deps: BurntSushi/toml, spf13/cobra
- `internal/common/pathguard.go` — `SafePath`, `NewSafePath()`, guarded write functions
- `internal/common/helpers.go` — shared regex patterns, `EpisodeInfo()`, `ExtractQuality()`, `Sanitize()`, `CleanPassthroughName()`, `PromptChoice()`
- `internal/common/video.go` — `VideoExts` map, `IsVideo()`, `IsEpisodeFile()`, `FindVideos()`, `LargestVideo()`
- `internal/common/symlink.go` — `HostToContainer()`, `MakeSymlink()`, `EnsureDir()`, `SymlinkTargetExists()`, `IsSymlink()`, `IsBareDir()`, `CleanBrokenSymlinks()`, `ValidateOutputDir()`
- `internal/logger/logger.go` — four levels (quiet/normal/verbose/debug), writes to stdout and log file simultaneously
- `internal/config/config.go` — TOML loading, `Validate()`, `ValidatePathGuard()` (rejects output-inside-source configs), `FindConfig()` with three-location search, orphan overrides as typed struct

**Pipelines**
- `internal/movies/parse.go` — title/year/quality extraction from scene names
- `internal/movies/movies.go` — full movie pipeline: scan, TMDB resolution (goroutines + semaphore, 8 concurrent), version grouping, symlink creation
- `internal/tv/episodes.go` — `BuildLinkName()`, multi-episode combined naming (`S01E05-E06`)
- `internal/tv/parse.go` — `normKey()`, `normMatch()`, `showSeason()`, episode detection helpers
- `internal/tv/tv.go` — full two-pass TV pipeline: Pass 1 (season folders + miniseries from movies dir), Pass 2 (bare episodes + conflict resolution), season symlink-to-real-dir conversion
- `internal/resolver/confidence.go` — word overlap confidence checking with short-name length check
- `internal/resolver/tmdb.go` — TMDB search via stdlib `net/http`, global cache with mutex, 8-second timeout

**CLI**
- `cmd/root.go` — cobra root, `--config` persistent flag, version `2.0.0`
- `cmd/sync.go` — `-v`/`-vv` count flag, `--dry-run`, `--yes`, `--tv-only`, `--movies-only`
- `cmd/clean.go` — broken symlink removal with dry-run support
- `cmd/validate.go` — config validation + PathGuard check + output dir scan
- `cmd/testlib.go` — test library generation
- `cmd/watch.go` — stub (not yet implemented)

**Test infrastructure**
- `internal/testlib/generate.go` — 72-file fake library matching Python version exactly, auto-generates `medialnk.toml`

### Fixes applied during port

- **RE2 incompatibility:** Python's `(?<![Ss\d])E(\d{2,3})\b` uses a negative lookbehind not supported in RE2. Replaced with `(?:^|[^Ss\d])E(\d{2,3})\b`.
- **Movies per-symlink labels removed:** Initial port emitted `[LINK]`/`[DRY]`/`[SKIP]` per symlink in the movies loop. Python doesn't emit these there (only the summary `[MOVIES]` line). Removed to maintain parity.
- **Missing imports:** Several packages (`fmt`, `strings`) added during build/vet passes.

### Verified

- Dry-run output: zero diff between Python and Go versions after path normalization
- Idempotency: second run produces zero changes
- Live sync: season symlink-to-real-dir conversion, miniseries routing, bare episode placement all confirmed correct
- `validate` command: all checks pass

### Documentation

- `README.md` — Go version: binary install + build-from-source instructions, Go package file structure, compiler-enforced source protection description
- `OVERVIEW.md` — Go version: version 2.0.0 complete, "Why Go" section as current language rationale, Go-style function names (`normKey`/`normMatch`), Phase 1 marked complete / Phase 2 current

---

## Post-Phase-1 issues identified (pre-2.1)

Issues documented in `refs/post-phase-1-issues.md`. To be resolved before Phase 2 builds on top of them:

1. Movie title overrides loaded from config but never consulted in pipeline
2. TMDB transient failures permanently cache nil (should only cache confirmed misses)
3. TMDB numeric ID discarded (needed by Phase 3+ for NFO, artwork, quality upgrades)
4. `convertSeason` path translation unvalidated (`strings.Replace` without `HasPrefix` check)
5. `HostToContainer` same unvalidated replace bug as above
6. `epInFolder` / `epSymlinkExists` compile regex on every call (replace with `strings.Contains`)
7. `findMatch` rescans `tv_linked` on every bare file (scan once before loop)
8. `resolveVersions` sort doesn't lowercase titles (parity difference with Python)
9. `resolveVersions` uses manual O(n²) bubble sort (replace with `sort.Slice`)
10. `MakeSymlink` silently swallows failures (can't distinguish exists vs error)
11. `resolverLog` adapter in `tv.go` is defined but never used (dead code)