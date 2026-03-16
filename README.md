j# medialnk

Scans your `/movies/` and `/tv/` folders, figures out what's what, and builds a parallel symlink tree organized the way media servers expect. 

Movies, TV shows, and miniseries are automatically separated and correctly restructured regardless of how disorganized the source folder is. 

The original (seeding) files stay exactly where/how they are.

Can be used as a one-time library importer, ongoing media library manager, or both. 

```text
What you have (Messy Source):         What medialnk builds (Clean Symlinks):
/movies/                              /movies-linked/
  Some.Movie.2020.1080p.BluRay/         Some Movie (2020)/
  The.Matrix.1999.2160p.REMUX/            Some Movie (2020).mkv -> original
  Mini.Series.S01E01.1080p/             The Matrix (1999)/
  Kill.Bill.Part.1.1080p/                 The Matrix (1999) - 2160P.mkv -> original
                                      /tv-linked/
/tv/                                    Breaking Bad/
  Breaking.Bad.S01.1080p.BluRay/          Season 01/ -> original folder
  Breaking.Bad.S02.720p.WEB-DL/           Season 02/ -> original folder
  Breaking.Bad.S01E04.720p.mkv          Mini Series/
  Fallout.S01E05-E06.1080p.mkv            Season 01/
                                            episode.mkv -> original

```

Run medialnk, point Jellyfin at the `-linked/` directories, done.

**Requires Python 3.11+** (uses stdlib `tomllib`, zero external dependencies).

---
## Main Features 

* **Smart Movie Parsing:** Extracts titles and years from messy scene names, grouping multiple quality versions (e.g., 1080p and 2160p) into the same canonical folder.
* **TV & Bare Episode Handling:** Groups season folders and automatically matches loose, bare episode files (e.g., `S01E05`, `3x05`, `Episode.4`, more) into their correct season directories.
  - Many different file conflict resolutions solved automatically, anything ambiguous will prompt to confirm 
* **Miniseries Detection:** Automatically detects folders in your `/movies/` directory that contain multiple episode files and routes them to your TV library instead.
* **Duplicate Handling:** Prompts you to choose which quality to link when two source folders provide the exact same TV season. (or you can keep both automatically)
* **TMDB Resolution:** Uses a free TMDB API key to resolve messy names to their canonical forms, 
falling back safely if confidence in the match is low.
* **Source File Immutability:** medialnk functionally cannot alter, delete, move, rename or otherwise change the source media files in anyway. It build a separate parallel "working library". 

---

## Why this exists

I spent forver trying to get my stuff automated for easy imports. Turned out it was a giant pain the ass. 

Every existing tool hit at least one wall when dealing with my messy,  manually-downloaded libraries:

- **Arr stack:** Importing an existing library is a pain because Sonarr/Radarr need an already-structured library to start.
- **FileBot:** Renames the **actual files**, which instantly breaks torrent seeding.
- **Hardlinks:** Fail completely on mergerfs pools with `EXDEV: cross-device link not permitted` if the source and destination land on different physical drives.
- **rclone union** and similar tools merge filesystem paths but can't rename, categorize, or restructure anything.
- **Manual Sorting:** Manually sorting thousands of files is unreasonable and insanely time consuming. 

---

## What this is for

medialnk is for people who want a clean media-server library. The output library is a parellel library of symlinks to then pass on to other services (Jellyfin/Arr), while treating your media library (torrents, etc) as an immutable, unchangable source. 

It works well with following situations: 

### One-time library cleanup
 Run medialnk once to turn a disorganized media collection into a clean linked library for Jellyfin or Plex.
  - Works well as a single use organizer. (original purpose)
  - Reads raw scene-named folders/files and builds the right structure automatically

### Ongoing linked-library maintenance

Run medialnk repeatedly as your source library changes.Works well if You **download media manually** and do not want to depend on Sonarr or Radarr for everything (or anything)
  - This will quickly re-organize library as you make changes. 
  - Can run as an automated service [PLANNED], scanning for changes, and outputting changes to an organized, presentation layer for existing library. 
  - medialnk still has full compatability with the Arr stack in almost all cases.
  - There are functions that will "pass-through" already properly strctured files without touching them.

### Companion tool for Sonarr/Radarr
If you already use Sonarr or Radarr, medialnk can still be useful as a safer presentation layer between your source storage and your media server. It does not replace the arr stack's download and upgrade workflow. It complements it.
  - medialnk still has full compatability with the Arr stack in almost all cases.
  - There are functions that will "pass-through" already properly strctured files without touching them.
  
### Safe presentation layer for media servers
medialnk functionally does not have the ability to delete, move, rename, or otherwise change your existing seeding/source library.

Your files need to stay where/how they are because of **torrent seeding**, shared storage, or existing workflows.

medialnk separates:

- **where your files actually live**
- **how Jellyfin/Plex sees them**

That lets your storage stay practical for downloads, seeding, pooling, or archival purposes while the linked library stays clean and media-server friendly.

### Symlink presentation layer

You use **mergerfs**, a different FUSE filesystem, seperate media drives, or pooled storage where hardlink-based workflows break. 
  - If your source file and destination land on different physical drives under the merger, `os.link()` returns `EXDEV: cross-device link not permitted`.
  - Symlinks will always work, regardless of drive, filesystem, or source. 

medialnk is not a downloader and not a replacement for the arr stack. It is a **linked-library organizer and maintenance tool**.

---

## Quick start

```bash
# 1. Create config
cp medialnk.toml ~/.config/medialnk/medialnk.toml
# Edit paths to match your setup

# 2. Preview
python3 -m medialnk sync --dry-run

# 3. Run
python3 -m medialnk sync

# 4. Point Jellyfin at movies-linked/ and tv-linked/
```

---

## Commands

```bash
medialnk sync                  # Full scan + link (movies then TV)
medialnk sync --dry-run        # Preview only
medialnk sync --yes            # Auto-accept all prompts
medialnk sync --tv-only        # Skip movies
medialnk sync --movies-only    # Skip TV
medialnk sync -v               # Verbose output
medialnk sync -vv              # Debug output
medialnk sync -q               # Quiet (errors/warnings only)

medialnk clean                 # Remove broken symlinks
medialnk clean --dry-run       # Preview what would be removed

medialnk validate              # Check config, paths, PathGuard

medialnk test-library /path    # Generate fake library for testing
medialnk test-library /path --reset
```

Global flags:
```bash
medialnk --config /path/to/medialnk.toml sync --dry-run
medialnk --version
```

---

## Configuration

### Config file location

Searched in order:
1. `--config /path/to/medialnk.toml` (CLI flag)
2. `./medialnk.toml` (current directory)
3. `~/.config/medialnk/medialnk.toml`

### Config format (TOML)

```toml
[paths]
media_root_host = "/mnt/storage/data/media"
media_root_container = "/data/media"    # same as host if no Docker
movies_source = "movies"                # relative to media_root_host
tv_source = "tv"
movies_linked = "movies-linked"
tv_linked = "tv-linked"

[tmdb]
api_key = ""                            # or TMDB_API_KEY env var
confidence_check = true

[logging]
log_dir = "logs"
verbosity = "normal"                    # quiet/normal/verbose/debug

[overrides.tv_names]
"The Office US" = "The Office (US)"

[overrides.tv_orphans]
"Season 1" = { show = "Little Bear", season = 1 }
```

### Config vs CLI

- **Config file:** how the installation normally behaves (paths, API keys, overrides)
- **CLI flags:** how this particular run should behave (`--dry-run`, `--yes`, `-v`)

CLI flags override config where they overlap.


---

## Technical Info / How it works [needs updating but this basic idea]

### Movies pipeline

Scans `/movies/`, extracts title and year from folder/file names, groups multi-version entries with quality suffixes, creates symlinks in `/movies-linked/`.

- Skips miniseries folders (2+ episode files) for the TV pipeline
- Flags Part.N folders as ambiguous (prompts for movie vs TV routing)
- TMDB auto-lookup for entries missing a year
- Multi-version: `Movie (Year) - 1080P.mkv`, `Movie (Year) - 2160P.mkv`
- Same-quality duplicates: `Movie (Year) - 1080P.2.mkv`

### TV pipeline (two passes)

**Pass 1:** Scans `/tv/` for season folders, groups by show name, creates season symlinks. Scans `/movies/` for miniseries. Passes through already-structured folders. Prompts on duplicate seasons (different qualities).

**Pass 2:** Handles bare episode files in `/tv/` with no parent folder. Parses show name, resolves via overrides/TMDB/fallback, matches against Pass 1 results. Handles conflicts interactively (quality variants, missing episodes, season conversions).

### Episode format support

| Format | Example | Status |
|--------|---------|--------|
| SxxExx | `Show.S01E05.mkv` | Supported |
| Multi-ep | `Show.S01E05-E06.mkv` | Supported (combined name) |
| NxNN | `Futurama.3x05.mkv` | Supported |
| Episode.N | `Documentary.Episode.4.mkv` | Supported |
| NofN | `Planet.Earth.1of6.mkv` | Folder scan only (not bare files) |
| Bare E01 | `pe.E01.mkv` | Folder detection only |
| Part.N | `Kill.Bill.Part.1.mkv` | Ambiguity prompt (not auto-routed) |

### TMDB confidence checking

TMDB results are validated before acceptance. Short parsed names (1-2 words) require all words present in the result with at most 1 extra word. Longer names require 50%+ word overlap. Rejected matches fall back to the parsed name and print `[TMDB] Rejected`.

### Source protection


Immutability of the existing source library is a main design trait. Two independent layers prevent your original media from being modified:

1. **File Path Protection (runtime):** Every filesystem write goes through guarded functions. Writes to source directories raise `SourceProtectionError` and crash immediately.

2. **Output validation (startup):** Output directories are scanned for real video files on every run. If found, the user is warned and prompted before any work begins.

There is no functional ability for medialnk to delete or modify the source (seeding) files in any way. This includes misconfiguration. 

Source files are never read for content, only filenames and sizes.

---

---

## Manual Overrides

### TV name overrides

Fix show names that parse wrong or don't match TVDB:

```toml
[overrides.tv_names]
"The Office US" = "The Office (US)"
"Mystery Science Theater" = "Mystery Science Theater 3000"
```

### TV orphan overrides

Map bare "Season N" folders with no show name:

```toml
[overrides.tv_orphans]
"Season 1" = { show = "Little Bear", season = 1 }
"dvdrip_full_season" = { show = "Good Eats", season = 4 }
```

Run `medialnk sync --dry-run -v` to identify what needs overrides.

---

## Recommended workflow

```bash
# Preview
medialnk sync --dry-run -v > dry-run.txt

# Review, add overrides, repeat until clean

# Run for real
medialnk sync

# Point Jellyfin/Plex at movies-linked/ and tv-linked/

# Arr import: manual import against linked dirs with rename OFF
# Re-enable rename after initial import
```

### Automated/scheduled runs

```bash
medialnk sync --yes -q    # auto-accept, quiet output
```

Per-run logs are written to the configured `log_dir` regardless of console verbosity.

---

## File structure

```
medialnk/
  __init__.py       Package version
  __main__.py       Entry point (python3 -m medialnk)
  cli.py            Subcommands, logger, arg parser
  config.py         TOML loading, validation
  common.py         Regex, PathGuard, filesystem helpers
  movies.py         Movie scanning + linking
  tv.py             TV scanning + linking
  resolver.py       TMDB lookups, confidence checking
  test_library.py   Fake library generator
medialnk.toml       Default config template
```

---

## Testing

```bash
# Generate test library with matching config
medialnk test-library /tmp/test-lib

# Dry run against it
medialnk --config /tmp/test-lib/medialnk.toml sync --dry-run -v

# Live run (auto-accept)
medialnk --config /tmp/test-lib/medialnk.toml sync --yes -v

# Validate
medialnk --config /tmp/test-lib/medialnk.toml validate
```

The test library covers all parsing scenarios: multi-version movies, miniseries, Part.N ambiguity, duplicate seasons, bare episode files in every supported format, apostrophe variations, trailing years, pass-through folders, orphan overrides, sample files, and all recognized video extensions.
