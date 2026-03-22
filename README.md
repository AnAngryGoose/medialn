# medialnk

Scans your `/movies/` and `/tv/` source folders, parses scene-named files and folders, and builds a clean symlink tree organized the way Jellyfin and Plex expect. Your original files are never touched.

```
Source (messy, seeding):              Presentation layer (clean symlinks):

/movies/                              /movies-linked/
  Some.Movie.2020.1080p.BluRay/         Some Movie (2020)/
  The.Matrix.1999.2160p.REMUX/            Some Movie (2020).mkv -> source
  Mini.Series.S01E01.1080p/             The Matrix (1999)/
                                           The Matrix (1999) - 2160P.mkv -> source
/tv/                                  /tv-linked/
  Breaking.Bad.S01.1080p.BluRay/        Breaking Bad/
  Breaking.Bad.S02.720p.WEB-DL/           Season 01/ -> source folder
  Breaking.Bad.S01E04.720p.mkv             Season 02/ -> source folder
  Futurama.3x05.720p.mkv               Mini Series/
                                          Season 01/
                                            Mini.Series.S01E01 - 1080P.mkv -> source
```

Point Jellyfin at `movies-linked/` and `tv-linked/`. Done.

---

## Why

**Symlinks over hardlinks.** Hardlinks break on mergerfs when source and destination land on different physical drives (`EXDEV: cross-device link not permitted`). Symlinks follow the union mount path and always work.

**Source files are immutable.** Torrents keep seeding from unchanged paths. The presentation layer is entirely disposable. Delete it and rebuild in seconds. This is enforced at the compiler level via `SafePath` — write functions cannot accept raw string paths, so source directories are unreachable by construction, not by convention.

**Fills the arr gap.** Radarr and Sonarr manage content they downloaded. They can't import a years-old disorganized library without something first organizing it. medialnk handles everything outside arr's awareness: manually grabbed torrents, legacy libraries, specific encodes. Radarr manages its own downloads. medialnk manages everything else. Jellyfin points at both. No collisions.

---

## Install

**Download a release binary:**
```bash
mv medialnk /usr/local/bin/medialnk
chmod +x /usr/local/bin/medialnk
```

**Build from source:**
```bash
git clone https://github.com/AnAngryGoose/medialnk
cd medialnk
go build -o medialnk .
```

---

## Quick Start

```bash
cp medialnk.toml ~/.config/medialnk/medialnk.toml
# Edit paths to match your setup

medialnk sync --dry-run -v    # preview first
medialnk sync                  # run for real
```

Dry run first, review output, add overrides to config if needed, then run live.

---

## Features

**Movie pipeline**
- Parses scene-format names to extract title and year
- Groups multiple quality versions of the same film under one canonical folder
- Detects miniseries misplaced in `/movies/` and routes them to TV automatically
- Resolves yearless entries via TMDB; flags Part.N folders as ambiguous with a prompt

**TV pipeline (two-pass)**
- Pass 1: groups season folders by show name, creates season symlinks or passthroughs
- Pass 2: handles bare episode files scattered in source with no parent folder
- Supported formats: `S01E05`, `S01E05-E06` (multi-ep combined), `3x05`, `Episode.4`
- Duplicate seasons at different qualities prompt for a choice
- Conflict resolution converts season symlinks to real directories when individual episode links are needed

**TMDB resolution**
- Resolves show and movie names to canonical titles
- Confidence checking prevents false matches; falls back to parsed name when uncertain
- Transient network failures do not poison the cache

**Source protection**
- `SafePath` is a Go type that can only be constructed from validated output paths
- All filesystem write functions accept only `SafePath`, never raw strings
- Source directories cannot be reached by any write path — compile-time enforcement, not runtime

**State tracking**
- After every real sync, `.medialnk-state.json` is written to each output directory
- Records everything linked, skipped, flagged, and unmatched with timestamps
- Dry runs never write or update state files

**Orphan scanner**
- Walks output symlinks to find source files with no corresponding link
- Reports coverage percentage per pipeline
- Runs automatically after sync; also available standalone via `medialnk orphans`

**Health checks**
- Before sync, validates that source directories meet a minimum video file count
- Optional sentinel file check catches silently unmounted mergerfs drives
- Aborts with a clear error rather than syncing against an empty mount point

**Watch mode (daemon)**
- Polls source directories for new content and auto-syncs when downloads complete
- Three-layer detection: debounce timer, file size stability check, incomplete download markers (`.!qB`, `.part`, `.aria2`, `_UNPACK_`)
- Non-interactive: ambiguous items are skipped and logged for manual review, not auto-accepted
- Optional HTTP health endpoint for Docker HEALTHCHECK and external monitoring
- Polling-based (not inotify) for reliable operation on MergerFS/FUSE filesystems

**Config overrides**
- Movie title overrides for names that parse incorrectly
- TV name overrides for canonical show name corrections
- TV orphan overrides for bare `Season N` folders with no parseable show name

---

## Commands

```bash
medialnk sync                  # Full scan and link
medialnk sync --dry-run        # Preview only, nothing written
medialnk sync --yes            # Auto-accept all prompts
medialnk sync --tv-only        # Skip movies pipeline
medialnk sync --movies-only    # Skip TV pipeline
medialnk sync -v / -vv         # Verbose / debug output
medialnk sync -q               # Quiet (errors and warnings only)

medialnk clean                 # Remove broken symlinks from output dirs
medialnk clean --dry-run

medialnk orphans               # Report source files with no symlink
medialnk orphans --json        # Machine-readable output
medialnk orphans -q            # Counts only

medialnk validate              # Check config, paths, and PathGuard

medialnk watch                 # Daemon: poll source dirs, auto-sync new content
                               # Requires [watch] enabled = true in config

medialnk test-library /path    # Generate a fake library for testing
medialnk test-library /path --reset

medialnk --config /path/to/medialnk.toml sync --dry-run
medialnk --version
```

---

## Configuration

Config is searched in order: `--config` flag, `./medialnk.toml`, `~/.config/medialnk/medialnk.toml`.

```toml
[paths]
media_root_host = "/mnt/storage/data/media"
media_root_container = "/data/media"    # same as host if not using Docker
movies_source = "movies"
tv_source = "tv"
movies_linked = "movies-linked"
tv_linked = "tv-linked"

[tmdb]
api_key = ""                            # optional, or set TMDB_API_KEY env var
confidence_check = true

[health]
enabled = true
min_source_files = 10                   # abort sync if source has fewer video files
sentinel_file = ""                      # optional: path that must exist before syncing
port = 0                                # HTTP health endpoint port, 0 = disabled (for watch mode)

[sync]
clean_after_sync = false                # remove broken symlinks automatically after sync

[watch]
enabled = false                         # must be true for `medialnk watch` to start
debounce_seconds = 30                   # wait after detecting new content before processing
poll_interval_seconds = 60              # how often to scan source directories

[logging]
log_dir = "logs"
verbosity = "normal"                    # quiet / normal / verbose / debug

[overrides.movie_titles]
"Some Parsed Title" = "Correct Title"

[overrides.tv_names]
"The Office US" = "The Office (US)"

[overrides.tv_orphans]
"Season 1" = { show = "Little Bear", season = 1 }
```

---

## Automated runs

**Watch mode (recommended):**
```bash
# Start the daemon — polls source dirs, auto-syncs when new content arrives
medialnk watch

# Ambiguous items (Part.N, quality conflicts) are skipped and logged for manual review
# HTTP health endpoint available when [health] port is set (for Docker HEALTHCHECK)
```

**One-shot (cron or torrent client hook):**
```bash
# qBittorrent completion hook
medialnk sync --yes -q

# Per-run logs are written to log_dir regardless of console verbosity
```

---

## Testing

```bash
medialnk test-library /tmp/test-lib
medialnk --config /tmp/test-lib/medialnk.toml sync --dry-run -v
medialnk --config /tmp/test-lib/medialnk.toml sync --yes -v
medialnk --config /tmp/test-lib/medialnk.toml sync --dry-run -v    # idempotency check
```

The test library covers multi-version movies, miniseries, Part.N ambiguity, duplicate seasons, bare episode files in all supported formats, pass-through folders, and orphan overrides.

---

## More detail

Architecture, pipeline internals, function reference, and the full planned roadmap are in `OVERVIEW.md`.