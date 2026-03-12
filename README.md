# medialn

Two Python scripts that build a clean, Jellyfin/arr stack-compatible symlink tree from a disorganized media library. Automatically matches and seperates files. No files are touched or moved. Seeding keeps working. 

---

## What this does

**Automatic Restructure** - Takes a messy media folder and builds a clean, correctly structured library that Jellyfin, Radarr, and Sonarr can read — using symlinks only. 

**Automatic Media Seperation** - Automatically, accurately seperating TV shows, miniseries, and Movies to correct folders for importing. 
  - Automatic media type matching with TMDB lookup matching, symlinking, and restructuring.
  
    -Manual TMDB lookup for anything it can't match on its own

**Handles mixed file formats**, naming conventions, folder structures
  - Well, it handles most of them used by files people add to arr stack.

**Multiple version grouping** - The same movie handled automatically with quality tagging.
  - Works with different (1080p vs. 4k) and same (2 1080p files) resolutions.

**Only uses absolute symlinks.**
  - Hardlinks can cause issues with MergerFS pools. 
  - Absolute symlinks work on everything always.

**Works fast.**
  - Scanned, matched, symlinked, and created structure for 1000 movie folders in about 1 second.
  - Absolute over relative symlinks also fix any pathing issue with Docker containers.
  - 

Basically, if you're library is a mess, run these 2 scripts and you're good to go. It should match everything, but it will warn you if it can't or you can set manual overrides inside the script itself. 

This will sort your movies folder as well as TV. It will auto scan and detect TV shows/miniseries regardless of the files mixed in same dir. Movies/TV in same folder will be seperated automatically.

The matching works pretty well regardless of the formatting of the existing files. 

More info on the functions are under the sections below. 

```
/movies/                              /tv/
  Some.Movie.2020.mkv                   Show.Name.S01.720p.../
  Show.Name.S01.720p.../                Show.Name.S02.1080p.../
    ...                                 ...
         ↓ make_movies_links.py              ↓ make_tv_links.py

/movies-linked/                       /tv-linked/
  Some Movie (2020)/                    Show Name/
    Some Movie (2020).mkv                 Season 01/ --> /tv/Show.Name.S01.../
                                          Season 02/  --> /tv/Show.Name.S02.../
```

Symlink targets are always **absolute container-side paths** so they resolve correctly inside Docker regardless of working directory or mount structure.

Hardlinks are intentionally not used — on mergerfs pools, `movies/` and `movies-linked/` can land on different underlying branches, causing `os.link()` to fail with `EXDEV`. Absolute symlinks work on all filesystems, always.

> Reference: [TRaSH Guides - Hardlinks and Instant Moves](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/)

---

## The problem this solves

For years I've tried to easily and cleanly setup radarr and the rest of the arr stack with my existing, old, disorganized media library. Well, turns out, Jellyfin and the arr stack want a certain structure to read the files easily. 

  - It also turns out, I'm pretty far off from that structure. 

Ive tried a few solutions in the past, but they all had some problem: 

- **Arr stack**: You can rename it with the Arr stack itself, but you need to already have a clean library to get it running. Chicken and egg. 

- **Renamers**:  like FileBot, or TinyMediaManager, etc will rename the files fine. But they rename the actual files. Breaks seeding. Depressing. 
  - Filebot **can** do symlinks via `--action symlink`, and it has TMDB lookup. But youd still have to manually sort movies vs tv across sometimes thousands of files, then sort them into folders, THEN rename them. 

- **rclone union** or a sonarr extension will just merge paths. They can't rename/categorize content. 

All hit at least one of these walls for me with a messy existing library:

- They rename or move the actual files, breaking torrent seeding. - oof.  
- They have no logic for detecting movies vs. TV in a mixed folder. - Making me do it manually, no thanks.
- They require the library to already be reasonably structured before they can match anything.  - Well, guess what? It's not. 
- They use hardlinks, which fail on mergerfs pools with `EXDEV` (cross-device link error). - ("JuSt uSe ZfS" - whatever. not the point)

These scripts don't have those problems.

---

## Scripts

### `make_movies_links.py`

Reads from `/movies/`, writes to `/movies-linked/`.

```
movies-linked/
  Movie Name (Year)/
    Movie Name (Year).mkv              ← single version
    Movie Name (Year) - 1080P.mkv      ← multiple versions, quality-tagged
    Movie Name (Year) - 2160P.mkv
    Movie Name (Year) - 1080P.2.mkv    ← two copies at same resolution
```

- Detects and skips multi-episode folders — those go to `tv-linked/` via `make_tv_links.py`
- Multiple versions of the same title+year get quality suffixes instead of being discarded
- Year must be preceded by a separator so numeric titles like `1917` or `2001` aren't misread as release years
- Entries with no detectable year are flagged — at end of run you can auto-resolve via TMDB or leave for manual matching
- Illegal filename characters (`/ : \ ? * " < > |`) replaced with `-`

### `make_tv_links.py`

Reads from `/tv/` and `/movies/` (for miniseries), writes to `/tv-linked/`.

```
tv-linked/
  Show Name/
    Season 01/  ──→ /tv/Show.Name.S01.720p.../
    Season 02/  ──→ /tv/Show.Name.S02.1080p.../
  Miniseries Title/
    Season 01/
      Miniseries.S01E01.mkv  ──→ /movies/Miniseries.S01.../episode.mkv
```

- Bare season folders (`Show.S01.720p...`) are grouped by show name automatically
- Miniseries sitting in `/movies/` (2+ episode files) are detected and routed to `tv-linked/` instead
- Show name matching is case-insensitive and apostrophe-agnostic — `Blue's Clues`, `Blues Clues`, and `blues clues` all resolve to the same group
- Trailing years are stripped from show names (`Bluey 2018` → `Bluey`)
- Episode formats detected: `S01E01`, `1x01`, `Episode.N`, `NofN`, bare `E01`
- Bare `E01`-format releases (no `S01` in folder name) are auto-detected as Season 1 via file/subdir scanning
- Already-structured folders are symlinked as-is (pass-through)
- Configurable name and orphan overrides for manual cases (see Setup)

---

## Usage

```bash
# Preview without creating anything
python3 make_movies_links.py --dry-run
python3 make_tv_links.py --dry-run

# Apply
python3 make_movies_links.py
python3 make_tv_links.py

# Remove broken symlinks and rebuild (useful after moving/deleting source files)
python3 make_movies_links.py --clean
python3 make_tv_links.py --clean
```

Pipe `--dry-run` to a file to review large libraries:
```bash
python3 make_tv_links.py --dry-run >> tv-dry-run.txt
```

---

## Recommended workflow

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

---

## Setup

### 1. Configure mount paths

At the top of each script:

```python
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path inside Docker
```

`MEDIA_ROOT_HOST` is used to find files on disk. `MEDIA_ROOT_CONTAINER` is written into symlink targets so they resolve correctly inside Jellyfin/Radarr/Sonarr.

If you're not running inside Docker, set both to the same value.

### 2. TMDB API key (movies script only)

Only needed for auto-lookup of entries with no detectable year.
Get a free key at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

```bash
export TMDB_API_KEY="your_key_here"
```

Or set it directly in the script:
```python
TMDB_API_KEY = "your_key_here"
```

### 3. Overrides (TV script only)

**`NAME_OVERRIDES`** — fix show names that parse inconsistently across seasons, or that don't match TVDB. Run `--dry-run` first and check `[TV SOURCE]` to see what names are being parsed.

```python
NAME_OVERRIDES = {
    "The Office US": "The Office (US)",
}
```

**`ORPHAN_OVERRIDES`** — for folders literally named `Season 1/` with no show name in the folder itself. These show up in `[TV PASS-THROUGH]` during a dry run.

```python
ORPHAN_OVERRIDES = {
    "Season 1": ("Little Bear", 1),
    "Season 2": ("Little Bear", 2),
}
```

---

## Requirements

Python 3.6+ — stdlib only, no dependencies.