**make_tv_links.py**

**Regexes**
- `SEASON_RE` — matches folder names with `S\d{2}` pattern, extracts show name prefix and season number
- `RE_SXXEXX` — standard `S01E01` format
- `RE_XNOTATION` — `1x01` format
- `RE_EPISODE` — `Episode.N` format
- `RE_NOF` — `NofN` format
- `RE_BARE_EPISODE` — bare `E\d+` with negative lookbehind to avoid matching `S01E01`
- `RE_STRIP` — strips quality/codec/release tokens from folder names for title cleaning
- `RE_SAMPLE` — word-boundary match to avoid false positives like `example.mkv`
- `RE_ILLEGAL_CHARS` — characters illegal on Windows/network mounts

**Config**
- `MEDIA_ROOT_HOST` / `MEDIA_ROOT_CONTAINER` — host and container path roots; all symlink targets are written using the container path
- `TV_SOURCE`, `MOVIES_SOURCE`, `TV_LINKED` — derived from media roots
- `VIDEO_EXTS` — set of recognized video extensions

**Overrides**
- `NAME_OVERRIDES` — dict of raw parsed name → canonical display name, applied after `extract_show_and_season()` resolves a show name
- `ORPHAN_OVERRIDES` — dict of exact folder name → `(show_name, season_num)`, checked before any other logic in `scan_tv_source()`

**Core functions**
- `is_video()` — extension check against `VIDEO_EXTS`
- `is_sample()` — `RE_SAMPLE` word-boundary match
- `is_episode()` — OR of all episode regexes including `RE_BARE_EPISODE`
- `episode_info()` — returns `(season, episode)` tuple; tries each regex in order, bare `E\d+` returns season 1
- `sanitize_filename()` — replaces illegal chars with `-`
- `normalize_show_key()` — lowercases, strips straight and curly apostrophes, collapses whitespace; used as grouping key only, never as display name
- `extract_show_and_season()` — applies `SEASON_RE`, replaces dots with spaces if no spaces present, strips trailing 4-digit year, applies `NAME_OVERRIDES`
- `clean_show_name()` — applies `RE_STRIP` to derive a display name from a raw folder name; used for bare episode folders and miniseries
- `is_bare_episode_folder()` — scans folder contents; counts direct video files matching `RE_BARE_EPISODE` plus subdirectories whose names match `RE_BARE_EPISODE`; returns True if count ≥ 2
- `host_to_container()` — string replace of `MEDIA_ROOT_HOST` with `MEDIA_ROOT_CONTAINER`
- `make_symlink()` — skips if link already exists; translates path via `host_to_container()`; writes absolute container-side symlink
- `ensure_dir()` — `os.makedirs()` with `exist_ok=True`, no-ops on dry run
- `clean_broken_symlinks()` — `os.walk()` checking both file and directory symlinks; removes broken ones; second pass removes empty directories bottom-up

**scan_tv_source()**
- Builds `name_map` dict of `normalized_key → canonical_display_name`; first-seen variant wins as canonical
- `canonical()` inner function registers and returns the canonical name for a given show name
- Per entry: checks `ORPHAN_OVERRIDES` first, then skips non-directories, then tries `extract_show_and_season()`, then `is_bare_episode_folder()`, then falls through to pass-through
- Bare episode folders: `clean_show_name()` derives display name, `NAME_OVERRIDES` applied, grouped as season 1
- `grouped` dict: `canonical_name → [(season_num, folder_name), ...]`

**scan_movies_for_miniseries()**
- Scans `MOVIES_SOURCE` directories only
- For each folder: collects video files with valid `episode_info()` results
- Requires ≥ 2 episode files to qualify as miniseries
- Returns `show_name → (folder_name, sorted_episodes)` where episodes are `(season, ep_num, filename)` tuples

**normalize_for_compare() / collect_warnings()**
- `normalize_for_compare()` — strips `{tvdb-...}` tags, year, quality tokens, lowercases; used for overlap detection only
- `collect_warnings()` — two checks: duplicate season numbers within a grouped show; name overlap between grouped and pass-through entries

**main()**
- `--clean` runs `clean_broken_symlinks()` before anything else
- TV grouped shows: iterates sorted seasons, symlinks each season folder as a directory symlink under `Show Name/Season XX/`
- TV pass-through: symlinks entire folder as-is under its original name
- Miniseries: creates `Show Name/Season XX/` directory, symlinks individual episode files; if filename already has `S01E01` pattern it's used as-is, otherwise renamed to `Show Name.S01E01.ext`

---

**make_movies_links.py**

**Regexes**
- `RE_SXXEXX`, `RE_XNOTATION`, `RE_EPISODE`, `RE_NOF`, `RE_BARE_EPISODE` — same episode detection set as TV script
- `RE_SAMPLE` — same word-boundary sample exclusion
- `RE_YEAR` — requires preceding separator `[.\s\[\(]` before a `19xx` or `20xx` year; prevents `1917`, `2001` being read as years
- `RE_QUALITY` — extracts first quality token (`2160p`, `1080p`, `REMUX`, etc.) for multi-version labeling
- `RE_STRIP` — strips year and all quality/codec/release tokens; release group pattern (`[A-Z]{2,}-[A-Z][A-Za-z0-9]+`) is wrapped in `(?-i:...)` to make it case-sensitive, preventing hyphenated words like `Half-Blood` matching
- `RE_ILLEGAL_CHARS` — same as TV script

**Config**
- Same `MEDIA_ROOT_HOST` / `MEDIA_ROOT_CONTAINER` pattern
- `MOVIES_SOURCE`, `MOVIES_LINKED`
- `TMDB_API_KEY` — read from environment, falls back to hardcoded value

**Core functions**
- `is_video()`, `is_sample()`, `is_episode()` — same as TV script
- `is_miniseries_folder()` — scans folder for video files, counts those passing `is_episode()`; returns True if ≥ 2
- `extract_year()` — `RE_YEAR` search, returns first match group
- `extract_quality()` — `RE_QUALITY` search, returns uppercased match
- `clean_title()` — strips extension, applies `RE_STRIP`, replaces dots with spaces if no spaces present, strips leading date prefixes (`YYYY MM DD`), strips unclosed brackets at end
- `sanitize_filename()`, `host_to_container()`, `make_symlink()`, `ensure_dir()`, `clean_broken_symlinks()` — same pattern as TV script

**scan_movies()**
- Separates bare files from folders at `MOVIES_SOURCE` root
- Bare files: skips non-video, episode files (routed to TV), samples; extracts year, title, quality directly from filename
- Folders: skips miniseries folders; scans one level deep for video files excluding episodes and samples; picks largest file as primary; extracts year and quality from both folder name and primary filename
- Builds `seen` dict: `"Title (Year)" → [(entry_name, video_path, quality), ...]`
- Multi-version resolution: if only one version, quality label is None; if multiple, each gets a quality tag; same-quality duplicates get `.2`, `.3` suffixes via a per-quality counter

**tmdb_search()**
- Refuses titles shorter than 4 characters
- `urllib.request` GET to `api.themoviedb.org/3/search/movie`
- Returns `(canonical_title, year)` from first result's `title` and `release_date[:4]`

**resolve_flagged_via_tmdb()**
- Filters flagged list to `no year found` entries only
- `ThreadPoolExecutor` with 8 workers runs `tmdb_search()` concurrently per entry
- Per result: finds video file (bare file or largest in folder), creates symlink under `Found Title (Year)/`

**main()**
- `--clean` runs `clean_broken_symlinks()` first
- Iterates sorted movies list, builds `folder_name = "Title (Year)"`, appends ` - QUALITY` suffix if quality label present
- After main run: prompts interactively to resolve no-year flagged entries via TMDB or skip