#!/usr/bin/env python3
"""
make_tv_links.py  [v0.22] 

 - bare episode vs. folder matching fixes. Fuzzy match improvments. 
 - Bug fix - duplicate show folders from name mismatch between Pass 1 and Pass 2
 - Bug fix - trailing year in filename producing split show folders
 - New function: normalize_for_match()

Builds a Jellyfin/Sonarr-ready symlink tree from disorganized TV and
miniseries folders. Reads from /tv/ and /movies/, writes to /tv-linked/.
Original files are never touched - torrent seeding keeps working.

Output:
  tv-linked/
    Show Name/
      Season 01/  -> /tv/Show.Name.S01.720p.../        (folder symlink)
      Season 02/  -> /tv/Show.Name.S02.1080p.../       (folder symlink)
      Season 03/                                        (real dir, bare-file season)
        Show Name.S03E01.1080P.mkv  -> /tv/Show.Name.S03E01...mkv
    Already Structured Show (Year) {tvdb-xxxxx}/  -> symlinked as-is
    Miniseries Title/
      Season 01/
        Miniseries.S01E01.mkv  -> /movies/Miniseries.S01.../episode.mkv

Sources:
  /tv/      - Season folders (Show.Name.S01.720p...) grouped by show and
              symlinked under Show Name/Season XX/.
            - Folders already in correct Jellyfin structure are symlinked as-is.
            - Bare episode files (no parent folder) handled in Pass 2.
  /movies/  - Folders with 2+ episode files are treated as miniseries and
              symlinked into tv-linked/.

How it works:
  Pass 1:
    - Groups season folders by show name (case/apostrophe insensitive)
    - Passes through already-structured folders untouched
    - Pulls miniseries out of /movies/ automatically
    - Detects episode formats: S01E01, 1x01, Episode.N, NofN, bare E01, Part.N
    - Warns about duplicate seasons and name overlaps

  Pass 2 - bare episode files:
    - Scans /tv/ for video files sitting directly in the folder with no parent
    - Resolves show name via NAME_OVERRIDES -> TMDB TV search -> parsed fallback
    - Matches resolved name against Pass 1 results using aggressive fuzzy matching
    - Falls back to scanning existing tv-linked/ folders on disk if grouped match fails
    - Detects conflicts with existing season structures and prompts for resolution
    - Never overwrites or modifies any existing structure without user confirmation
    - Safe to re-run - already-linked episodes are silently skipped

  General:
    - Creates absolute symlinks using container paths (works inside Docker)
    - No hardlinks - avoids EXDEV on mergerfs pools

Setup:
  1. Set MEDIA_ROOT_HOST and MEDIA_ROOT_CONTAINER below
  2. (Optional) Set TMDB_API_KEY for automatic show name resolution on bare files
     Get a free key at https://www.themoviedb.org/settings/api
  3. Add NAME_OVERRIDES for shows that parse inconsistently or where TMDB
     returns the wrong result
  4. Add ORPHAN_OVERRIDES for bare "Season N" folders with no show name

Usage:
  python3 make_tv_links.py --dry-run   # preview, no changes
  python3 make_tv_links.py             # create symlinks
  python3 make_tv_links.py --clean     # remove broken links, then rebuild

Sonarr naming: https://wiki.servarr.com/sonarr/faq#what-is-the-correct-folder-structure-for-sonarr
Hardlink info: https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/
"""

import os
import re
import json
import argparse
import urllib.request
import urllib.parse

from common import (
    RE_SXXEXX, RE_BARE_EPISODE, RE_SAMPLE, RE_PART,
    RE_XNOTATION, RE_EPISODE, RE_NOF,
    is_video, is_sample, sanitize_filename, extract_quality,
    make_symlink, ensure_dir, clean_broken_symlinks,
)

# ---------------------------------------------------------------------------
# Config - set these to match your setup
# ---------------------------------------------------------------------------
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path inside Docker

TV_SOURCE     = f"{MEDIA_ROOT_HOST}/tv"
MOVIES_SOURCE = f"{MEDIA_ROOT_HOST}/movies"
TV_LINKED     = f"{MEDIA_ROOT_HOST}/tv-linked-test"

# Get a free API key at https://www.themoviedb.org/settings/api
# Used for automatic show name resolution on bare episode files.
# If not set, bare files fall back to the cleaned filename title.
TMDB_API_KEY = os.environ.get("TMDB_API_KEY", "")

# ---------------------------------------------------------------------------
# Regex patterns (TV-specific)
# ---------------------------------------------------------------------------

SEASON_RE = re.compile(r'^(.+?)[. ]([Ss])(\d{2})([Ee]\d+.*|[. ].*)$')

# Strip quality/codec/release tokens from folder names for title cleaning
RE_STRIP = re.compile(
    r'[. ]([Ss]\d{2}([Ee]\d{2})?|'
    r'\d{4}|'
    r'2160p|1080p|720p|576p|480p|'
    r'REPACK\d*|BluRay|BDRip|Blu-ray|'
    r'WEB-DL|WEBRip|AMZN|NF|HMAX|PMTP|HDTV|DVDRip|DVDrip|UHD|NTSC|PAL|'
    r'HDR\d*|DV|DDP[\d.]*|DD[\+\d.]*|DTS|FLAC[\d.]*|AAC[\d.]*|AC3|Opus|'
    r'x264|x265|H\.264|H\.265|h264|h265|AVC|HEVC|'
    r'REMASTERED|EXTENDED|UNRATED|LIMITED|DOCU|CRITERION|'
    r'[A-Z0-9]+-[A-Z][A-Za-z0-9]+)'
    r'.*$',
    re.IGNORECASE
)

# ---------------------------------------------------------------------------
# Overrides - run --dry-run first to see what names are being parsed
# ---------------------------------------------------------------------------

# Fix show names that parse inconsistently or don't match TVDB.
# These take priority over TMDB lookups.
# Format: "parsed name" -> "name you want"
NAME_OVERRIDES = {
    "A Pup Named Scooby Doo":    "A Pup Named Scooby-Doo",
    "A.Pup.Named Scooby-Doo":   "A Pup Named Scooby-Doo",
    "Scooby Doo Where Are You!": "Scooby Doo Where Are You",
    "Scooby-Doo Where Are You":  "Scooby Doo Where Are You",
    "The Office":                "The Office (US)",
    "The Office US":             "The Office (US)",
    # "3000" is stripped as a number token — override to preserve full title
    "Mystery Science Theater":   "Mystery Science Theater 3000",
}

# ORPHAN_OVERRIDES: map bare "Season N" folders (no show name in the
# folder itself) to their correct show. Run --dry-run first to identify
# orphans — they appear in [TV PASS-THROUGH] without a parent show name.
#
# Format: "folder name on disk" -> ("Show Name", season_number)
ORPHAN_OVERRIDES = {
    # Little Bear — bare Season N folders with no show name in the folder itself
    "Season 1": ("Little Bear", 1),
    "Season 2": ("Little Bear", 2),
    "Season 3": ("Little Bear", 3),
    "Season 4": ("Little Bear", 4),
    "Season 5": ("Little Bear", 5),
    # Folders using "Season.N" naming instead of "S04" — files inside are
    # standard S04E01 format so is_bare_episode_folder() won't catch them
    "Wild.Kratts.Season.4":  ("Wild Kratts", 4),
    "The Blue Planet Season 1": ("The Blue Planet", 1),
    # Planet Earth — folder has no S01 and files use abbreviated "pe.s01e01" prefix
    # so neither SEASON_RE nor is_bare_episode_folder() can detect it
    "Planet.Earth.1080p.BluRay.x264-CULTHD": ("Planet Earth", 1),
}



# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

def episode_info(filename):
    """Extract (season, episode) from a filename. Returns None if no match."""
    m = RE_SXXEXX.search(filename)
    if m:
        return int(m.group(1)), int(m.group(2))
    m = RE_XNOTATION.search(filename)
    if m:
        parts = re.split(r'x', m.group(0), flags=re.IGNORECASE)
        return int(parts[0]), int(parts[1])
    m = RE_EPISODE.search(filename)
    if m:
        return 1, int(m.group(1))
    m = RE_BARE_EPISODE.search(filename)
    if m:
        return 1, int(m.group(1))
    m = RE_NOF.search(filename)
    if m:
        return 1, int(m.group(1))
    m = RE_PART.search(filename)
    if m:
        return 1, int(m.group(1))
    return None


def normalize_show_key(name):
    """Normalize a show name for grouping within Pass 1 (case/apostrophe insensitive).
    Used only for grouping season folders under the same show name - not for
    cross-source matching. See normalize_for_match() for that."""
    name = name.lower()
    name = re.sub(r"['\u2019`]", '', name)
    name = re.sub(r'\s+', ' ', name)
    return name.strip()


def normalize_for_match(name):
    """Aggressive normalization used only for matching names across different sources.

    Strips articles, possessives, studio name prefixes, trailing years, and all
    punctuation so that names like "Marvel's Spidey and His Amazing Friends" and
    "Spidey and His Amazing Friends" resolve to the same key for comparison.

    Never used for display or folder creation - comparison only.
    """
    name = name.lower()
    name = re.sub(r"['\u2019`]", '', name)                    # apostrophes + possessives
    name = re.sub(r'^(the|a|an)\s+', '', name)                     # leading articles
    name = re.sub(r"^(marvels?|dcs?|disneys?|nbc|bbc)\s+", '', name)  # studio prefixes
    name = re.sub(r'\s+\d{4}$', '', name)                          # trailing years
    name = re.sub(r'[^a-z0-9\s]', '', name)                        # remaining punctuation
    name = re.sub(r'\s+', ' ', name)
    return name.strip()


def extract_show_and_season(folder_name):
    """Parse a folder name like 'Show.Name.S01.720p...' into (show_name, season_num)."""
    m = SEASON_RE.match(folder_name)
    if not m:
        return None, None
    raw_name   = m.group(1)
    season_num = int(m.group(3))
    show_name  = raw_name.replace('.', ' ').strip() if ' ' not in raw_name else raw_name.strip()
    show_name  = re.sub(r'\s+\d{4}$', '', show_name)
    show_name  = sanitize_filename(NAME_OVERRIDES.get(show_name, show_name))
    return show_name, season_num


def clean_show_name(folder_name):
    """Derive a display name from a raw folder name by stripping quality/codec tokens."""
    name = folder_name
    if ' ' not in name:
        name = name.replace('.', ' ')
    name = RE_STRIP.sub('', name)
    return sanitize_filename(name.strip(' .-_'))


def is_bare_episode_folder(folder_path):
    """True if folder has 2+ bare E\\d+ entries (files or subdirectories)."""
    count = 0
    with os.scandir(folder_path) as it:
        for entry in it:
            if entry.is_file() and is_video(entry.name) and RE_BARE_EPISODE.search(entry.name):
                count += 1
            elif entry.is_dir() and RE_BARE_EPISODE.search(entry.name):
                count += 1
            if count >= 2:
                return True
    return False


def _symlink(link_path, target_host_path, dry_run):
    """Wrapper around common.make_symlink with module-level config."""
    make_symlink(link_path, target_host_path, dry_run,
                 MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER)


def _prompt(message, valid):
    """Loop until the user enters a valid choice."""
    while True:
        choice = input(message).strip().lower()
        if choice in valid:
            return choice
        print(f"    Invalid - enter one of: {', '.join(valid)}")


# ---------------------------------------------------------------------------
# TMDB TV lookup
# ---------------------------------------------------------------------------

# Cache so the same show name only hits the API once per run,
# regardless of how many bare files share that show name.
_tmdb_tv_cache = {}


def tmdb_search_tv(parsed_name):
    """Search TMDB for a TV show by name. Returns canonical title string or None.
    Results are cached per parsed name so each unique name only hits the API once.
    """
    if parsed_name in _tmdb_tv_cache:
        return _tmdb_tv_cache[parsed_name]

    if not TMDB_API_KEY or len(parsed_name) < 3:
        _tmdb_tv_cache[parsed_name] = None
        return None

    params = urllib.parse.urlencode({"query": parsed_name, "api_key": TMDB_API_KEY})
    url = f"https://api.themoviedb.org/3/search/tv?{params}"
    try:
        with urllib.request.urlopen(url, timeout=8) as resp:
            data = json.loads(resp.read())
        results = data.get("results", [])
        if not results:
            _tmdb_tv_cache[parsed_name] = None
            return None
        canonical = sanitize_filename(results[0].get("name", parsed_name))
        _tmdb_tv_cache[parsed_name] = canonical
        return canonical
    except Exception:
        _tmdb_tv_cache[parsed_name] = None
        return None


def resolve_show_name(parsed_name):
    """Resolve a raw parsed show name to its canonical display name.

    Resolution order:
      1. NAME_OVERRIDES  - user-defined corrections, always win over TMDB
      2. TMDB TV search  - automatic canonical title from TMDB
      3. Parsed fallback - cleaned filename title used as-is
    """
    if parsed_name in NAME_OVERRIDES:
        return NAME_OVERRIDES[parsed_name]
    canonical = tmdb_search_tv(parsed_name)
    if canonical:
        return canonical
    return parsed_name


def find_matching_show(show_name, grouped):
    """Find the canonical show name in the Pass 1 grouped dict that matches
    show_name, using aggressive fuzzy matching via normalize_for_match().

    Falls back to scanning existing folders in tv-linked/ on disk if the
    grouped dict lookup fails. This catches cases where TMDB returned a
    different form of the name than what folder parsing produced (e.g.
    "Spidey..." vs "Marvel's Spidey..."), ensuring bare file episodes land
    under the existing folder rather than creating a duplicate.

    Returns the matched canonical name string, or None if no match found.
    """
    match_key = normalize_for_match(show_name)

    # Stage 1: check grouped dict from Pass 1
    for g in grouped:
        if normalize_for_match(g) == match_key:
            return g

    # Stage 2: check what already exists on disk in tv-linked/
    if os.path.isdir(TV_LINKED):
        try:
            with os.scandir(TV_LINKED) as it:
                for existing in it:
                    if (existing.is_dir() and
                            normalize_for_match(existing.name) == match_key):
                        return existing.name
        except (PermissionError, FileNotFoundError):
            pass

    return None


# ---------------------------------------------------------------------------
# Bare file helpers
# ---------------------------------------------------------------------------

def parse_bare_episode(filename):
    """Parse a bare episode filename into (show_name, season_num, episode_num, quality).
    Returns None if no recognizable SxxExx or NxNN pattern is found.

    Everything before the episode marker is treated as the show name.
    Dots are replaced with spaces when no spaces exist in the parsed title.
    Trailing years are stripped from the show name to match how Pass 1
    handles folder names (e.g. 'Fallout 2024' -> 'Fallout').
    """
    name = os.path.splitext(filename)[0]

    m = RE_SXXEXX.search(name)
    if m:
        season_num  = int(m.group(1))
        episode_num = int(m.group(2))
        raw_title   = name[:m.start()].strip(' .-_')
        show_name   = (raw_title.replace('.', ' ').strip()
                       if ' ' not in raw_title else raw_title.strip())
        show_name   = re.sub(r'\s+\d{4}$', '', show_name).strip()
        return show_name, season_num, episode_num, extract_quality(name)

    m = RE_XNOTATION.search(name)
    if m:
        parts       = re.split(r'x', m.group(0), flags=re.IGNORECASE)
        season_num  = int(parts[0])
        episode_num = int(parts[1])
        raw_title   = name[:m.start()].strip(' .-_')
        show_name   = (raw_title.replace('.', ' ').strip()
                       if ' ' not in raw_title else raw_title.strip())
        show_name   = re.sub(r'\s+\d{4}$', '', show_name).strip()
        return show_name, season_num, episode_num, extract_quality(name)

    return None


def episode_exists_in_folder(folder_path, episode_num, season_num):
    """Check whether a given episode already exists inside a source folder.
    Returns (exists: bool, quality: str or None).

    Used to determine whether a bare episode file is already covered by
    an existing season folder symlink before prompting the user.
    """
    pattern = re.compile(
        rf'[Ss]{season_num:02d}[Ee]{episode_num:02d}',
        re.IGNORECASE
    )
    try:
        with os.scandir(folder_path) as it:
            for entry in it:
                if entry.is_file() and is_video(entry.name):
                    if pattern.search(entry.name):
                        return True, extract_quality(entry.name)
    except (PermissionError, FileNotFoundError):
        pass
    return False, None


def _find_episode_symlink(season_dir, episode_num, season_num):
    """Check whether an individual episode symlink already exists in a real
    (non-symlink) season directory. Used for idempotency on re-runs.
    Returns the symlink path if found, None otherwise.
    """
    pattern = re.compile(
        rf'[Ss]{season_num:02d}[Ee]{episode_num:02d}',
        re.IGNORECASE
    )
    try:
        with os.scandir(season_dir) as it:
            for entry in it:
                if os.path.islink(entry.path) and pattern.search(entry.name):
                    return entry.path
    except (PermissionError, FileNotFoundError):
        pass
    return None


def convert_season_symlink_to_real_dir(show_name, season_num, season_symlink_path, dry_run):
    """Replace a season directory symlink with a real directory, then re-create
    individual episode symlinks for every video file in the original source folder.

    Only the tv-linked/ structure is modified. The source folder and its contents
    are never opened for writing or otherwise touched.

    Returns True if successful (or dry_run), False on error.
    """
    try:
        container_target   = os.readlink(season_symlink_path)
        source_folder_path = container_target.replace(MEDIA_ROOT_CONTAINER, MEDIA_ROOT_HOST, 1)
    except OSError as e:
        print(f"    [ERROR] Could not read symlink: {e}")
        return False

    if not os.path.isdir(source_folder_path):
        print(f"    [ERROR] Symlink target does not exist on disk: {source_folder_path}")
        return False

    try:
        with os.scandir(source_folder_path) as it:
            source_files = sorted(
                [e for e in it
                 if e.is_file() and is_video(e.name) and not is_sample(e.name)],
                key=lambda e: e.name
            )
    except (PermissionError, FileNotFoundError) as e:
        print(f"    [ERROR] Could not scan source folder: {e}")
        return False

    print(f"    Converting Season {season_num:02d} symlink -> real directory")
    print(f"    Source: {source_folder_path}")
    print(f"    Re-linking {len(source_files)} episode(s) as individual symlinks:")

    if not dry_run:
        os.remove(season_symlink_path)
        os.makedirs(season_symlink_path, exist_ok=True)

    for f in source_files:
        ep = parse_bare_episode(f.name)
        if ep:
            _, s, e, q = ep
            q_suffix  = f" - {q.upper()}" if q else ""
            link_name = f"{show_name}.S{s:02d}E{e:02d}{q_suffix}{os.path.splitext(f.name)[1]}"
        else:
            link_name = f.name

        link_path = os.path.join(season_symlink_path, link_name)
        print(f"      [LINK] {link_name}")
        if not dry_run:
            if not os.path.exists(link_path) and not os.path.islink(link_path):
                container_target = f.path.replace(MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER, 1)
                os.symlink(container_target, link_path)

    return True


# ---------------------------------------------------------------------------
# Pass 1 scanners
# ---------------------------------------------------------------------------

def scan_tv_source():
    """Scan /tv/ for season folders and group by show. Returns (grouped, passthrough).

    Bare files in /tv/ are intentionally skipped here - they are handled
    by scan_tv_bare_files() in Pass 2 after Pass 1 completes.
    """
    grouped     = {}
    name_map    = {}
    passthrough = []

    def canonical(show_name):
        key = normalize_show_key(show_name)
        if key not in name_map:
            name_map[key] = show_name
        return name_map[key]

    with os.scandir(TV_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        name = entry.name

        if name in ORPHAN_OVERRIDES:
            show_name, season_num = ORPHAN_OVERRIDES[name]
            grouped.setdefault(canonical(show_name), []).append((season_num, name))
            continue

        if not entry.is_dir():
            # Bare files are handled in Pass 2
            continue

        show_name, season_num = extract_show_and_season(name)
        if show_name:
            grouped.setdefault(canonical(show_name), []).append((season_num, name))
        elif is_bare_episode_folder(entry.path):
            show_name = clean_show_name(name)
            show_name = sanitize_filename(NAME_OVERRIDES.get(show_name, show_name))
            grouped.setdefault(canonical(show_name), []).append((1, name))
        else:
            passthrough.append(name)

    return grouped, passthrough


def scan_movies_for_miniseries():
    """Scan /movies/ for folders with 2+ episode files. Returns show_name -> (folder, episodes)."""
    results = {}

    with os.scandir(MOVIES_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        if not entry.is_dir():
            continue

        episodes = []
        with os.scandir(entry.path) as it2:
            files = sorted(it2, key=lambda e: e.name)

        for f in files:
            if not f.is_file() or not is_video(f.name) or is_sample(f.name):
                continue
            info = episode_info(f.name)
            if info:
                episodes.append((info[0], info[1], f.name))

        if len(episodes) >= 2:
            show_name = clean_show_name(entry.name)
            results[show_name] = (entry.name, sorted(episodes))

    return results


# ---------------------------------------------------------------------------
# Pass 2 - bare episode file scanner
# ---------------------------------------------------------------------------

def scan_tv_bare_files(grouped):
    """Scan /tv/ for bare video files and categorise each one.

    A bare file is a video file sitting directly in /tv/ with no parent folder.
    For each bare file:
      - Parses show name, season, episode, and quality from the filename
      - Strips trailing years from parsed show name (e.g. 'Fallout 2024' -> 'Fallout')
      - Resolves show name via NAME_OVERRIDES -> TMDB -> parsed fallback
      - Matches against Pass 1 results using aggressive fuzzy matching, then
        falls back to scanning existing tv-linked/ folders on disk

    Returns:
      bare_new       list of (show_name, season_num, episode_num, quality, filepath)
                     New episodes with no existing season structure at all.

      bare_conflicts list of (show_name, season_num, episode_num, quality, filepath,
                              conflict_type, info)
                     Episodes where season structure already exists.
                     conflict_type values:
                       'quality_variant'   episode exists in folder at different quality
                       'missing_episode'   episode not found in existing folder
                       'bare_dir_episode'  season real dir exists, episode not yet linked

      bare_unmatched list of filenames that could not be parsed into show/season/episode
    """
    bare_new       = []
    bare_conflicts = []
    bare_unmatched = []

    with os.scandir(TV_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        if not entry.is_file():
            continue
        if not is_video(entry.name) or is_sample(entry.name):
            continue

        result = parse_bare_episode(entry.name)
        if not result:
            bare_unmatched.append(entry.name)
            continue

        raw_show, season_num, episode_num, quality = result
        show_name = sanitize_filename(resolve_show_name(raw_show))

        # Use fuzzy matching to find an existing show in grouped or on disk.
        # This prevents duplicate show folders when TMDB and folder parsing
        # produce slightly different canonical names for the same show.
        matched_show = find_matching_show(show_name, grouped)

        if matched_show and matched_show != show_name:
            # Adopt the name already established by Pass 1 or on disk
            show_name = matched_show

        season_label = f"Season {season_num:02d}"
        season_path  = os.path.join(TV_LINKED, show_name, season_label)

        canonical_show = matched_show if matched_show in grouped else None

        if canonical_show is None:
            # Show has no folder-based seasons from Pass 1
            if os.path.isdir(season_path) and not os.path.islink(season_path):
                # Real dir from a previous bare-file run - check idempotency
                if _find_episode_symlink(season_path, episode_num, season_num):
                    continue  # Already linked - skip silently
                bare_conflicts.append((
                    show_name, season_num, episode_num, quality,
                    entry.path, 'bare_dir_episode', season_path
                ))
            else:
                # Completely new - no structure exists yet
                bare_new.append((show_name, season_num, episode_num, quality, entry.path))

        else:
            # Show exists in grouped - find whether this specific season is there
            seasons_for_show = grouped[canonical_show]
            source_folder = next(
                (sfolder for snum, sfolder in seasons_for_show if snum == season_num),
                None
            )

            if source_folder is None:
                # Show exists but not this season - treat as new
                bare_new.append((show_name, season_num, episode_num, quality, entry.path))
                continue

            # Season exists as a folder symlink - check if episode is in source folder
            source_path = os.path.join(TV_SOURCE, source_folder)
            exists, existing_quality = episode_exists_in_folder(
                source_path, episode_num, season_num
            )

            if exists:
                if (existing_quality and quality and
                        existing_quality.upper() != quality.upper()):
                    # Different quality - needs user decision
                    bare_conflicts.append((
                        show_name, season_num, episode_num, quality,
                        entry.path, 'quality_variant',
                        {'source_folder': source_folder,
                         'existing_quality': existing_quality}
                    ))
                # Same quality already covered by folder symlink - skip silently
            else:
                # Episode not in source folder at all
                bare_conflicts.append((
                    show_name, season_num, episode_num, quality,
                    entry.path, 'missing_episode',
                    {'source_folder': source_folder}
                ))

    return bare_new, bare_conflicts, bare_unmatched


# ---------------------------------------------------------------------------
# Pass 2 handlers
# ---------------------------------------------------------------------------

def handle_bare_new(bare_new, dry_run):
    """Create real season directories and individual episode symlinks for bare
    files with no existing season structure. No prompts - unambiguous new content.
    Idempotent: already-linked episodes from previous runs are silently skipped.
    """
    if not bare_new:
        return

    grouped_new = {}
    for show_name, season_num, episode_num, quality, filepath in bare_new:
        grouped_new.setdefault((show_name, season_num), []).append(
            (episode_num, quality, filepath)
        )

    print(f"\n[BARE FILES - NEW] {len(grouped_new)} show/season(s) with no existing structure:\n")

    for (show_name, season_num), episodes in sorted(grouped_new.items()):
        season_label = f"Season {season_num:02d}"
        show_dir     = os.path.join(TV_LINKED, show_name)
        season_dir   = os.path.join(show_dir, season_label)

        print(f"  {show_name} / {season_label}  ({len(episodes)} episode(s))")

        ensure_dir(show_dir, dry_run)
        ensure_dir(season_dir, dry_run)

        for episode_num, quality, filepath in sorted(episodes):
            ext       = os.path.splitext(filepath)[1]
            q_suffix  = f" - {quality.upper()}" if quality else ""
            link_name = f"{show_name}.S{season_num:02d}E{episode_num:02d}{q_suffix}{ext}"
            link_path = os.path.join(season_dir, link_name)

            if os.path.islink(link_path) and os.path.exists(link_path):
                print(f"    [SKIP] {link_name}")
                continue

            _symlink(link_path, filepath, dry_run)


def handle_bare_conflicts(bare_conflicts, dry_run):
    """Interactively handle bare file conflicts with existing season structure.

    Three conflict types:

      quality_variant:
        Episode exists in the season folder at a different quality.
        Converting the season symlink to a real directory is required so both
        versions can coexist as individual episode symlinks.

      missing_episode:
        Episode is not in the existing season folder at all.
        Same conversion required so the bare file can be added individually.

      bare_dir_episode:
        Season real dir already exists from a previous bare-file run.
        New episode is added to the existing real directory.

    In dry-run mode, all conflicts are listed with their planned action.
    No changes are made and no prompts are shown.

    No existing file or symlink is ever modified without user confirmation.
    """
    if not bare_conflicts:
        return

    print(f"\n[BARE FILES - CONFLICTS] {len(bare_conflicts)} episode(s) need attention:\n")

    if dry_run:
        for show_name, season_num, episode_num, quality, filepath, ctype, info in bare_conflicts:
            season_label = f"Season {season_num:02d}"
            print(f"  {show_name} / {season_label} / E{episode_num:02d}"
                  f"  [{quality or 'unknown quality'}]")
            print(f"    File: {os.path.basename(filepath)}")
            if ctype == 'quality_variant':
                print(f"    Existing in folder '{info['source_folder']}': "
                      f"{info['existing_quality']}")
                print(f"    Action: Convert season symlink to real dir, add quality variant")
            elif ctype == 'missing_episode':
                print(f"    Not found in folder '{info['source_folder']}'")
                print(f"    Action: Convert season symlink to real dir, add episode")
            elif ctype == 'bare_dir_episode':
                print(f"    Season real dir exists: {info}")
                print(f"    Action: Add episode symlink to existing season dir")
            print()
        print("  (run without --dry-run to resolve these interactively)")
        return

    for show_name, season_num, episode_num, quality, filepath, ctype, info in bare_conflicts:
        season_label = f"Season {season_num:02d}"
        filename     = os.path.basename(filepath)
        ext          = os.path.splitext(filename)[1]
        q_suffix     = f" - {quality.upper()}" if quality else ""
        link_name    = f"{show_name}.S{season_num:02d}E{episode_num:02d}{q_suffix}{ext}"
        season_path  = os.path.join(TV_LINKED, show_name, season_label)

        print(f"\n  {show_name} / {season_label} / E{episode_num:02d}")
        print(f"    File: {filename}")

        if ctype in ('quality_variant', 'missing_episode'):
            if ctype == 'quality_variant':
                print(f"    Episode already exists in season folder as "
                      f"{info['existing_quality']}")
                print(f"    This file is {quality or 'unknown quality'}")
            else:
                print(f"    Episode not found in season folder: {info['source_folder']}")

            print(f"    Season {season_label} is currently a folder symlink.")
            print(f"    Adding this episode requires converting it to a real directory")
            print(f"    and re-linking all existing episodes as individual symlinks.")
            print(f"    Source files are never touched.")
            print()
            print(f"    [1] Convert season to real dir and add this episode")
            print(f"    [s] Skip - handle manually")

            choice = _prompt("    Choice: ", ("1", "s"))
            if choice == "1":
                ok = convert_season_symlink_to_real_dir(
                    show_name, season_num, season_path, dry_run
                )
                if ok:
                    link_path = os.path.join(season_path, link_name)
                    _symlink(link_path, filepath, dry_run)
            else:
                print(f"    [SKIP] {filename} - skipped, handle manually")

        elif ctype == 'bare_dir_episode':
            print(f"    Season real dir already exists: {info}")
            print(f"    Quality: {quality or 'unknown'}")
            print()
            print(f"    [1] Add episode symlink to existing season dir")
            print(f"    [s] Skip")

            choice = _prompt("    Choice: ", ("1", "s"))
            if choice == "1":
                link_path = os.path.join(season_path, link_name)
                _symlink(link_path, filepath, dry_run)
            else:
                print(f"    [SKIP] {filename} - skipped")


# ---------------------------------------------------------------------------
# Warnings
# ---------------------------------------------------------------------------

def normalize_for_compare(name):
    """Strip year, TVDB tags, quality tokens from a name for overlap detection."""
    name = re.sub(r'\{[^}]+\}', '', name)
    name = re.sub(r'\(\d{4}\)', '', name)
    name = clean_show_name(name)
    return name.lower().strip()


def collect_warnings(tv_grouped, tv_passthrough):
    """Detect duplicate seasons and grouped/pass-through name overlaps."""
    warnings = []

    for show_name, seasons in tv_grouped.items():
        seen_seasons = {}
        for season_num, folder in seasons:
            if season_num in seen_seasons:
                warnings.append(
                    f"Duplicate season: {show_name} S{season_num:02d} "
                    f"appears in both '{seen_seasons[season_num]}' and '{folder}'"
                )
            else:
                seen_seasons[season_num] = folder

    grouped_normalized = {normalize_for_compare(name): name for name in tv_grouped}
    for pt_entry in tv_passthrough:
        norm = normalize_for_compare(pt_entry)
        if norm in grouped_normalized:
            warnings.append(
                f"Name overlap: grouped show '{grouped_normalized[norm]}' and "
                f"pass-through '{pt_entry}' resolve to the same name - "
                f"tv-linked/ will have two separate entries"
            )

    return warnings


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main(dry_run, clean):
    if clean:
        if os.path.isdir(TV_LINKED):
            print(f"[CLEAN] Removing broken symlinks from {TV_LINKED}...\n")
            clean_broken_symlinks(TV_LINKED)
        else:
            print(f"[CLEAN] {TV_LINKED} does not exist, nothing to clean.\n")

    ensure_dir(TV_LINKED, dry_run)

    # Pass 1 - folder-based sources
    tv_grouped, tv_passthrough = scan_tv_source()
    miniseries = scan_movies_for_miniseries()

    print(f"\n{'[DRY RUN] ' if dry_run else ''}TV Symlink Plan")
    print("=" * 60)

    print(f"\n[TV SOURCE] {len(tv_grouped)} shows:\n")
    for show_name, seasons in sorted(tv_grouped.items()):
        seasons_sorted = sorted(seasons, key=lambda x: x[0])
        season_str = ", ".join(f"S{s:02d}" for s, _ in seasons_sorted)
        print(f"  {show_name}  ({season_str})")

        show_dir = os.path.join(TV_LINKED, show_name)
        ensure_dir(show_dir, dry_run)

        for season_num, orig_folder in seasons_sorted:
            season_label = f"Season {season_num:02d}"
            link_path    = os.path.join(TV_LINKED, show_name, season_label)
            target       = os.path.join(TV_SOURCE, orig_folder)
            _symlink(link_path, target, dry_run)

    print(f"\n[TV PASS-THROUGH] {len(tv_passthrough)} folders (symlinked as-is):\n")
    for entry in sorted(tv_passthrough):
        print(f"  {entry}")
        link_path = os.path.join(TV_LINKED, entry)
        target    = os.path.join(TV_SOURCE, entry)
        _symlink(link_path, target, dry_run)

    print(f"\n[MINISERIES from MOVIES] {len(miniseries)} shows:\n")
    for show_name, (folder_name, episodes) in sorted(miniseries.items()):
        print(f"  {show_name}  ({len(episodes)} episodes)  [movies/{folder_name}]")

        season_num   = episodes[0][0]
        season_label = f"Season {season_num:02d}"
        season_dir   = os.path.join(TV_LINKED, show_name, season_label)
        ensure_dir(season_dir, dry_run)

        for season, ep_num, filename in episodes:
            orig_path = os.path.join(MOVIES_SOURCE, folder_name, filename)
            ext       = os.path.splitext(filename)[1]
            link_name = (filename if RE_SXXEXX.search(filename)
                         else f"{show_name}.S{season:02d}E{ep_num:02d}{ext}")
            link_path = os.path.join(season_dir, link_name)
            _symlink(link_path, orig_path, dry_run)

        print()

    # Pass 2 - bare episode files in /tv/
    bare_new, bare_conflicts, bare_unmatched = scan_tv_bare_files(tv_grouped)

    handle_bare_new(bare_new, dry_run)
    handle_bare_conflicts(bare_conflicts, dry_run)

    if bare_unmatched:
        print(f"\n[BARE FILES - UNMATCHED] {len(bare_unmatched)} file(s) could not be parsed:\n")
        for fname in bare_unmatched:
            print(f"  {fname}")
        print("  These need a NAME_OVERRIDE entry or manual placement.")

    # Warnings
    warnings = collect_warnings(tv_grouped, tv_passthrough)
    if warnings:
        print(f"\n[WARNINGS] {len(warnings)} issue(s) need attention:\n")
        for w in warnings:
            print(f"  [WARN] {w}")
        print()

    print("=" * 60)
    if dry_run:
        print("DRY RUN complete - no files or folders created.")
        print("Run without --dry-run to apply.")
    else:
        print(f"\nDone. Point Jellyfin/Sonarr at: {TV_LINKED}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Build a Jellyfin/Sonarr-ready symlink tree from TV and miniseries folders."
    )
    parser.add_argument("--dry-run", action="store_true",
                        help="Preview without creating anything")
    parser.add_argument("--clean", action="store_true",
                        help="Remove broken symlinks + empty dirs before rebuilding")
    args = parser.parse_args()
    main(args.dry_run, args.clean)