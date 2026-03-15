# medialn

Two Python scripts that build a clean, Jellyfin/arr-compatible symlink library from a messy, unstructured media folder. Automatically separates movies, TV shows, and miniseries. No files are moved or renamed. Seeding keeps working.

---

## What this does

Automatically organize a messy media library to set up Jellyfin, Radarr, or Sonarr without manually sorting thousands of files.

They scan your existing `/movies/` and `/tv/` folders, figure out what's what, seperate the files automatically, and build a clean symlink tree that Jellyfin and the arr stack can actually read. The original files stay exactly where they are. Your torrent client keeps seeding from the same paths. Nothing breaks.

```
/movies/                              /tv/
  Some.Movie.2020.mkv                   Show.Name.S01.720p.../
  Show.Name.S01.720p.../                Show.Name.S02.1080p.../
  Mini.Series.1080p.../
    ...                                 ...

         | make_movies_links.py              | make_tv_links.py

/movies-linked/                       /tv-linked/
  Some Movie (2020)/                    Show Name/
    Some Movie (2020).mkv                 Season 01/ -> /tv/Show.Name.S01.../
                                          Season 02/ -> /tv/Show.Name.S02.../
                                        Mini Series/
                                          Season 01/ -> /movies/Mini.Series.../
```

Run the two scripts, point Jellyfin at the `-linked/` directories, and you're good to go.

---

## Why this exists

Jellyfin and the arr stack want a specific folder structure. If your library doesn't already match that, you're stuck. I've tried the usual approaches and they all hit at least one wall:

- **Arr stack** can rename files, but it needs a clean library to get started. Chicken and egg.
- **Renamers** like FileBot or TinyMediaManager rename the actual files. That breaks torrent seeding. FileBot can do symlinks via `--action symlink`, but you'd still need to manually sort movies vs TV across thousands of files first. Gross. 
- **rclone union** and similar tools merge paths but can't rename or categorize anything. 

Common problems across all of these:

- They rename or move files, which breaks seeding. Not great. 
- They have no logic for detecting movies vs TV in a mixed folder. 
- They need the library to already be reasonably structured before they can match anything. This is a bad time, trust me. 
- They use hardlinks, which fail on mergerfs pools with `EXDEV` (cross-device link error). I do plan on making a hardlink version of this for use on single filesystems. 

These scripts don't have those problems.

---

## Key features

### Automatic separation

The scripts automatically detect and separate movies, TV shows, and miniseries. If you have a miniseries sitting in your `/movies/` folder (downloaded from a movie tracker, for example), the TV script will find it and route it to `tv-linked/`. The movies script knows to skip it. No manual sorting needed.

Folders with ambiguous content (like Part.N files that could be a multi-part movie or a miniseries) get flagged for you to confirm, rather than being silently misrouted.

### Seeding stays intact

Everything is done with symlinks. The original files are never moved, renamed, or modified in any way. Your torrent client keeps seeding from the original paths as if nothing happened.

All symlinks are absolute, using container-side paths so they resolve correctly inside Docker. Hardlinks are intentionally not used since on mergerfs pools, source and destination can land on different underlying branches, which causes `os.link()` to fail with `EXDEV`.

> Reference: [TRaSH Guides - Hardlinks and Instant Moves](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/)

### Accurate matching

The title and year parsing is built to handle the kind of filenames you actually see with most files. It handles dot-separated names, bracket tags, quality strings, codec info, and release group tags. Numeric titles like `1917` or `2001` won't get misread as release years (the year must be preceded by a separator character). 

Episode detection covers `S01E01`, `1x01`, `Episode.N`, `NofN`, bare `E01`, and `Part.N` formats.

TMDB auto-lookup is available for entries where a year can't be extracted from the filename. You get prompted at the end of a run to either auto-resolve these via TMDB or leave them for manual matching.

You will also get a prompt for other, ambiguous, potentially wrong matches to make sure it goes to correct place. 

### Nothing is ever changed without you confirming it

Any situation where existing structure would be modified requires you to type a confirmation first. The script will never silently overwrite, replace, or restructure anything that already exists in your linked library. If you skip a prompt, the file is left alone and noted in the output.


### Other features

- **Multi-version grouping** - multiple copies of the same movie (1080p, 4K, Remux) coexist in one folder with quality suffixes. Same-resolution duplicates get `.2`, `.3` to avoid collisions.
- **Show name grouping** - bare season folders like `Show.Name.S01.720p...` are grouped by show name under `Show Name/Season 01/`.
- **Pass-through** - folders already in correct Jellyfin structure are symlinked as-is.
- **Case/apostrophe-insensitive grouping** - `Blue's Clues`, `Blues Clues`, and `blues clues` all resolve to the same show.
- **Name and orphan overrides** - allow for manual correction show names that parse wrong or map bare `Season N` folders to the right show.
- **Duplicate and overlap warnings** - flags when two source folders map to the same show+season, or when a grouped show and a pass-through folder overlap.
- **Dry run mode** - preview everything before committing.
- **Clean mode** - remove broken symlinks and empty directories, then rebuild.
- **Fast** - scanned, matched, and symlinked 1000 movie folders in a few seconds.

---

## Scripts

### `make_movies_links.py`

Reads from `/movies/`, writes to `/movies-linked/`.

```
movies-linked/
  Movie Name (Year)/
    Movie Name (Year).mkv              <- single version
    Movie Name (Year) - 1080P.mkv      <- multiple versions, quality-tagged
    Movie Name (Year) - 2160P.mkv
    Movie Name (Year) - 1080P.2.mkv    <- same-resolution duplicates
```

### `make_tv_links.py`

Reads from `/tv/` and `/movies/` (for miniseries), writes to `/tv-linked/`.

```
tv-linked/
  Show Name/
    Season 01/  -> /tv/Show.Name.S01.720p.../
    Season 02/  -> /tv/Show.Name.S02.1080p.../
  Miniseries Title/
    Season 01/
      Miniseries.S01E01.mkv  -> /movies/Miniseries.S01.../episode.mkv
```

### `common.py`

**Bare (non nested) episode handling**

Shared utilities used by both scripts. Contains regex patterns, filesystem helpers, and symlink logic. Keeps everything in one place so the two scripts stay in sync.
**Brand new show, no existing structure** File arrives for a show that doesn't exist in your linked library at all. The script creates the show folder and season folder, links the episode in, no questions asked. If you run it again later, it recognises the episode is already linked and skips it silently.

**Episode already exists in the season folder** You have a complete season folder symlinked for a show, and a bare file arrives for an episode that's already in that folder at the same quality. The script sees it's already covered and skips it silently. Nothing to do.

**Same episode, different quality** You have a 1080p season folder linked, and a 720p version of one episode lands as a bare file. The script flags this and asks you what to do. If you confirm, it converts the season folder symlink into a real directory, re-links all the existing episodes individually inside it, then adds the new quality version alongside them. Both versions end up in the same Season folder and Jellyfin will show both.

**Episode missing from the season folder entirely** The season folder is linked but this particular episode isn't in it — maybe it was a gap in a release, or you grabbed it from somewhere else. Same prompt as above. You confirm, the season gets converted to a real directory, and the missing episode gets added.

* * *


---

## Usage

```bash
# Preview without creating anything
python3 make_movies_links.py --dry-run
python3 make_tv_links.py --dry-run

# Run for real
python3 make_movies_links.py
python3 make_tv_links.py

# Remove broken symlinks and rebuild
python3 make_movies_links.py --clean
python3 make_tv_links.py --clean
```

Pipe `--dry-run` to a file to review large libraries:
```bash
python3 make_movies_links.py --dry-run >> movies-dry-run.txt
python3 make_tv_links.py --dry-run >> tv-dry-run.txt
```

### Recommended workflow

```bash
# 1. Dry run and review output
python3 make_movies_links.py --dry-run >> movies-dry-run.txt
python3 make_tv_links.py --dry-run >> tv-dry-run.txt

# 2. Add NAME_OVERRIDES / ORPHAN_OVERRIDES as needed, repeat until clean

# 3. Run for real
python3 make_movies_links.py
python3 make_tv_links.py

# 4. Point Jellyfin at movies-linked/ and tv-linked/
# 5. Radarr/Sonarr manual import against the linked directories
```

### **ENSURE RENAMING IS SET TO OFF IN RADARR/SONARR WHEN IMPORTING CLEANED LIBRARY. YOU CAN RE-ENABLE AFTER TO CORRECT NEW DOWNLOADS.** 

---

## Setup

### 1. Configure mount paths

Set these at the top of each script (and in `common.py`):

```python
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path inside Docker
```

`MEDIA_ROOT_HOST` is where the scripts find files on disk. `MEDIA_ROOT_CONTAINER` is written into symlink targets so they resolve correctly inside Jellyfin/Radarr/Sonarr.

If you're not running inside Docker, set both to the same value.

### 2. TMDB API key (movies script only)

Only needed if you want auto-lookup for entries with no detectable year. Get a free key at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

```bash
export TMDB_API_KEY="your_key_here"
```

Or set it directly in `make_movies_links.py`:
```python
TMDB_API_KEY = "your_key_here"
```

> API reference: [TMDB Search Movies](https://developer.themoviedb.org/reference/search-movie)

### 3. Overrides (TV script only)

**`NAME_OVERRIDES`** - fix show names that parse differently across seasons, or that don't match TVDB. Run `--dry-run` first and check `[TV SOURCE]` to see what names are being parsed.

```python
NAME_OVERRIDES = {
    "The Office US": "The Office (US)",
    "Scooby-Doo Where Are You": "Scooby Doo Where Are You",
}
```

**`ORPHAN_OVERRIDES`** - for folders literally named `Season 1/` with no show name. These show up in `[TV PASS-THROUGH]` during a dry run.

```python
ORPHAN_OVERRIDES = {
    "Season 1": ("Little Bear", 1),
    "Season 2": ("Little Bear", 2),
}
```

### **ENSURE RENAMING IS SET TO OFF IN RADARR/SONARR WHEN IMPORTING CLEANED LIBRARY. YOU CAN RE-ENABLE AFTER TO CORRECT NEW DOWNLOADS.** 

---

## Requirements

Python 3.6+ - stdlib only, no external dependencies.

## Changelog
---

## v0.23 Changelog

### `common.py`

**`clean_broken_symlinks()` incorrectly treating all medialn symlinks as broken**
- `os.path.exists()` follows symlinks and checks if the target exists at the literal path
- medialn writes symlinks using container-side paths (`/data/media/...`) — those paths don't exist on the host (`/mnt/storage/data/media/...`), so every symlink was being treated as broken
- `--clean` was removing all episode symlinks inside converted real directories, causing empty dirs to be pruned and Pass 1 to recreate them as folder symlinks on rebuild — this is what caused the quality conflict prompt to reappear on re-run
- New `_symlink_target_exists()` helper added — reads symlink target, translates container path to host path if roots are provided, then checks existence against the translated path
- `clean_broken_symlinks()` now accepts `host_root` and `container_root` optional parameters and uses `_symlink_target_exists()` for all existence checks

---

### `make_tv_links.py`

**Quality conflict resolution re-prompted on subsequent runs and crashed on confirmation**
- After a `quality_variant` conflict was resolved (season symlink converted to real dir, variant linked), re-running showed the same prompt again with incorrect message `"Season Season 01 is currently a folder symlink"`
- Root cause: `scan_tv_bare_files()` detected a quality variant by checking the source folder contents — which always shows different qualities — without checking the actual state of the season directory in `tv-linked/`
- On confirmation at the re-prompt, `convert_season_symlink_to_real_dir()` called `os.readlink()` against the now-real directory, which is an invalid operation on Linux — `[ERROR] Could not read symlink: Invalid argument`
- Fix: in the `quality_variant` branch, check whether `season_path` is already a real directory before classifying the conflict
    - If real dir exists and the variant symlink is already inside it — skip silently
    - If real dir exists but variant not yet inside it — reroute to `bare_dir_episode` conflict type, which adds the symlink directly without attempting conversion
- Same fix applied to `missing_episode` branch — identical root cause, same incorrect behavior would occur on re-run after conversion

**`clean_broken_symlinks()` call updated**
- `clean_broken_symlinks(TV_LINKED)` → `clean_broken_symlinks(TV_LINKED, MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER)`

---

### `make_movies_links.py`

**`clean_broken_symlinks()` call updated**
- `clean_broken_symlinks(MOVIES_LINKED)` → `clean_broken_symlinks(MOVIES_LINKED, MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER)`

**Local `RE_QUALITY` and `extract_quality()` removed**
- Both were defined locally as duplicates of the versions now in `common.py`
- `RE_QUALITY` local definition removed
- `extract_quality()` local definition removed
- `extract_quality` added to the `from common import (...)` block

**Hardcoded TMDB API key removed**
- Private fork API key was present in the uploaded file — reverted to `os.environ.get("TMDB_API_KEY", "")` for the public version

**Dead variable removed**
- `raw_entries = []` in `scan_movies()` was assigned but never used — removed

###  TV Script testing

-   **Pass 1 folder grouping** - season folders grouped, named, symlinked correctly
-   **Pass-through** - already-structured folders symlinked as-is
-   **Miniseries from /movies/** - correctly detected and routed to tv-linked
-   **Bare file new show/season** - real dir created, episodes linked, idempotent
-   **Bare file quality variant** - conflict detected, prompt works, season converted, both qualities coexist, no re-prompt
-   **Bare file missing episode** - conflict detected, prompt works, season converted, episode added, no re-prompt
-   **Sonarr hardlink collision** - script skips existing hardlinks, link count intact
-   **`--clean`** - only removes genuinely broken symlinks, container path translation working correctly, real dirs and their contents preserved
-   **Idempotency** - re-runs fully silent on already-linked content
-   **Name resolution** - fuzzy matching working across TMDB/folder parse differences, trailing year stripping working, apostrophe normalization correct




## v0.22 

### Bug fix - duplicate show folders from name mismatch between Pass 1 and Pass 2

**The problem:** When a bare episode file arrives for a show that already has season folders linked, the two passes could produce different canonical names for the same show. Pass 1 parses folder names directly. Pass 2 goes through TMDB. TMDB doesn't always return the same string that folder parsing produces. "Marvel's Spidey and His Amazing Friends" from a folder became "Spidey and His Amazing Friends" from TMDB, and the normalized key comparison failed to match them, creating a second show folder on disk.

**The fix:** New `normalize_for_match()` function used exclusively for cross-source comparison. It strips leading articles (the, a, an), studio prefixes (Marvel's, DC's, Disney's, BBC, NBC), possessives, trailing years, and all remaining punctuation before comparing. This is intentionally more aggressive than `normalize_show_key()`, which is still used for grouping Pass 1 folders where light normalization is correct.

New `find_matching_show()` function replaces the inline key lookup in `scan_tv_bare_files()`. It runs two stages: first checks the Pass 1 grouped dict using fuzzy matching, then falls back to scanning existing folders in `tv-linked/` on disk. The disk fallback catches cases where a show was created in a previous run and isn't in the current run's grouped dict.

When a fuzzy match succeeds, the bare file adopts the already-established name rather than creating a new one.

---

### Bug fix - trailing year in filename producing split show folders

**The problem:** Files named `Fallout.2024.S01E01.mkv` produced raw show name `"Fallout 2024"` from `parse_bare_episode()`. Pass 1 already stripped trailing years via `extract_show_and_season()`. Pass 2 did not, so the normalized keys never matched and a second `Fallout 2024` folder appeared alongside the existing `Fallout` folder.

**The fix:** `parse_bare_episode()` now strips trailing four-digit years from the parsed show name before returning, matching the same logic Pass 1 already applies to folder names.

---

### New function: `normalize_for_match()`

Separate normalization path for cross-source name comparison only. Never used for display names or folder creation. The distinction between this and `normalize_show_key()` is intentional and documented in both functions.

### New function: `find_matching_show()`

Two-stage show name lookup: grouped dict first, disk scan second. Centralizes the matching logic that was previously inline in `scan_tv_bare_files()` and makes it testable in isolation.


**v.21**

### New — bare episode file handling (TV script)

The TV script now handles video files sitting directly in `/tv/` with no parent folder. Previously these were silently ignored.

**Name resolution**

-   Show names are resolved automatically via TMDB TV search before falling back to the parsed filename title
-   `NAME_OVERRIDES` take priority over TMDB — existing overrides are unaffected
-   TMDB results are cached per run so each unique show name only hits the API once

**New output section: `[BARE FILES - NEW]`** Episodes with no existing season structure are linked automatically with no prompts. Show and season directories are created as real directories (not symlinks) with individual episode symlinks inside.

**New output section: `[BARE FILES - CONFLICTS]`** Episodes that collide with existing season structure are flagged interactively:

-   Episode already covered at same quality: silently skipped
-   Episode exists in folder at different quality: prompts to convert season symlink to real dir and add quality variant
-   Episode missing from existing season folder: prompts to convert and add
-   Season real dir exists from previous run, episode not yet linked: prompts to add

**New output section: `[BARE FILES - UNMATCHED]`** Files that couldn't be parsed into show/season/episode are listed at the end for manual handling.

**Idempotent** Re-running after bare files have been linked skips already-linked episodes silently. Safe to run repeatedly.

* * *

### Bug fix — duplicate regex definitions (TV script)

`RE_XNOTATION`, `RE_EPISODE`, and `RE_NOF` were defined locally in the TV script despite already existing in `common.py`. The local definitions are removed and the TV script now imports them from common along with the rest of the shared patterns. Behavior is identical but the two scripts are now properly in sync — a pattern change in `common.py` will apply to both scripts.

* * *

### common.py — extract\_quality() added

`extract_quality()` and its `RE_QUALITY` pattern moved into `common.py` and are now shared across both scripts. The TV script imports it from common. The movies script still has a local copy pending cleanup.

* * *

### New imports (TV script)

`json`, `urllib.request`, `urllib.parse` added to support TMDB TV lookup.

* * *

### Known pending

-   `make_movies_links.py` local `extract_quality()` not yet removed — carry forward to next update



**v.20:**
- Extracted shared code into `common.py` - both scripts now import from the same place instead of duplicating logic
- Removed dead code (`is_episode()` was defined but unused in the TV script)
- Extracted interactive Part.N routing out of `main()` into its own function
- Cleaned up multi-version resolution logic (was re-parsing a string it had just built)
- `clean_broken_symlinks` now checks both file and directory symlinks everywhere (the movies version was only checking files)
- Video-finding logic consolidated into shared helpers instead of being repeated in multiple places
- General cleanup: comments, docstrings, formatting

**v.12:**
- Added Part.N detection and ambiguous folder handling to movies script
- Added Part.N as last-resort episode pattern in TV script
- Interactive prompt for routing ambiguous Part.N folders as movie or TV
