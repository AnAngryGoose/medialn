# medialnk Project Overview

**Version:** 2.2.0
**Repository:** https://github.com/AnAngryGoose/medialnk
**Language:** Go
**Dependencies:** BurntSushi/toml, spf13/cobra (build-time only, static binary output)

---

## What This Project Is

medialnk is a symlink-based media library manager. It sits between your raw storage and your media server, creating a clean organized presentation layer without touching the files underneath.

Think of it like a container image for your media library. The source files are the image: immutable, never touched. The symlink tree is the running container: clean, organized, and completely disposable. Blow it away and rebuild in seconds. Your actual files were never at risk.

Your torrent folder stays as chaotic as it needs to be for seeding and storage. Jellyfin always sees a clean, correctly named, fully organized library. Zero manual sorting. Zero risk to source files.

### Role in the Stack

medialnk is not a replacement for Radarr or Sonarr. It fills the gap those tools cannot:

- **Radarr/Sonarr** manage content they downloaded. They cannot import, organize, or upgrade content they did not acquire.
- **medialnk** manages everything outside arr's awareness: manually grabbed torrents, legacy libraries, specific encodes, content that arrived before arr was set up.
- **Together:** Radarr handles automated downloads into its own output directory. medialnk handles manually acquired content into its own output directory. Jellyfin points at both. No collision, no duplication, full coverage.

medialnk also solves several specific arr problems structurally: multi-season pack import failures, bulk library bootstrapping, safe import architecture that prevents arr's delete behavior from causing data loss, and cross-seed deduplication at the presentation layer.

---

## The Two-Layer Architecture

**Source layer (immutable).** Your actual media files on disk. Scene-named torrent downloads, bare files, mixed content, however it arrived. medialnk reads filenames and folder structures from this layer but never writes to it. This is enforced at the compiler level by PathGuard, not just by convention or documentation.

**Presentation layer (managed).** A parallel set of directories (`movies-linked/`, `tv-linked/`) containing nothing but symlinks organized the way Jellyfin, Plex, and the arr stack expect.

```
Source layer (read-only)              Presentation layer (symlinks)
/media/movies/                        /media/movies-linked/
  Some.Movie.2020.1080p.BluRay/         Some Movie (2020)/
    Some.Movie.2020.1080p.mkv              Some Movie (2020).mkv -> source
  The.Matrix.1999.2160p.REMUX/          The Matrix (1999)/
    The.Matrix.1999.2160p.mkv              The Matrix (1999) - 2160P.mkv -> source

/media/tv/                            /media/tv-linked/
  Breaking.Bad.S01.1080p.BluRay/        Breaking Bad/
    Breaking.Bad.S01E01.1080p.mkv          Season 01/ -> source folder
  Breaking.Bad.S02.720p.WEB-DL/           Season 02/ -> source folder
  Breaking.Bad.S01E07.1080p.mkv         Futurama/
  Futurama.3x05.720p.mkv                  Season 03/
                                             Futurama.S03E05 - 720P.mkv -> source
```

Consequences of this architecture:
- Deleting or rebuilding the presentation layer has zero impact on source files
- Torrent seeding continues from original paths without interruption
- Rollback is instant: delete the linked directories and run again
- The source layer can grow chaotically without affecting the organized view
- Multiple presentation layers can exist for different consumers (profiles)

---

## Source Protection

This is the most important architectural constraint. It is enforced at the compiler level, not by convention.

**PathGuard uses a `SafePath` type:**

```go
type SafePath struct {
    path string  // unexported, cannot be constructed arbitrarily
}

func NewSafePath(p string, outputRoots []string) (SafePath, error) {
    for _, root := range outputRoots {
        if strings.HasPrefix(p, root) {
            return SafePath{path: p}, nil
        }
    }
    return SafePath{}, fmt.Errorf("path %q is not under any output root", p)
}

// All write functions accept only SafePath, never raw strings
func Symlink(target string, link SafePath) error {
    return os.Symlink(target, link.path)
}
```

A raw string path can never reach a filesystem write function. The Go compiler enforces this. It is a compile error to attempt to bypass it, not a runtime crash.

**This constraint is non-negotiable. No feature, refactor, or shortcut justifies weakening it.**

A secondary check validates output directories on startup: if real video files are found in an output directory (indicating possible misconfiguration where source and output overlap), the user is warned and prompted before any work begins.

Source files are never read for content. Only filenames and sizes are examined.

---

## Why Go

The planned feature set requires real concurrency:

- **Watch mode daemon, webhook receiver, metrics endpoint, qBit API poller** all want to run concurrently in one process. Go's goroutines map naturally to this.
- **Single static binary** with no runtime dependency. Users install nothing. Docker images are small.
- **PathGuard is enforced at compile time** because Go's type system prevents raw strings from reaching write functions, rather than catching it at runtime.
- **Performance at scale** for large libraries with thousands of files.

The Deluge torrent client is a concrete example of what threading limitations look like at scale: sluggish UI, hanging operations, climbing CPU at idle. The planned medialnk daemon would hit the same wall with a different approach.

The two external dependencies (BurntSushi/toml, cobra) are build-time only. The compiled binary and Docker image have no runtime dependencies.

---

## Go Package Structure

```
medialnk/
  cmd/
    root.go          # cobra root command, global flags, version
    sync.go          # sync subcommand
    clean.go         # clean subcommand
    validate.go      # validate subcommand
    orphans.go       # orphans subcommand (Phase 2.3)
    watch.go         # watch subcommand (daemon mode)
    testlib.go       # test-library subcommand
  internal/
    config/
      config.go      # TOML loading, validation, path resolution
    common/
      pathguard.go   # SafePath type, NewSafePath, all write functions
      symlink.go     # symlink creation, existence checking, path translation
      video.go       # video extension list, file detection helpers
    movies/
      movies.go      # movie pipeline orchestration
      parse.go       # title/year/quality extraction from scene names
    tv/
      tv.go          # TV pipeline orchestration, Pass 1 and Pass 2
      parse.go       # show name extraction, season/episode detection
      episodes.go    # episode format regexes, episode_info logic
    resolver/
      tmdb.go        # TMDB API calls via stdlib net/http
      confidence.go  # word overlap, length check, accept/reject logic
    state/
      state.go       # Collector, Entry types, WriteMovies/WriteTV (Phase 2.1)
    health/
      health.go      # source directory health checks (Phase 2.4)
    orphans/
      orphans.go     # orphan scanner, output symlink walking (Phase 2.3)
    watch/
      watch.go       # poll-based watcher, debounce, stability checks (Phase 2.5)
    testlib/
      generate.go    # fake library generator for testing
  main.go
  medialnk.toml      # default config template
```

`internal/` is intentional. Go's internal package convention prevents anything outside the module from importing these packages. Encapsulation is enforced by the language, not convention.

---

## CLI Interface

```bash
medialnk sync                  # full scan and link
medialnk sync --dry-run        # preview only, no writes
medialnk sync --yes            # auto-accept all prompts
medialnk sync --tv-only        # skip movies pipeline
medialnk sync --movies-only    # skip TV pipeline
medialnk sync -v               # verbose output
medialnk sync -vv              # debug output
medialnk sync -q               # quiet (errors and warnings only)

medialnk clean                 # remove broken symlinks
medialnk clean --dry-run

medialnk validate              # check config, paths, PathGuard, health
medialnk orphans               # report unlinked source files
medialnk orphans --json        # machine-readable orphan report
medialnk orphans -q            # orphan counts only

medialnk watch                 # daemon: poll source dirs, auto-sync new content
                               # requires [watch] enabled = true in config

medialnk test-library /path    # generate fake library for testing
medialnk test-library /path --reset

medialnk --config /path/to/medialnk.toml sync --dry-run
medialnk --version
```

---

## Config Format

Config file searched in order:
1. `--config /path/to/medialnk.toml` (CLI flag)
2. `./medialnk.toml` (current directory)
3. `~/.config/medialnk/medialnk.toml`

```toml
[paths]
media_root_host = "/mnt/storage/data/media"
media_root_container = "/data/media"    # same as host if not using Docker
movies_source = "movies"
tv_source = "tv"
movies_linked = "movies-linked"
tv_linked = "tv-linked"

[tmdb]
api_key = ""                            # or TMDB_API_KEY env var
confidence_check = true

[logging]
log_dir = "logs"
verbosity = "normal"                    # quiet | normal | verbose | debug

[overrides.tv_names]
"The Office US" = "The Office (US)"

[overrides.tv_orphans]
"Season 1" = { show = "Little Bear", season = 1 }

[health]
enabled = true                          # default true
min_source_files = 10                   # abort sync if fewer video files found
sentinel_file = ""                      # optional: path that must exist

[sync]
clean_after_sync = false                # remove broken symlinks after each sync

[watch]
enabled = false                         # must be true for `medialnk watch`
debounce_seconds = 30                   # delay after detecting new content
poll_interval_seconds = 60              # source directory poll interval
```

**Rule:** Config file controls installation behavior (paths, keys, overrides). CLI flags control per-run behavior (`--dry-run`, `--yes`, `-v`). CLI flags override config where they overlap.

Every opt-in feature has its own config section. Nothing is enabled by default beyond the core sync behavior.

---

## Pipelines

### Movies Pipeline

Scans `movies_source`, extracts title and year from scene names, groups multi-version entries, creates symlinks in `movies_linked`.

Key behaviors:
- `normalize()`: converts underscores to dots before parsing
- `RE_STRIP`: strips noise tags (VHS, Uncut, etc.) before title extraction
- Multi-version grouping: `Movie (Year) - 1080P.mkv` and `Movie (Year) - 2160P.mkv` in same folder
- Same-quality duplicates get numbered suffix: `Movie (Year) - 1080P.2.mkv`
- Miniseries detection: folders with 2+ episode files are skipped and handed to TV pipeline
- Part.N folders: flagged as ambiguous, user prompted for movie vs TV routing
- TMDB lookup for entries missing a year

Movies pipeline always runs before TV pipeline. TV's miniseries scanner reads the movies source directory. Consistent run order ensures consistent results.

### TV Pipeline (Two Passes)

**Pass 1: Folder-based content**
- Scans `tv_source` for season folders
- Groups by show name using light normalization (`normKey()`)
- `SEASON_TEXT_RE` fallback for spelled-out "Season N" folder naming
- `scanSeasonContainer()` for multi-season pack folders with S01-SNN range indicators
- Creates season symlinks under show directories
- Pass-through for already-structured folders (Jellyfin/Plex naming preserved)
- `cleanPassthroughName()` for safe cleanup of pass-through content
- Scans `movies_source` for miniseries (folders with episode files)
- Prompts on duplicate season quality (two folders for same season at different qualities)

**Pass 2: Bare episode files**
- Handles individual episode files in `tv_source` with no parent season folder
- Parses show name and episode info from filename
- Resolves show name via overrides, TMDB, or fallback
- Matches against Pass 1 results
- Conflict resolution: quality variants, missing episodes, season folder conversion

### Episode Format Support

| Format | Example | Status |
|--------|---------|--------|
| SxxExx | `Show.S01E05.mkv` | Supported |
| Multi-ep | `Show.S01E05-E06.mkv` | Supported (combined naming) |
| NxNN | `Futurama.3x05.mkv` | Supported |
| Episode.N | `Documentary.Episode.4.mkv` | Supported |
| NofN | `Planet.Earth.1of6.mkv` | Folder scan only |
| Bare E01 | `pe.E01.mkv` | Folder detection only |
| Part.N | `Kill.Bill.Part.1.mkv` | Ambiguity prompt |

Multi-episode combined naming (`S01E05-E06`) is used because both Jellyfin and Sonarr recognize it. Two separate symlinks for one file would appear as duplicate content.

### TMDB Resolver

All TMDB calls use stdlib `net/http`. No external HTTP library.

Confidence checking before accepting a result:
- Short names (1-2 words): all parsed words must be present in result, at most 1 extra word allowed
- Longer names: 50%+ word overlap required
- Word comparison normalizes to plain alphanumeric before comparison (fixes failures on titles with special characters)
- Rejected matches fall back to parsed name and log `[TMDB] Rejected`

---

## Normalization Functions

Two separate normalization functions exist for different purposes. Do not conflate them.

**`normKey()`**: Light normalization for Pass 1 folder grouping. Lowercases, strips apostrophes. Used to group `Schitts.Creek.S01` and `Schitt's Creek.S02` under the same show. Aggressive normalization here would misgroup folders.

**`normMatch()`**: Aggressive normalization for cross-source matching. Strips articles, studio prefixes, years, all punctuation. Used when comparing a bare episode file's parsed show name against Pass 1 results.

---

## Log Labels

| Label | Meaning |
|-------|---------|
| `[LINK]` | Symlink created successfully |
| `[SKIP]` | Already exists, no action taken |
| `[NO LINK]` | Source entry could not be parsed (no title, no year, no video file) — not linked |
| `[UNPROCESSED]` | Could not parse or route, passed through as-is |
| `[TMDB]` | TMDB lookup result (accepted or rejected) |
| `[WARN]` | Non-fatal issue worth noting |
| `[ERROR]` | Fatal or near-fatal issue |
| `[DRY]` | Dry-run only, would have linked |
| `[WATCH]` | Watch mode activity (detected, triggered, skipped for manual review) |

---

## Conflict Resolution Scenarios (TV)

| # | Situation | What happens |
|---|-----------|-------------|
| 15 | Same episode, same quality, already covered | Silently skipped |
| 16 | Same episode, different quality | Prompt to convert season symlink to real dir, re-link individually with quality tags |
| 17 | Episode missing from season folder | Same conversion prompt, missing episode added |
| 18 | Multiple conflicts, same season | First conflict converts, subsequent conflicts add directly without re-prompting |
| 19 | Season already converted from previous run | Detected as bare_dir, episode added directly |
| 20 | Duplicate season, different quality folders | User prompted to choose quality |
| 21 | Everything already linked | Every call returns already-exists, runs silently, idempotent |

Season symlink to real directory conversion is necessary because individual episode symlinks cannot be written into a folder symlink without writing into the source directory. A real directory in the presentation layer is the only correct structure that allows mixed content from different sources.

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go | Planned daemon features (watch mode, webhook receiver, metrics endpoint, qBit poller) need real concurrency. Go goroutines map naturally. Static binary, no runtime dependency. |
| SafePath type (compiler enforcement) | Runtime crash on bad path is weaker than compile error. With SafePath, bypassing source protection is structurally impossible, not just caught at runtime. |
| BurntSushi/toml dependency kept | No TOML in Go stdlib. Rolling a parser introduces maintenance burden and subtle edge case bugs. Build-time dependency only, users see a static binary. |
| cobra dependency kept | Manual subcommand dispatch is achievable but costs ~150 lines and loses auto-generated help text. Cobra is ubiquitous in Go CLIs and negligible as a dependency. |
| Symlinks not hardlinks | MergerFS EXDEV: `os.link()` fails with cross-device error when source and destination land on different physical drives under the union mount. Symlinks follow the unified mount path regardless. |
| Absolute container paths in symlinks | Relative symlinks break when Docker container working directory differs from host. Absolute container paths always resolve correctly inside Jellyfin and arr containers. |
| Movies pipeline before TV | TV's miniseries scanner reads the movies source directory. Consistent run order ensures consistent results. |
| Part.N excluded from miniseries detection | Could be multi-part film or miniseries. No heuristic is reliable. Human routing is the only correct answer. |
| Two normalization functions | Pass 1 grouping needs light normalization. Cross-source matching needs aggressive normalization. Conflating them would misgroup Pass 1 folders. |
| TMDB word-overlap with length check | Pure word overlap accepts false matches ("Retro Show" matches "Super Maximum Retro Show" since both parsed words are present). Length check rejects results where the TMDB title is clearly a different longer-named show. |
| Season symlink to real dir for conflicts | Cannot write individual episode symlinks into a folder symlink without writing into source. Real directory in presentation layer is the only correct structure. |
| Arr rename OFF during initial import | Arr rename renames entries in the presentation layer, not source. During bulk import this causes mismatched library state. |
| Safe pass-through cleanup only | Dots to spaces is non-destructive. Stripping years or metadata from pass-through names could break Jellyfin matching for already-correctly-structured folders. |
| Per-run log files | Console can be quiet for cron. Log always has detail for debugging. Critical when tool runs unattended. |
| Config for installation behavior, CLI for run behavior | Paths and API keys don't change between runs. `--dry-run` and `--yes` change per invocation. |
| All opt-in features default off | A minimal install behaves exactly like the current tool. Users enable only what they want. New features cannot break existing setups. |
| Polling not inotify for watch mode | MergerFS is a FUSE filesystem and does not reliably propagate inotify events. Polling against MergerFS is reliable — `os.ReadDir` and `os.Stat` return correct results. |
| Full rescan per watch trigger (not scoped sync) | TV Pass 1 builds a grouping map of all season folders that Pass 2 depends on. Scoping Pass 1 breaks Pass 2 context. Full rescan is correct, simple, and fast enough — existing symlinks hit `[SKIP]`, TMDB lookups are cache hits after first run. |
| `nonInteractive` separate from `auto` | `auto` (`--yes`) auto-accepts defaults. Watch mode needs different behavior: skip ambiguous items and log for manual review. `nonInteractive` is checked before `auto` at every prompt site; where they differ (Part.N, quality conflicts), `nonInteractive` takes priority. |
| `medialnk watch` as daemon entry point | Future long-running features (metrics, webhooks, Jellyfin triggers) attach to the same HTTP mux and goroutine group rather than requiring a separate daemon command. |
| Three-layer download detection | Debounce timer absorbs filesystem activity; stability check catches slow transfers; incomplete markers catch known download client patterns. Defense in depth — no single layer needs to be perfect. |
| TMDB 250ms rate limiter | Caps at ~4 req/sec, well within TMDB's free tier limit of ~40/10s. Mutex serializes the 8-goroutine concurrent movie resolver to safe levels. |

---

## Function Reference

Complete catalogue of every exported and significant unexported function, grouped by package.

---

### `internal/common` — pathguard.go

| Function / Method | Description |
|---|---|
| `NewSafePath(p, outputRoots)` | Validates `p` is under an output root; returns a `SafePath` or error. The only constructor for `SafePath`. |
| `SafePath.Path()` | Returns the validated path string for read-only operations (stat, lstat, readdir). |
| `SafePath.String()` | Implements `fmt.Stringer`; returns the path string. |
| `Symlink(target, link SafePath)` | Creates a symlink at `link` pointing to `target`. Only write function for symlinks. |
| `Remove(path SafePath)` | Removes a file or empty directory. Compiler-enforced, no raw string. |
| `MkdirAll(path SafePath, perm)` | Creates directory and all parents, like `os.MkdirAll`. |
| `WriteFile(path SafePath, data, perm)` | Writes data to a file, like `os.WriteFile`. Used for state file output. |

---

### `internal/common` — symlink.go

| Function | Description |
|---|---|
| `HostToContainer(path, hostRoot, containerRoot)` | Translates a host absolute path to its container-side equivalent by replacing the host root prefix. Returns error if path doesn't start with hostRoot. |
| `ContainerToHost(path, hostRoot, containerRoot)` | Inverse of `HostToContainer`: translates container paths back to host paths. Returns path unchanged if container root is not a prefix. Used by orphan scanner to resolve symlink targets. |
| `MakeSymlink(linkPath SafePath, targetHostPath, dryRun, hostRoot, containerRoot)` | Creates a symlink at `linkPath` pointing to `targetHostPath` (translated via `HostToContainer`). Skips if already exists. Returns true if created (or would be in dry-run). |
| `EnsureDir(path SafePath, dryRun)` | Creates directory and parents unless dry-run. |
| `SymlinkTargetExists(linkPath, hostRoot, containerRoot)` | Returns true if the symlink at `linkPath` resolves to an existing file (applies container→host translation). |
| `IsSymlink(path)` | Returns true if path is a symbolic link. |
| `IsBareDir(path)` | Returns true if path is a real directory (not a symlink). |
| `CleanBrokenSymlinks(directory SafePath, hostRoot, containerRoot)` | Walks directory removing broken symlinks, then prunes empty subdirectories. Returns count removed. |
| `ValidateOutputDir(directory, dryRun, nonInteractive)` | Checks for real (non-symlink) video files in an output directory. Warns the user and prompts before continuing if any are found. In non-interactive mode, logs a warning and continues without prompting. |

---

### `internal/common` — video.go

| Function | Description |
|---|---|
| `IsVideo(filename)` | Returns true if the filename has a recognized video extension (`.mkv`, `.mp4`, `.avi`, `.ts`, `.m4v`). |
| `IsEpisodeFile(filename, includePart)` | Returns true if the filename contains episode notation. When `includePart` is false, Part.N patterns are excluded. |
| `FindVideos(folder, excludeEpisodes, excludeSamples, recursive)` | Returns video files in `folder`, optionally recursing into subdirectories. Episode and sample filtering available. |
| `LargestVideo(videos)` | Returns the `VideoEntry` with the largest `Size`. |

---

### `internal/common` — helpers.go

| Function | Description |
|---|---|
| `IsSample(filename)` | Returns true if the filename matches the sample file pattern. |
| `EpisodeInfo(filename, includePart)` | Parses `(season, episode)` from a filename using a regex cascade: SxxExx → NxNN → Episode.N → bare E01 → NofN → Part.N. Returns nil if nothing matches. |
| `ExtractQuality(name)` | Returns the first quality tag found in the name (uppercased), e.g. `1080P`, `WEB-DL`. Empty string if none. |
| `Sanitize(name)` | Replaces filesystem-illegal characters (`/:\?*"<>\|`) with `-`. |
| `CleanPassthroughName(folderName)` | Converts dots to spaces (only if name has no spaces), then normalizes whitespace. No metadata stripping. |
| `PromptChoice(message, valid)` | Reads stdin in a loop until the user enters one of the valid choices (case-insensitive). |

**Shared regex patterns** (exported for use by tv/parse.go):

| Pattern | Matches |
|---|---|
| `ReSxxExx` | `S01E05`, `s1e2`, etc. |
| `ReXNotation` | `3x05`, `1x02`, etc. |
| `ReEpisode` | `Episode.4`, `Episode_12` |
| `ReBareEpisode` | `E01`, `E12` not preceded by S/digit |
| `ReMultiEp` | `-E06`, `.E06` (multi-episode suffix) |
| `ReNof` | `1of6`, `(2of8)` |
| `RePart` | `Part.1`, `Pt 2`, etc. |

---

### `internal/config`

| Function / Method | Description |
|---|---|
| `Load(path)` | Reads a TOML config file and returns a fully resolved `*Config`. Resolves relative paths to absolute using `media_root_host`. TMDB key from env overrides config file. |
| `FindConfig(cliPath)` | Searches for a config file: CLI path → `./medialnk.toml` → `~/.config/medialnk/medialnk.toml`. Returns empty string if not found. |
| `Config.Validate()` | Checks that `media_root_host`, `movies_source`, and `tv_source` directories exist. Returns a slice of error strings. |
| `Config.Summary()` | Returns a human-readable string of the resolved config for display at startup. |
| `Config.ValidatePathGuard()` | Returns an error if any output directory is inside a source directory (misconfiguration guard). |

---

### `internal/movies` — parse.go

| Function | Description |
|---|---|
| `normalize(name)` | Replaces underscores with dots so older scene releases parse correctly. |
| `year(name)` | Extracts the first 4-digit year (1900–2099) from a normalized filename. Returns empty string if none. |
| `title(name)` | Extracts a clean human-readable title from a scene-format name by stripping noise tags, release group, year, codec, etc. |
| `isMiniseries(folder)` | Returns true if a folder contains ≥2 video files with episode notation (indicating TV, not movie). |
| `isAmbiguousParts(folder)` | Returns true + sorted filenames if a folder contains ≥2 Part.N video files with no other episode notation (ambiguous: movie or TV). |

---

### `internal/movies` — movies.go

| Function | Description |
|---|---|
| `scan(cfg)` | Scans `movies_source`; categorizes entries as movies, flagged (no title/year), skipped (miniseries), or ambiguous (Part.N). Returns all four slices. |
| `resolveVersions(seen)` | Flattens the grouped `map[key][]movieEntry` into a sorted slice, assigning quality labels to multi-version groups and numbering same-quality duplicates. |
| `Run(cfg, dryRun, auto, nonInteractive, log, col)` | Executes the full movie pipeline: scan → link → handle ambiguous Part.N → TMDB yearless resolution. Records all links, skips, flags, and TMDB misses to the state collector. When `nonInteractive` is true, skips ambiguous Part.N entries (logs `[WATCH]` and flags for review) instead of prompting. Returns summary count map. |
| `routeMovie(entryName, t, y, cfg, dryRun, log, col)` | Handles an ambiguous Part.N folder confirmed as a movie: finds the largest video and creates the symlink. Records to state collector. |
| `tmdbResolve(noYear, cfg, dryRun, log, col)` | Concurrently resolves yearless flagged entries via TMDB (up to 8 goroutines). Creates symlinks for resolved entries. Records links and misses to state collector. Returns count resolved. |

---

### `internal/tv` — parse.go

| Function | Description |
|---|---|
| `normKey(name)` | Light normalization: lowercase + strip apostrophes + normalize whitespace. Used for Pass 1 folder grouping so minor name variations map to the same show. |
| `normMatch(name)` | Aggressive normalization: strip articles, studio prefixes, possessives, years, all non-alphanumeric. Used for cross-source matching (bare episode files vs Pass 1 results). |
| `showSeason(folder, overrides)` | Parses a season folder name into `(show, seasonNum)` using `seasonRE` then `seasonTextRE`. Applies name overrides. Returns `("", 0, false)` on no match. |
| `cleanShow(folder)` | Strips scene metadata from a folder name leaving just the show title. |
| `isBareEpFolder(folder)` | Returns true if a folder contains ≥2 individually-named episode files or directories (episode notation in the names). |
| `ParseBareEpisode(filename)` | Parses show/season/episode/quality from a bare episode filename using SxxExx → NxNN → Episode.N cascade. Returns nil if unrecognized. |
| `findMatch(show, grouped, linkedEntries)` | Finds the canonical show name in `grouped` or pre-scanned `tv_linked` entries that matches the normalized show name. Returns empty string if no match. |
| `epInFolder(folder, episode, season)` | Checks if a specific episode (`S%02dE%02d`) exists in the source folder. Returns `(found, quality)`. |
| `epSymlinkExists(seasonDir, episode, season)` | Checks if a symlink for a specific episode exists in an output season directory. |
| `normCompare(name)` | Strips TVDB IDs, year annotations, then applies `cleanShow`. Used for duplicate/overlap warning detection. |

---

### `internal/tv` — episodes.go

| Function | Description |
|---|---|
| `BuildLinkName(show, season, episode, quality, ext, secondEp)` | Constructs the standardized episode symlink filename: `Show.S01E05 - 1080P.mkv` or `Show.S01E05-E06 - 1080P.mkv` for multi-episode. |

---

### `internal/tv` — tv.go

| Function | Description |
|---|---|
| `scanSeasonContainer(folderName, folderPath, overrides)` | Handles multi-season pack folders (S01-S31 range indicator). Extracts the show name, recurses one level to find season subfolders, returns `(show, seasonNum, relPath)` tuples. |
| `scanTV(cfg)` | Pass 1 scanner: reads `tv_source`, groups entries by show name into `map[show][]seasonEntry`. Returns the grouped map and a list of passthrough folder names. |
| `scanMiniseries(cfg)` | Reads `movies_source` for folders containing ≥2 episode files (miniseries). Returns a map of show name → episode list. |
| `resolveDupes(show, seasons, dryRun, auto, nonInteractive, log)` | For shows with multiple folders for the same season number, prompts the user (or picks first in auto/dry-run/non-interactive) to select one. Returns the deduplicated season list. |
| `convertSeason(show, snum, path, cfg, dryRun, log, col)` | Replaces a season folder symlink with a real directory, re-linking all episodes individually. Records each re-linked episode to state collector (only when ParseBareEpisode succeeds). |
| `scanBare(grouped, cfg, log)` | Pass 2 scanner: reads bare episode files in `tv_source`, classifies each as new, conflict, or unmatched. Returns all three lists. |
| `handleNew(newEps, cfg, dryRun, log, col)` | Creates show/season directories and symlinks for all new bare episodes (no conflict with Pass 1). Records to state collector. Returns count linked. |
| `handleConflicts(conflicts, cfg, dryRun, auto, nonInteractive, log, col)` | Resolves conflict episodes: quality variants and missing episodes trigger `convertSeason` then add the link; bare_dir episodes add directly to an existing real season dir. When `nonInteractive` is true, skips quality/missing conflicts (logs `[WATCH]` for review) and auto-adds bare_dir entries. Records to state collector. Returns count resolved. |
| `warnings(grouped, pt)` | Detects and returns warning strings for: duplicate season folders, name overlap between grouped shows and passthrough folders. |
| `Run(cfg, dryRun, auto, nonInteractive, log, col)` | Executes the full two-pass TV pipeline: Pass 1 (season symlinks) → passthrough → miniseries → Pass 2 (bare files). When `nonInteractive` is true, skips ambiguous prompts and logs for manual review. Records all links, skips, and unmatched to state collector. Returns summary count map. |

---

### `internal/resolver` — confidence.go

| Function | Description |
|---|---|
| `words(s)` | Normalizes a string to a set of lowercase plain words (strips non-word characters). |
| `wordOverlap(parsed, result)` | Confidence check: short names (1-2 words) require all parsed words present with ≤1 extra; longer names require 50%+ word overlap. |

---

### `internal/resolver` — tmdb.go

| Function | Description |
|---|---|
| `ClearCache()` | Resets the global in-memory TMDB result cache. Use between test runs. |
| `tmdbGet(endpoint, query, apiKey)` | Makes a TMDB API GET request and returns the raw response body. 8-second timeout. Rate-limited to 250ms between calls (~4 req/sec) via mutex, safe for concurrent use by the 8-goroutine movie resolver. Not exported. |
| `SearchTV(name, apiKey, confidence, log)` | Looks up a TV show on TMDB; returns `(canonicalTitle, id)` or `("", 0)`. Results are cached per query; transient failures are not cached. |
| `SearchMovie(title, apiKey, confidence, log)` | Looks up a movie on TMDB; returns `(canonicalTitle, year, id)` or `("", "", 0)`. Results are cached per query. |
| `ResolveTVName(parsed, overrides, apiKey, confidence, log)` | Returns the canonical show name via override → TMDB → parsed fallback. ID is 0 for override/fallback results. |

---

### `internal/state`

| Function / Method | Description |
|---|---|
| `New()` | Creates a new `Collector` with the current UTC time as the run start. |
| `Collector.RecordMovieLink(title, year, quality, src, link)` | Records a newly created movie symlink with timestamp. Nil-safe. |
| `Collector.RecordMovieSkip(title, year, quality, src, link)` | Records a movie symlink that already existed (skip). Nil-safe. |
| `Collector.RecordMovieFlagged(name, reason)` | Records a source entry that could not be parsed. Nil-safe. |
| `Collector.RecordMovieUnmatched(name)` | Records a yearless entry that TMDB could not resolve. Nil-safe. |
| `Collector.RecordTVSeasonLink(show, season, src, link)` | Records a newly created TV season folder symlink. Nil-safe. |
| `Collector.RecordTVSeasonSkip(show, season, src, link)` | Records a TV season symlink that already existed. Nil-safe. |
| `Collector.RecordTVEpisodeLink(show, season, episode, secondEp, quality, src, link)` | Records a newly created TV episode symlink. `secondEp` is `*int` (nil for single ep). Nil-safe. |
| `Collector.RecordTVEpisodeSkip(show, season, episode, secondEp, quality, src, link)` | Records a TV episode symlink that already existed. Nil-safe. |
| `Collector.RecordTVUnmatched(names)` | Records bare episode filenames that could not be matched. Nil-safe. |
| `Collector.WriteMovies(path SafePath, version)` | Finalizes run metadata and writes the movies state to a JSON file via PathGuard. |
| `Collector.WriteTV(path SafePath, version)` | Finalizes run metadata and writes the TV state to a JSON file via PathGuard. |

---

### `internal/health`

| Function | Description |
|---|---|
| `Check(cfg)` | Runs health checks against all source directories. Returns a `Result` per source dir and an overall pass bool. Checks sentinel file existence and minimum video file count with early termination. Read-only. |

---

### `internal/orphans`

| Function / Method | Description |
|---|---|
| `Scan(cfg)` | Walks source and output directories, cross-references symlink targets against source files, returns `*Report` with orphan lists and coverage counts. Walks output symlinks (not just state file) to correctly handle passthroughs. |
| `Report.TotalOrphans()` | Returns total orphan count across both pipelines. |
| `Report.TotalSource()` | Returns total source video file count. |
| `Report.CoveragePct()` | Returns percentage of source files that have symlinks. |

---

### `internal/watch`

| Function / Method | Description |
|---|---|
| `New(cfg, log, syncFunc)` | Creates a new `Watcher`. Calls `detectLocalRoot()` to determine which config root (host or container) is locally accessible. `syncFunc` is a `func() error` closure that runs a full idempotent sync. |
| `Watcher.Run()` | Blocking poll loop. Takes an initial snapshot of source directories, then polls at `WatchPollInterval`. Detects new entries, debounces, checks stability and incomplete markers, triggers sync. Returns on `Stop()` or error. |
| `Watcher.Stop()` | Signals the poll loop to exit. Safe for concurrent call (closes stop channel). |
| `Watcher.LastPollAt()` | Returns the timestamp of the last completed poll cycle. Used by the HTTP health endpoint to detect stale watchers. |
| `detectLocalRoot(cfg)` | Checks whether `HostRoot` or `ContainerRoot` is accessible on the local filesystem. Returns whichever resolves. Allows the same config to work on bare metal and inside Docker. |
| `Watcher.localPath(cfgAbsPath)` | Translates a config absolute path (resolved against HostRoot) to the locally accessible equivalent by swapping the root prefix. Fixes the path double-nesting bug that would occur from naive `filepath.Join`. |
| `Watcher.sourceDirs()` | Returns the list of source directories to poll, translated via `localPath()`. |
| `Watcher.snapshot()` | Seeds `knownPaths` with all current entries in source directories so they are not treated as new on the first poll. |
| `Watcher.poll()` | Single poll cycle: scans source dirs for new entries, manages debounce timers, checks incomplete download markers and size stability, triggers sync when entries are ready. |
| `totalSize(path)` | Returns the total byte size of a file or directory tree. Used for stability checking between poll cycles. |
| `hasIncompleteMarkers(path)` | Returns true if the path contains files with `.!qB`, `.part`, `.aria2` suffixes or `_UNPACK_` prefix, indicating an in-progress download. |

---

## Planned Work Summary

Full detail in TODO.md. High-level phases:

1. **Go port** (complete) — Feature parity with Python version
2. **Foundation** (current) — State tracking (complete), orphan scanner (complete), health checks (complete), watch mode daemon (complete), Docker, `medialnk status`
3. **Presentation layer enrichment** — Companion files, NFO generation, artwork, TMDB collections, user profiles, ffprobe validation
4. **Automation and stack integration** — qBit API, subtitle download, Jellyfin API, Autoscan, arr webhook receiver, notifications, Prometheus
5. **Arr integration layer** — Arr handoff mode, multi-season pack bridge, unmatched release rescue, cross-seed deduplication, safe import architecture, specials handling
6. **Library intelligence** — Missing content detection, watched status portability, storage reclamation, source path migration, quality upgrade awareness
7. **Power features** — Policy config, diff/rollback, TUI conflict resolution, output profiles, YouTube pipeline
8. **Watch mode daemon** (complete) — Implemented as Phase 2.5. Poll-based watcher, non-interactive sync, TMDB rate limiter, HTTP health endpoint
9. **Anime pipeline** — AniDB integration, separate dedicated pipeline
10. **Lower priority** — Hardlink mode, Web UI, Plex API, library statistics
