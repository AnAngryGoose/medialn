# medialnk Project Overview

**Version:** 1.0.0
**Repository:** https://github.com/AnAngryGoose/medialnk
**Language:** Python 3.11+ (stdlib only, zero dependencies)


## What This Project Is

medialnk is a symlink-based media library manager. It creates a clean, organized presentation layer for media servers (Jellyfin, Plex) by building a parallel directory tree of symlinks from disorganized source folders. The source files are never modified, moved, renamed, or deleted.

This is fundamentally different from other media organization tools. Rather than restructuring your existing library, medialnk creates a second layer on top of it. Your original files stay exactly where they are. Your torrent client keeps seeding from the same paths. The symlink layer is what your media server reads. If something goes wrong, you delete the symlink directories and run again. Your actual media was never at risk.


## The Two-Layer Architecture

This is the core concept that makes medialnk unique.

**Source layer (immutable).** Your actual media files on disk. Scene-named torrent downloads, bare files, mixed content, however it arrived. medialnk reads filenames and folder structures from this layer but never writes to it. This is enforced at runtime by PathGuard, not just by convention.

**Presentation layer (managed).** A parallel set of directories (`movies-linked/`, `tv-linked/`) containing nothing but symlinks. These symlinks point back to the source layer and are organized the way Jellyfin, Plex, and the arr stack expect: `Movie Name (Year)/Movie Name (Year).mkv`, `Show Name/Season 01/Show.S01E01.mkv`. This is what your media server sees.

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
    Breaking.Bad.S01E02.1080p.mkv          Season 02/ -> source folder
  Breaking.Bad.S02.720p.WEB-DL/         The Simpsons (1989) {tvdb-71663}/
    ...                                    -> source (passed through as-is)
  Breaking.Bad.S01E07.1080p.mkv         Futurama/
  Futurama.3x05.720p.mkv                  Season 03/
                                             Futurama.S03E05 - 720P.mkv -> source
```

The separation means:
- Deleting or rebuilding the presentation layer has zero impact on source files
- Torrent seeding continues from original paths without interruption
- Multiple presentation layers could theoretically exist for different servers
- The source layer can grow chaotically without affecting the organized view
- Rollback is instant: delete the linked directories, run medialnk again


## Why This Exists

Every existing tool for organizing a media library hits at least one of these walls:

**Arr stack (Radarr, Sonarr)** can rename and organize files, but needs an already-structured library to start with. If your library is a flat pile of scene-named torrents, Radarr can't even import them. You need to organize before you can use the organizer. Chicken and egg.

**FileBot / TinyMediaManager** rename actual files. That breaks torrent seeding because the torrent client expects the original filenames and paths. FileBot has a `--action symlink` mode, but it still requires you to manually sort movies from TV across your entire library before it can match anything.

**rclone union** and similar mount-merging tools combine filesystem paths but cannot rename, categorize, or restructure content. The view is just a merge of the same mess.

**Hardlink approaches** (standard recommendation from TRaSH Guides) fail on mergerfs pools. When source and destination land on different physical drives under the merger, `os.link()` fails with `EXDEV: cross-device link not permitted`. Symlinks follow a path through the unified mount and do not have this limitation.

**Manual sorting** is what everyone falls back to. Spend a weekend moving files into folders. Miss some. Do it again when new content arrives. Not sustainable.

medialnk addresses all of these:
- No file renaming or moving. Symlinks only. Seeding continues.
- Automatic content detection: movies, TV, miniseries separated without manual sorting.
- Works on mergerfs. Symlinks resolve through the union mount.
- One-time import or ongoing scheduled management. Both are first-class.
- Ambiguous content (Part.N folders that could be movies or miniseries) is flagged for human decision rather than silently misrouted.
- Nothing in the presentation layer is ever changed without the user confirming it.

### Design Policy

medialnk prioritizes building a clean, canonical library for media servers (Jellyfin/Plex). Compatibility with Sonarr/Radarr remains a design requirement, but medialnk will not contort its core model around arr-specific assumptions when those conflict with a cleaner standalone library model.

In practice:
1. Folder and file naming follows canonical media-server conventions
2. No unnecessary arr-hostile behavior
3. When forced to choose, prefer direct library correctness over arr convenience


## Architecture

### Package Layout

```
medialnk/
  __init__.py         Version constant
  __main__.py         Entry point (python3 -m medialnk)
  cli.py              Subcommand dispatch, Logger class, argument parsing
  config.py           TOML loading, path resolution, validation
  common.py           Regex patterns, PathGuard, guarded fs ops, helpers
  resolver.py         TMDB lookups, confidence checking, caching
  movies.py           Movie scanning, categorization, linking
  tv.py               TV scanning, bare file handling, conflict resolution
  test_library.py     Fake library generator
medialnk.toml         Config template
```

### Data Flow

```
                  Config (medialnk.toml)
                         |
                    cli.py main()
                    /          \
            movies.run()     tv.run()
               |                |
         [movies_source]   [tv_source] + [movies_source]
           read only          read only (miniseries scan)
               |                |
         [movies_linked]   [tv_linked]
           write only        write only
```

Both pipelines receive a Config object, read from source directories, and write symlinks to output directories. The movies pipeline runs first. The TV pipeline reads from both `/tv/` (primary) and `/movies/` (for miniseries detection).

### Execution Flow

```
1. Load config from TOML
2. Parse CLI args, apply overrides
3. init_guard(sources, outputs) -- lock PathGuard
4. validate_output_dir() for each output -- check for real files
5. movies.run() -- scan, categorize, link (unless --tv-only)
6. tv.run() -- scan, group, bare files, conflicts (unless --movies-only)
7. Write log file
8. Print summary, exit
```

### Subcommands

| Command | What it does |
|---------|-------------|
| `sync` | Full scan and link run. Accepts `--dry-run`, `--yes`, `--tv-only`, `--movies-only`, `-v/-vv`, `-q` |
| `clean` | Removes broken symlinks from all output dirs and prunes empty directories |
| `validate` | Checks config validity, source paths, output dir content, PathGuard configuration |
| `test-library` | Generates a fake source library under a given path for testing |

### Two-Pass TV Processing

The TV pipeline processes content in two passes to handle the interaction between folder-based seasons and loose bare files.

**Pass 1: Folder-based sources.** Scans `/tv/` for season folders (e.g. `Show.Name.S01.1080p.BluRay/`), groups them by normalized show name, and creates season-level symlinks. Also scans `/movies/` for miniseries (folders with 2+ episode files). Folders already in Jellyfin structure are passed through. This pass produces the `grouped` dict that maps show names to their season folders, which Pass 2 needs for matching.

**Pass 2: Bare episode files.** Scans `/tv/` for video files sitting directly in the folder with no parent. For each file, it parses show name, season, episode, and quality from the filename. Resolves the show name via overrides, TMDB, or parsed fallback. Matches against Pass 1 results using aggressive fuzzy normalization. Categorizes each file as new, conflicting, or unmatched. New episodes get linked automatically. Conflicts are resolved interactively (or auto-accepted with `--yes`). Unmatched files are reported for manual handling.

The two-pass design exists because bare files need to know what folder-based structure already exists before they can be placed correctly. A bare `Breaking.Bad.S01E07.1080p.mkv` needs to know that Breaking Bad Season 01 is already linked from a folder so it can detect the conflict and prompt for season conversion rather than creating a duplicate.


## Immutability System

### Why It Exists

The project's core rule is that source media files are never modified, moved, or deleted under any circumstance. This protects torrent seeding, prevents data loss from config mistakes, and makes the entire presentation layer disposable without risk.

This rule is enforced in code, not by convention. A comment saying "don't write here" doesn't stop a bug from writing there. PathGuard does.

Source files are structurally unreachable by any write path in the codebase. There is no code path that could rename, delete, or overwrite a source file regardless of how it is called. This is a hard architectural guarantee, not a policy.

### Level 1: PathGuard (Runtime Enforcement)

Every filesystem write operation in the entire codebase flows through one of four guarded functions: `safe_remove()`, `safe_rmdir()`, `safe_makedirs()`, `safe_symlink()`. Each calls `PathGuard.assert_writable()` before the actual OS call.

The guard maintains two lists: source paths (protected) and output paths (writable). Any write targeting a source path raises `SourceProtectionError` and crashes the process immediately. Any write targeting a path outside all registered directories also raises, preventing accidental writes to random filesystem locations.

At `lock()` time, the guard also rejects configurations where an output directory is inside or equal to a source directory.

No other code in the codebase calls `os.remove`, `os.rmdir`, `os.makedirs`, or `os.symlink` directly. The four `safe_*` functions are the only write path.

Note on `safe_symlink`: only the `link_path` (the new symlink being created) is guarded. The symlink target is the source file path, which is read-only. Writing to a path and pointing to a path are different operations. The guard prevents writing the symlink into a source directory; it does not and cannot affect where the symlink points.

### Level 2: Output Validation (Config Mistake Detection)

On every startup, `validate_output_dir()` walks each output directory and checks for real (non-symlink) video files. If any are found, it means the output directory might actually be the user's real media library (a config mistake). The guard can't catch this because the user told it the path is an output. But the content scan reveals that real media is already there.

The user is warned, shown up to 10 of the offending files, and prompted. The default response is abort. In dry-run mode, the warning is shown but execution continues since dry-run can't modify anything.

### Startup Sequence

```
1. init_guard(sources=[movies_source, tv_source],
              outputs=[movies_linked, tv_linked])
     - Registers all paths, locks the guard
     - Rejects output-inside-source or output-equals-source

2. validate_output_dir(movies_linked, dry_run)
   validate_output_dir(tv_linked, dry_run)
     - Scans for real video files in output dirs
     - Warns and prompts if found

3. Normal execution begins
     - Every write goes through safe_* functions
     - Guard validates every single one
```


## Function Reference

### common.py (Foundation)

#### Regex Patterns

| Pattern | Matches | Example | Used By |
|---------|---------|---------|---------|
| `RE_SXXEXX` | S01E01 format | `Breaking.Bad.S01E01.1080p.mkv` | Highest priority in all episode detection |
| `RE_XNOTATION` | 1x01 format | `Futurama.3x05.720p.mkv` | Both pipelines |
| `RE_EPISODE` | Episode.N format | `Documentary.Episode.4.mkv` | Both pipelines |
| `RE_NOF` | NofN format | `Planet.Earth.1of6.mkv` | Folder scanning only |
| `RE_BARE_EPISODE` | Bare E01 | `pe.E01.1080p.mkv` | Folder detection only |
| `RE_MULTI_EP` | Multi-ep continuation | `-E06` after `S01E05` | TV bare file parser |
| `RE_PART` | Part.N / Pt.N | `Kill.Bill.Part.1.mkv` | Ambiguity detection, optional in episode_info |
| `RE_SAMPLE` | Sample files | `sample.mkv` | Exclusion filter |
| `RE_QUALITY` | Quality tags | `1080p`, `REMUX`, `BluRay` | Quality labeling |
| `RE_ILLEGAL` | Illegal filename chars | `:`, `?`, `*` | Sanitization |

#### PathGuard System

| Name | Purpose |
|------|---------|
| `SourceProtectionError` | Exception class. Write targeted a protected path. Hard crash. |
| `PathGuard` | Class. Holds source/output lists. Validates every write after lock. |
| `PathGuard.register_source(path)` | Mark directory as protected. Before lock only. |
| `PathGuard.register_output(path)` | Mark directory as writable. Before lock only. |
| `PathGuard.lock()` | Finalize. Rejects output-inside-source. No more registrations. |
| `PathGuard.assert_writable(path)` | Check a path before writing. Raises on source or unregistered. |
| `init_guard(sources, outputs)` | Create module-level guard, register, lock. Called once at startup. |
| `get_guard()` | Return the module-level guard for inspection or testing. |

#### Guarded Write Functions

| Function | What it wraps |
|----------|--------------|
| `safe_remove(path)` | `os.remove()` after `assert_writable()` |
| `safe_rmdir(path)` | `os.rmdir()` after `assert_writable()` |
| `safe_makedirs(path)` | `os.makedirs()` after `assert_writable()` |
| `safe_symlink(target, link_path)` | `os.symlink()`. Only `link_path` is guarded. Target is the source file path (read reference only). |

#### Detection Functions

| Function | Purpose |
|----------|---------|
| `is_video(filename)` | True if extension is .mkv, .mp4, .avi, .ts, or .m4v |
| `is_sample(filename)` | True if "sample" appears as a word boundary in filename |
| `episode_info(filename, include_part=True)` | Returns (season, episode) tuple or None. `include_part=False` excludes Part.N to avoid false positives on multi-part films. |
| `is_episode_file(filename, include_part=True)` | Boolean wrapper around `episode_info()` |
| `extract_quality(name)` | Returns first quality tag found, uppercased ("1080P", "REMUX"), or None |
| `sanitize(name)` | Replaces Windows/SMB-illegal characters with `-` |
| `clean_passthrough_name(folder_name)` | Dots to spaces (when no spaces exist), whitespace normalization. Does NOT strip years, metadata, or canonicalize. For pass-through folders. |

#### Symlink and Directory Functions

| Function | Purpose |
|----------|---------|
| `host_to_container(path, host_root, container_root)` | Translates host path to Docker container path for symlink targets |
| `make_symlink(link_path, target, dry_run, host_root, container_root)` | Creates absolute container-side symlink. Returns True if created, False if already exists. |
| `ensure_dir(path, dry_run)` | Creates directory and parents. No-op in dry-run. |
| `symlink_target_exists(link_path, host_root, container_root)` | Checks if symlink target exists, translating container paths to host paths first |
| `clean_broken_symlinks(directory, host_root, container_root)` | Removes broken symlinks and prunes empty dirs in an output directory. Returns count removed. |
| `validate_output_dir(directory, dry_run)` | Scans output dir for real video files. Warns and prompts if found. |
| `find_videos(folder, exclude_episodes, exclude_samples)` | Lists video files in a folder with optional filtering |
| `largest_video(videos)` | Returns largest file by size from DirEntry list |
| `prompt_choice(message, valid)` | Interactive prompt loop. Returns validated choice. |

---

### config.py (Configuration)

| Name | Purpose |
|------|---------|
| `Config` | Class. Holds all resolved, validated settings for a run. All paths absolute. |
| `Config.validate()` | Checks that host_root and source directories exist. Returns error list. |
| `Config.summary()` | Human-readable config dump for log output. |
| `find_config(cli_path)` | Searches for config file: CLI path, then `./medialnk.toml`, then `~/.config/medialnk/medialnk.toml`. |
| `load_config(cli_path)` | Loads TOML, constructs Config. Returns (Config, path). |

Config fields loaded but not yet wired into pipeline logic:

| Field | Config key | Status |
|-------|-----------|--------|
| `movie_title_overrides` | `[overrides.movie_titles]` | Loaded from TOML, stored on Config, not applied in movies.py yet |

---

### resolver.py (TMDB and Name Resolution)

| Function | Purpose |
|----------|---------|
| `_word_overlap(parsed, result)` | Word-set confidence check. Short names: all words + max 1 extra. Long names: 50%+ overlap. |
| `tmdb_search_tv(name, api_key, confidence, log)` | TMDB TV search. Returns canonical title string or None. Cached per run. |
| `tmdb_search_movie(title, api_key, confidence, log)` | TMDB movie search. Returns (title, year) tuple or None. Cached per run. |
| `resolve_tv_name(parsed, overrides, api_key, confidence, log)` | Resolution chain: overrides dict, then TMDB with confidence, then parsed fallback. |
| `clear_cache()` | Clears TMDB result cache. For testing. |

---

### movies.py (Movie Pipeline)

| Function | Purpose |
|----------|---------|
| `_year(name)` | Extracts 4-digit year requiring a preceding separator (prevents "1917" misread) |
| `_title(name)` | Strips extension, quality tags, date prefixes, unclosed brackets. Returns clean title. |
| `_is_miniseries(folder)` | True if folder has 2+ episode-pattern video files. Used to skip for TV pipeline. |
| `_is_ambiguous_parts(folder)` | True if folder has 2+ Part.N files with no standard episode markers. Returns (bool, file_list). |
| `scan(cfg)` | Main scanner. Categorizes every entry in movies_source as movie, flagged, skipped (miniseries), or ambiguous (Part.N). Groups multi-version movies. Returns 4 lists. |
| `_resolve_versions(seen)` | Turns grouped entries into flat list. Single versions: no label. Multiple: quality suffixes. Same-quality duplicates: `.2`, `.3`. |
| `run(cfg, dry_run, auto, log)` | Full pipeline. Scan, link, handle flagged/ambiguous, TMDB resolve. Returns counts dict. |
| `_route_movie(entry, title, year, cfg, dry_run, log)` | Symlinks an ambiguous Part.N folder as a movie (picks largest video file). |
| `_tmdb_resolve(no_year, cfg, dry_run, log)` | Concurrent TMDB lookup (ThreadPoolExecutor, 8 workers) for yearless entries. |

---

### tv.py (TV Pipeline)

#### Parsing

| Function | Purpose |
|----------|---------|
| `_show_season(folder, overrides)` | Parses `Show.Name.S01.720p...` into (show_name, season_num). Applies name overrides. |
| `_clean_show(folder)` | Strips quality/codec/release tokens for display name. More aggressive than passthrough cleanup. |
| `_is_bare_ep_folder(folder)` | True if folder has 2+ bare E\d+ entries. Detects non-standard episode folders. |
| `parse_bare_episode(filename)` | Returns (show, season, episode, quality, second_ep) or None. Handles SxxExx+multi-ep, NxNN, Episode.N. |
| `build_link_name(show, season, ep, quality, ext, second_ep)` | Constructs standardized symlink filename. Handles multi-ep naming (S01E05-E06). |

#### Name Matching

| Function | Purpose |
|----------|---------|
| `_norm_key(name)` | Light normalization for Pass 1 grouping: lowercase, strip apostrophes, normalize whitespace. |
| `_norm_match(name)` | Aggressive normalization for cross-source matching: additionally strips articles, studio prefixes, trailing years, all punctuation. |
| `_find_match(show, grouped, tv_linked)` | Two-stage lookup: check grouped dict, then scan existing tv-linked/ on disk. Uses `_norm_match`. |

`_norm_key` and `_norm_match` are intentionally separate. Pass 1 grouping uses `_norm_key` (light) so that "Schitt's Creek" and "Schitts Creek" are grouped together without accidentally merging different shows that only differ in articles. Cross-source matching in Pass 2 uses `_norm_match` (aggressive) because bare files and folder names from different releases need to find the same canonical show even with significant surface differences in naming.

#### Episode State

| Function | Purpose |
|----------|---------|
| `_ep_in_folder(folder, ep, season)` | Checks if a specific episode exists in a source folder. Returns (exists, quality). |
| `_ep_symlink_exists(season_dir, ep, season)` | Checks if an episode symlink already exists in a converted season dir. For idempotency. |
| `_convert_season(show, snum, path, cfg, dry_run, log)` | Replaces season symlink with real directory, re-links all source episodes individually. Inherits quality from folder name for files without their own tag. |

#### Scanners

| Function | Purpose |
|----------|---------|
| `_scan_tv(cfg)` | Pass 1. Groups season folders by show name, identifies pass-through and bare-episode folders. Returns (grouped, passthrough). |
| `_scan_miniseries(cfg)` | Scans movies_source for 2+ episode folders (excludes Part.N). Returns show -> (folder, episodes). |
| `_resolve_dupes(show, seasons, dry_run, auto, log)` | Prompts user to choose quality when duplicate season folders exist. Auto/dry-run picks first. |
| `_scan_bare(grouped, cfg, log)` | Pass 2. Categorizes bare video files in tv_source as new, conflict, or unmatched. |

#### Handlers

| Function | Purpose |
|----------|---------|
| `_handle_new(new, cfg, dry_run, log)` | Creates show/season dirs and episode symlinks for new content. No prompts needed. |
| `_handle_conflicts(conflicts, cfg, dry_run, auto, log)` | Interactive conflict resolution. Checks if season was already converted by a prior conflict. Three types: quality variant, missing episode, bare_dir (season dir already exists). |

#### Warnings

| Function | Purpose |
|----------|---------|
| `_norm_compare(name)` | Strips year/TVDB tags/quality for overlap detection between grouped and pass-through. |
| `_warnings(grouped, pt)` | Detects duplicate seasons and name overlaps. Returns warning strings. |

#### Pipeline

| Function | Purpose |
|----------|---------|
| `run(cfg, dry_run, auto, log)` | Full TV pipeline: Pass 1, pass-through, miniseries, Pass 2 (bare new + conflicts), warnings, summary. Returns counts dict. |

---

### cli.py (Interface)

| Name | Purpose |
|------|---------|
| `Logger` | Class with quiet/normal/verbose/debug levels. Optionally writes to log file (always at verbose level). |
| `cmd_sync(args)` | Subcommand handler. Loads config, inits guard, validates, runs movies then TV, writes log. |
| `cmd_clean(args)` | Subcommand handler. Removes broken symlinks from all output dirs. |
| `cmd_validate(args)` | Subcommand handler. Checks config, paths, output dir content, PathGuard validity. |
| `cmd_test_library(args)` | Subcommand handler. Generates fake test library with matching config. |
| `build_parser()` | Constructs argparse with sync/clean/validate/test-library subcommands. |
| `main()` | Entry point. Parses args, dispatches to subcommand handler. |


## Bare File Handling Matrix

### Formats Parsed by parse_bare_episode()

| # | Source file | Parsed result | Output symlink | Notes |
|---|------------|---------------|----------------|-------|
| 1 | `Breaking.Bad.S01E01.720p.WEB-DL.mkv` | show="Breaking Bad", S01, E01, 720P, None | `Breaking Bad.S01E01 - 720P.mkv` | Standard SxxExx. Most common format. |
| 2 | `Show.Name.S02E03.mkv` | show="Show Name", S02, E03, None, None | `Show Name.S02E03.mkv` | No quality in filename, no quality tag in output. |
| 3 | `Fallout.S01E05-E06.1080p.mkv` | show="Fallout", S01, E05, 1080P, second_ep=6 | `Fallout.S01E05-E06 - 1080P.mkv` | Multi-episode. Combined name recognized by Jellyfin/Sonarr. |
| 4 | `Show.S01E05E06.1080p.mkv` | show="Show", S01, E05, 1080P, second_ep=6 | `Show.S01E05-E06 - 1080P.mkv` | Alternative multi-ep format (no dash). Same output. |
| 5 | `Futurama.3x05.720p.mkv` | show="Futurama", S03, E05, 720P, None | `Futurama.S03E05 - 720P.mkv` | NxNN format. Season extracted from number before x. |
| 6 | `Some.Documentary.Episode.4.1080p.mkv` | show="Some Documentary", S01, E04, 1080P, None | `Some Documentary.S01E04 - 1080P.mkv` | Episode.N format. Season defaults to 1. |
| 7 | `Bluey.2018.S01E05.1080p.mkv` | show="Bluey" (2018 stripped), S01, E05, 1080P | `Bluey.S01E05 - 1080P.mkv` | Trailing year stripped from show name to match folder parsing. |
| 8 | `The.Office.US.S01E01.720p.mkv` | show="The Office (US)" (via override) | `The Office (US).S01E01 - 720P.mkv` | NAME_OVERRIDE applied before any other resolution. |
| 9 | `Marvels.Spidey.and.His.Amazing.Friends.S01E01.1080p.mkv` | show varies (TMDB or parsed) | TMDB: `Spidey and His Amazing Friends.S01E01 - 1080P.mkv`. No TMDB: `Marvels Spidey and His Amazing Friends.S01E01 - 1080P.mkv` | TMDB strips studio prefix via canonical name. Without TMDB, falls back to parsed. |
| 10 | `Old.Show.S01E01.HDTV.ts` | show="Old Show", S01, E01, HDTV, None | `Old Show.S01E01 - HDTV.ts` | .ts extension preserved. |
| 11 | `Retro.Show.S02E03.480p.m4v` | show="Retro Show", S02, E03, 480P, None | `Retro Show.S02E03 - 480P.m4v` | .m4v extension preserved. |

### Formats NOT Parsed (Land in UNMATCHED)

| # | Source file | Why | Workaround |
|---|------------|-----|------------|
| 12 | `Planet.Earth.1of6.720p.mkv` | NofN format: title boundary is ambiguous. "Planet.Earth" runs directly into "1of6" with no clean separator. Parsing would require external metadata to know where the title ends. | Add to tv_orphans override or place manually. |
| 13 | `random_video_no_pattern.mkv` | No recognizable episode marker anywhere in the filename. | Manual placement. |
| 14 | `sample.mkv` | Filtered out by `is_sample()` before parsing reaches it. Not a real episode. Expected behavior, does not appear in unmatched list. | N/A. |

### Conflict Scenarios

| # | Situation | Source state | What happens | Output after resolution |
|---|-----------|-------------|--------------|------------------------|
| 15 | Same episode, same quality, already covered | Season folder linked as symlink, contains S01E01 at 1080p. Bare file `Show.S01E01.1080p.mkv` exists. | Silently skipped. The folder symlink already provides this episode at this quality. No output, no prompt. | No change. |
| 16 | Same episode, different quality | Season folder has S01E01 at 1080p. Bare file `Show.S01E01.720p.mkv` exists. | Prompts to convert season symlink to real directory. On confirm: original folder episodes re-linked individually with quality tags, new quality version added alongside. | `Show.S01E01 - 1080P.mkv` + `Show.S01E01 - 720P.mkv` in real Season dir. |
| 17 | Episode missing from season folder | Season folder has E01-E03. Bare file `Show.S01E07.1080p.mkv` exists. | Same conversion prompt. Folder episodes re-linked, missing episode added. | E01-E03 (from folder) + E07 (from bare file) in real Season dir. |
| 18 | Multiple conflicts, same season | Season folder has E01-E03. Bare files: E01 at 720p, E07, E08. | First conflict (E01 quality) converts the symlink. Second and third (E07, E08) detect season is already a real dir and add directly without re-prompting for conversion. | All episodes present. No crash on subsequent conflicts (was BUG-01 in v0.24). |
| 19 | Season already converted from previous run | Previous run converted season to real dir. New bare file arrives on next run. | Detected as bare_dir type. Prompts to add episode directly. No conversion attempt. | Episode added to existing real dir. |
| 20 | Duplicate season, different quality folders | `Fallout.S01.1080p.BluRay/` and `Fallout.S01.2160p.WEB-DL/` both exist. | User prompted to choose which quality to link. Each option shows folder name and detected quality. Dry-run and auto mode pick first. | Selected quality folder symlinked. Other ignored. |
| 21 | Re-run with everything already linked | All shows, seasons, and bare files already processed. | Every make_symlink call returns False (already exists). No prompts, no changes. Run completes silently. | No change. Idempotent. |

### Pass 1 (Folder) Scenarios

| # | Situation | Source | Output |
|---|-----------|--------|--------|
| 22 | Standard season folder | `Breaking.Bad.S01.1080p.BluRay.x264-GROUP/` | `Breaking Bad/Season 01/` -> source folder |
| 23 | Multi-season grouping | `Show.S01.../` + `Show.S02.../` | Both under `Show Name/` |
| 24 | Apostrophe variation | `Schitts.Creek.S01.../` + `Schitt's Creek.S02.../` | Both under `Schitt's Creek/` |
| 25 | Trailing year in folder name | `Bluey.2018.S01.1080p.WEB-DL/` | Year stripped, show = "Bluey" |
| 26 | Pass-through (already structured) | `The Simpsons (1989) {tvdb-71663}/Season 01/...` | Symlinked as-is, name preserved |
| 27 | Pass-through (scene naming) | `Wild.Kratts.Season.4/` | Cleaned to `Wild Kratts Season 4/` via clean_passthrough_name |
| 28 | Bare episode folder | `Planet.Earth.1080p.BluRay/pe.E01...` | Detected, folder symlinked as Season 01 under "Planet Earth" |
| 29 | Orphan (bare "Season N") | `Season 1/` | Via tv_orphans override to specified show name |
| 30 | Miniseries from /movies/ (SxxExx) | `The.Night.Of.2016.1080p/The.Night.Of.S01E01.1080p.mkv...` | Individual episode symlinks under `The Night Of/Season 01/` |
| 31 | Miniseries from /movies/ (NxNN) | `Some.Mini.2020.720p/Some.Mini.1x01.720p.mkv...` | Individual episode symlinks under `Some Mini/Season 01/` |
| 32 | Part.N folder in /movies/ | `Kill.Bill.2003.1080p.BluRay/Kill.Bill.Part.1.1080p.mkv...` | NOT detected as miniseries. Left for movies pipeline ambiguity prompt. |

### TMDB Confidence Results

| Parsed name | TMDB result | Accepted? | Why |
|------------|-------------|-----------|-----|
| "Fallout" | "Fallout" | Yes | 1 word, exact match, same length. |
| "The Office" | "The Office US" | Yes | 2 parsed words both present, TMDB is 3 words (2+1 allowed). |
| "Retro Show" | "Super Maximum Retro Show" | No | 2 parsed words present but TMDB has 4 words (> 2+1=3). Different show. |
| "Retro Show" | "Amazing World of Gumball" | No | 0 of 2 parsed words overlap. |
| "Some Weird Long Show" | "Some Weird Long Different Show" | Yes | 3 of 4 words overlap (75%, above 50% threshold). |
| "Fallout" | "Fallout New Vegas Adventures" | No | All 1 word present but TMDB has 4 words (> 1+1=2). |


## Planned Work

### High Priority

1. **Movie title overrides.** Config field `movie_title_overrides` is already loaded from `[overrides.movie_titles]` and stored on Config. Not yet consumed by movies.py. Wire it in the same way TV name overrides work.

2. **State tracking file.** JSON in output directories recording what has been linked, from where, and when. Enables: orphan detection (source files with no corresponding symlink), change reporting (what is new since last run), faster re-runs (skip unchanged content), `medialnk status` command.

3. **Orphan scan.** Walk source directories, check for corresponding symlinks in output. Report source files that nothing points to. Important for library maintenance over time.

### Medium Priority

4. **NofN bare file parsing.** Dedicated regex for "1of6" style patterns with explicit title boundary detection. Covers a real-world format that currently lands in unmatched.

5. **TMDB confidence improvements.** Levenshtein distance, word order weighting, optional user confirmation before accepting a TMDB result.

6. **Policy config section.** Move hardcoded behavioral decisions into config: `duplicate_season_policy = prompt|first|highest`, `multi_episode_naming = combined|separate`, `passthrough_name_cleanup = true|false`, `tmdb_confidence = strict|loose|off`, `conflict_resolution = prompt|accept|skip`. Code is already structured so these can be added without refactoring.

### Lower Priority

7. **Watch mode (inotify/watchdog).** `medialnk watch` to automatically process new files as they appear in source directories. Requires debouncing for incomplete downloads.

8. **Summary JSON per run.** Machine-readable sibling to the log file for integration with monitoring/dashboards.

9. **Web UI.** Lightweight interface for viewing library state, resolving conflicts, managing overrides without editing TOML.

10. **Hardlink mode.** For single-filesystem users who want hardlinks for arr compatibility. Separate mode since PathGuard and clean behavior differ.


## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Two-layer architecture (source + presentation) | Separates concerns completely. Source can be chaotic, presentation is clean. Either can change independently. Presentation is disposable. |
| Symlinks not hardlinks | MergerFS EXDEV. Symlinks follow the unified mount path regardless of physical drive layout. |
| Absolute container paths in symlinks | Relative symlinks break when Docker container working directory differs from host. Absolute container paths always resolve inside Jellyfin/Sonarr. |
| Movies pipeline runs before TV | TV's miniseries scanner reads /movies/. Consistent run order ensures consistent results. |
| Part.N excluded from miniseries detection | Could be multi-part film or miniseries. No heuristic is reliable. Human routing is the only correct answer. |
| episode_info has include_part parameter | Single function, two behaviors. Avoids duplicating the entire pattern chain in a separate function. |
| normalize_for_match() separate from _norm_key() | Pass 1 grouping needs light normalization (case, apostrophes). Cross-source matching needs aggressive normalization (articles, studio prefixes, years, all punctuation). Conflating them would misgroup Pass 1 folders. |
| TMDB word-overlap with length check for short names | Blind acceptance causes false matches ("Retro Show" -> "Super Maximum Retro Show"). Pure word overlap would accept it since both parsed words are present. Adding a length check catches cases where the TMDB result is clearly a different, longer-titled show. |
| Season symlink -> real dir for conflicts | Can't write individual episode symlinks into a folder symlink without writing into the source directory. A real directory in the presentation layer is the only structure that allows mixed content from different sources. |
| User prompt for duplicate season quality | Auto-merging both qualities into one dir would confuse Sonarr (two copies of every episode). The user knows which quality they want active. |
| Arr rename OFF during initial import | Arr rename renames entries in the presentation layer, not source. During bulk import this causes mismatched library state. Disable for import, re-enable for new downloads. |
| Safe pass-through cleanup only | Dots to spaces is non-destructive. Stripping years or metadata from pass-through names could break Jellyfin matching for folders that are already correctly structured. |
| PathGuard as runtime enforcement (not just convention) | A comment or documentation saying "don't write to source" does not prevent a bug from doing exactly that. The guard makes it a hard crash. |
| validate_output_dir on every startup | A sentinel file in the output dir would miss the case where config changes between runs. Scanning actual content checks what's really there. |
| Multi-episode combined naming (S01E05-E06) | Both Jellyfin and Sonarr recognize this format. Two separate symlinks for one file would appear as duplicate content. |
| TOML config (stdlib tomllib) | Target audience (selfhosted users) knows YAML from Docker compose, but TOML avoids the pyyaml dependency. tomllib is stdlib since Python 3.11. Zero external dependencies matters for a tool people install on servers. |
| Subcommands over flat flags | `sync`, `clean`, `validate`, `test-library` are distinct operations with different meanings. Subcommands make the CLI self-documenting and prevent invalid flag combinations. |
| Config for installation behavior, CLI for run behavior | Paths and API keys don't change between runs. `--dry-run` and `--yes` change by the moment. Separating these prevents config file edits for one-off operations. |
| Per-run log files at verbose level | Console can be quiet for cron. The log always has the detail for debugging after the fact. Logs become critical for trust and recoverability when the tool runs unattended. |
| movie_title_overrides loaded but not yet applied | Config infrastructure is in place matching the TV override pattern. Wiring it into movies.py is straightforward when needed. Not wiring it prematurely avoids dead code in the hot path. |