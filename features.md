**make_tv_links.py**

- Groups bare season folders (`Show.Name.S01.720p...`) by show name under `Show Name/Season XX/`
- Pass-through for already-structured folders — symlinked as-is without renaming
- Miniseries detection — folders in `/movies/` with 2+ episode files routed to `tv-linked/` instead of `movies-linked/`
- Bare `E\d+` episode format detection — folders with no `S01` in name but containing `E01`-style files auto-detected and grouped as Season 1
- One-episode-per-subdir detection — bare `E\d+` subdirectory names counted alongside direct files (handles BBC Life, Frozen Planet style releases)
- Case-insensitive, apostrophe-agnostic show name grouping — `Blue's Clues`, `Blues Clues`, `BLUES CLUES` all merge to one group
- Trailing year stripped from show names — `Bluey 2018` → `Bluey`
- First-seen canonical name wins — variant spellings of the same show use whichever name was parsed first
- Episode formats detected: `S01E01`, `1x01`, `Episode.N`, `NofN`, bare `E01`
- `NAME_OVERRIDES` — manually correct show names that parse inconsistently or don't match TVDB
- `ORPHAN_OVERRIDES` — map bare `Season N/` folders with no show name to the correct show and season number
- Duplicate season warning — flags when two source folders map to the same show + season
- Pass-through name overlap warning — flags when a grouped show and a pass-through folder resolve to the same name
- `--dry-run` — full output preview with all planned symlinks, no files created
- `--clean` — removes broken symlinks and empty directories, then rebuilds
- Absolute container-side symlinks throughout — resolves correctly inside Docker regardless of working directory
- No hardlinks — avoids `EXDEV` failures on mergerfs pools
- Original files never modified — torrent seeding unaffected
- Illegal filename characters replaced with `-`
- Sample files excluded via word-boundary match
- `os.scandir()` throughout — avoids redundant `stat()` calls

---

**make_movies_links.py**

- Builds `Movie Name (Year)/Movie Name (Year).ext` structure from disorganized source
- Multi-version grouping — multiple copies of the same title+year coexist with quality suffixes (`1080P`, `2160P`, `REMUX`)
- Same-resolution duplicate handling — two `1080P` copies get `.2`, `.3` suffixes rather than colliding
- Year extraction with separator guard — preceding dot/space/bracket required so `1917` and `2001` aren't misread as years
- Title cleaning — strips quality tags, codec strings, release group tags, leading date prefixes, trailing brackets
- Release group tag matching is case-sensitive — prevents hyphenated words like `Half-Blood` being stripped as release group tokens
- Episode detection — folders with 2+ episode files skipped, routed to `tv-linked/` via `make_tv_links.py`
- Bare `E\d+` episode detection — `E01`-format multi-episode folders (no `S01` prefix) also correctly identified and skipped
- TMDB auto-lookup — entries with no detectable year flagged; at end of real run prompts to resolve via TMDB or skip
- TMDB lookup runs concurrently — 8 worker threads, `ThreadPoolExecutor`
- Largest file selected as primary when a folder contains multiple video files
- Quality label extracted from both folder name and primary filename
- `--dry-run` — full preview with source paths and planned symlink targets, no files created
- `--clean` — removes broken symlinks and empty directories, then rebuilds
- Absolute container-side symlinks throughout
- No hardlinks
- Original files never modified
- Illegal filename characters replaced with `-`
- Sample files excluded via word-boundary match
- `os.scandir()` throughout