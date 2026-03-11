# medialinker

Two Python scripts that build a clean, Jellyfin/arr stack-compatible symlink
tree from a disorganized media library. This will not touch/move the existing files. Doesnt break seeding, doesn't write to existing files, works automatically. 

---

## **What this does**

- Scans a messy, unstructured movies or TV folder and builds a clean Jellyfin/Radarr-ready library

- Handles any mix of file formats, naming conventions, folder structures 

    - Well, it handles most of them used by files people add to arr stack. 

- Detects and separates TV/miniseries/movie content automatically — nothing manual
    
    - This was my main issue. I had miniseries/tv mixed with the Movies folder. This will seperate them automatically. 

- Automatic TMDB lookup matching, symlinking, and restructuring. 
    
    - Manual TMDB lookup for anything it can't match on its own

- Multiple versions of the same movie handled automatically with quality tagging.   
    
    - Works with different (1080p vs. 4k) and same (2 1080p files) resolutions. 

- Groups bare season folders by show automatically

- Warns you about anything ambiguous at the end

    - Allows for manual matching to TMDB if, for example, a movie doesn't have a year in title. 

    - Also will let you choose whether to let it *try* to auto match it without year. 

- Only uses absolute symlinks. 

    - Hardlinks can cause issues with MergerFS pools. Absolute symlinks work on everything always.

    - Absolute over relative symlinks also fix any pathing issue with Docker containers. 

- Can test as a dry run before commiting to anything 

- Works fast. 

    - Scanned, matched, symlinked, and created structure for 1000 movie folders in about 3 seconds. 

---

## The problem this solves

For years I've tried to easily and cleanly setup radarr and the rest of the arr stack with my existing, old, disorganized media library. Well, turns out, Jellyfin and the arr stack want a certain structure to read the files easily. 

  - It also turns out, I'm pretty far off from that structure. 

Ive tried a few solutions in the past, but they all had some problem. 

- You can rename it with the Arr stack itself, but you need to already have a clean library to get it running. Chicken and egg. Makes me sad. 

- Renamers like FileBot, or TinyMediaManager, etc will rename the files fine. But they rename the actual files. That breaks seeding and just makes everyone sad. 

  - Filebot **can** do symlinks via `--action symlink`, and it has TMDB lookup. But youd still have to manually sort movies vs tv across sometimes thousands of files, then sort them into folders, THEN rename them. People are sad again. 

- rclone union or a sonarr extension will just merge paths. They can't rename/categorize content. Too bad. So sad. 

All hit at least one of these walls for me with a messy existing library:

- They rename or move the actual files, breaking torrent seeding. - oof.  
- They have no logic for detecting movies vs. TV in a mixed folder. - Making me do it manually, no thanks.
- They require the library to already be reasonably structured before they can match anything.  - Well, guess what? It's not. 
- They use hardlinks, which fail on mergerfs pools with `EXDEV` (cross-device link error). - ("JuSt uSe ZfS" - whatever. not the point)

---

## How it works

```
/movies/                         /tv/
  Some.Movie.2020.mkv              Show.Name.S01.720p.../
  Show.Name.S01.720p.../           Show.Name.S02.1080p.../
  ...                              ...

         ↓ make_movies_links.py        ↓ make_tv_links.py

/movies-linked/                  /tv-linked/
  Some Movie (2020)/               Show Name/
    Some Movie (2020).mkv  ──→       Season 01/  ──→ /tv/Show.Name.S01.../
                                     Season 02/  ──→ /tv/Show.Name.S02.../
```

All symlink targets are **absolute container-side paths** (e.g. `/data/media/movies/...`),
so they resolve correctly inside Docker regardless of working directory or mount structure.

Hardlinks are intentionally not used. On mergerfs pools, `movies/` and `movies-linked/`
can land on different underlying branches, causing `os.link()` to fail with `EXDEV`.
Absolute symlinks work on all filesystems, always.

I split this across 2 diffrent scripts, one for movies, and one for TV. The movie script is rather straightforward. The TV script can scan for TV shows as well as miniseries seperately, pull them from a different folder (like your /movies/ folder) and group them together so it can be picked up correctly in Jellyfin. 

> Reference: [TRaSH Guides — Hardlinks and Instant Moves](https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/)

---

## Scripts

### `make_movies_links.py`

Reads from `/movies/`, writes to `/movies-linked/`.

**Output structure:**
```
movies-linked/
  Movie Name (Year)/
    Movie Name (Year).mkv              ← single version
    Movie Name (Year) - 1080P.mkv      ← multiple versions of same title+year
    Movie Name (Year) - 2160P.mkv
    Movie Name (Year) - 1080P.2.mkv    ← two copies at same resolution
```

**Features:**

  - **Multi-version grouping**: two or more copies of the same title+year each get
    a quality suffix (1080P, 2160P, REMUX, etc.) instead of being discarded.
    Same-resolution duplicates get .2, .3, ... appended to disambiguate.

  - **Year/title protection**: year must be preceded by a separator character so
    numeric titles like "1917" or "2001" are not misread as release years.

  - **Episode detection**: folders containing 2+ episode files (S01E01, 1x01,
    Episode.N, NofN) are skipped — make_tv_links.py symlinks those instead.

  - **TMDB auto-lookup**: entries with no parseable year are flagged. At the end
    of a real run you can choose to resolve them via concurrent TMDB search
    (ThreadPoolExecutor, 8 workers) or leave them for manual matching.

  - **Filename sanitization**: characters illegal on Windows/network mounts
    (/ : \\ ? * " < > |) are replaced with - in all generated names.

  - **Sample exclusion**: word-boundary match (\bsample\b) avoids false positives
    like example.mkv.

  - **Performance**: os.scandir() used throughout to avoid redundant stat() calls.
 
**Original files are NEVER modified. Torrent clients keeps seeding from original paths.**

**Usage:**
```bash
python3 make_movies_links.py --dry-run   # preview without creating anything
python3 make_movies_links.py             # create symlinks
python3 make_movies_links.py --clean     # remove broken links then rebuild
```

---

### `make_tv_links.py`

Reads from `/tv/` and `/movies/` (for miniseries), writes to `/tv-linked/`.

**Output structure:**
```
tv-linked/
  Show Name/
    Season 01/  ──→ /tv/Show.Name.S01.720p.../
    Season 02/  ──→ /tv/Show.Name.S02.1080p.../
  Already Structured Show (Year) {tvdb-xxxxx}/  ──→ symlinked as-is
  Miniseries Title/
    Season 01/
      Miniseries.S01E01.mkv  ──→ /movies/Miniseries.S01.../episode.mkv
```

**Features:**
  - **Show grouping**: bare season folders (Show.S01.quality...) are parsed,
    grouped by show name, and placed under Show Name/Season XX/.

  - **Pass-through**: folders already in correct Jellyfin structure are symlinked
    as-is without renaming.
    
  - **Miniseries detection**: folders in /movies/ containing 2+ episode files
    (S01E01, 1x01, Episode.N, NofN) are routed here instead of movies-linked/.

  - **Name overrides**: hardcoded corrections for known naming inconsistencies
    (e.g. "The Office US" -> "The Office (US)").

  - **Orphan overrides**: bare "Season N" folders with no show context are mapped
    to their correct show via ORPHAN_OVERRIDES.

  - **Episode format detection covers**: S01E01, 1x01, Episode.N, NofN.
  - 
  - **Filename sanitization**: characters illegal on Windows/network mounts
    (/ : \\ ? * " < > |) are replaced with - in all generated names.

  - **Sample exclusion**: word-boundary match (\\bsample\\b) avoids false positives
    like example.mkv.

  - **Performance**: os.scandir() used throughout to avoid redundant stat() calls.
  
**Original files are NEVER modified. Torrent clients keeps seeding from original paths.**

**Usage:**
```bash
python3 make_tv_links.py --dry-run   # preview without creating anything
python3 make_tv_links.py             # create symlinks
python3 make_tv_links.py --clean     # remove broken links then rebuild
```

---

## Setup

### 1. Configure mount paths

At the top of each script, set the two path constants to match your setup:

```python
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path as seen inside Docker
```

`MEDIA_ROOT_HOST` is used to find files on disk. `MEDIA_ROOT_CONTAINER` is written
into the symlink targets so they resolve correctly inside Jellyfin/Radarr/Sonarr.

If you're not running inside Docker, set both to the same value.

### 2. TMDB API key (movies script only)

Required only if you want auto-lookup for entries with no parseable year.
Get a free key at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

```bash
export TMDB_API_KEY="your_key_here"
python3 make_movies_links.py
```

Or set it directly in the script:
```python
TMDB_API_KEY = "your_key_here"
```

> API reference: [TMDB Search Movies](https://developer.themoviedb.org/reference/search-movie)

### 3. Customise overrides (TV script only)

Open `make_tv_links.py` and edit the two dicts near the top:

**`NAME_OVERRIDES`** — use when the same show's season folders parse to different names,
or when the parsed name doesn't match TVDB. Run `--dry-run` first and check the
`[TV SOURCE]` section to see what names are being parsed.

```python
NAME_OVERRIDES = {
    "The Office US": "The Office (US)",
    "Scooby-Doo Where Are You": "Scooby Doo Where Are You",
}
```

**`ORPHAN_OVERRIDES`** — use when a folder is literally named `Season 1/` with no show
name. These show up in `[TV PASS-THROUGH]` during a dry run.

```python
ORPHAN_OVERRIDES = {
    "Season 1": ("Some Show", 1),
    "Season 2": ("Some Show", 2),
}
```

---

## Recommended workflow

```bash
# 1. Dry run both scripts and review output
python3 make_movies_links.py --dry-run >> outputfilemovies.txt
python3 make_tv_links.py --dry-run >> outputfiletv.txt

# 2. Add any NAME_OVERRIDES / ORPHAN_OVERRIDES needed, repeat dry run until clean

# 3. Run for real
python3 make_movies_links.py
python3 make_tv_links.py

# 4. Point Jellyfin libraries at movies-linked/ and tv-linked/
# 5. Use Radarr/Sonarr manual import against the linked directories
```

---

## Requirements

Python 3.6+ — stdlib only, no dependencies.
