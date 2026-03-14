#!/usr/bin/env python3
"""
make_tv_links.py  [v2.0]

Builds a Jellyfin/Sonarr-ready symlink tree from disorganized TV and
miniseries folders. Reads from /tv/ and /movies/, writes to /tv-linked/.
Original files are never touched - torrent seeding keeps working.

Output:
  tv-linked/
    Show Name/
      Season 01/  -> /tv/Show.Name.S01.720p.../
      Season 02/  -> /tv/Show.Name.S02.1080p.../
    Already Structured Show (Year) {tvdb-xxxxx}/  -> symlinked as-is
    Miniseries Title/
      Season 01/
        Miniseries.S01E01.mkv  -> /movies/Miniseries.S01.../episode.mkv

Sources:
  /tv/      - Bare season folders (Show.Name.S01.720p...) are grouped by show
              and symlinked under Show Name/Season XX/.
            - Folders already in correct Jellyfin structure are symlinked as-is.
  /movies/  - Folders with 2+ episode files are treated as miniseries and
              symlinked into tv-linked/.

How it works:
  - Groups bare season folders by show name (case/apostrophe insensitive)
  - Passes through already-structured folders untouched
  - Pulls miniseries out of /movies/ automatically
  - Detects episode formats: S01E01, 1x01, Episode.N, NofN, bare E01, Part.N
  - Warns about duplicate seasons and name overlaps
  - Creates absolute symlinks using container paths (works inside Docker)
  - No hardlinks - avoids EXDEV on mergerfs pools

Setup:
  1. Set MEDIA_ROOT_HOST and MEDIA_ROOT_CONTAINER below
  2. Add NAME_OVERRIDES for shows that parse inconsistently
  3. Add ORPHAN_OVERRIDES for bare "Season N" folders with no show name

Usage:
  python3 make_tv_links.py --dry-run   # preview, no changes
  python3 make_tv_links.py             # create symlinks
  python3 make_tv_links.py --clean     # remove broken links, then rebuild

Sonarr naming: https://wiki.servarr.com/sonarr/faq#what-is-the-correct-folder-structure-for-sonarr
Hardlink info: https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/
"""

import os
import re
import argparse

from common import (
    RE_SXXEXX, RE_BARE_EPISODE, RE_SAMPLE, RE_PART, RE_XNOTATION, RE_EPISODE, RE_NOF,
    is_video, is_sample, sanitize_filename,
    make_symlink, ensure_dir, clean_broken_symlinks,
)

# ---------------------------------------------------------------------------
# Config - set these to match your setup
# ---------------------------------------------------------------------------
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path inside Docker

TV_SOURCE     = f"{MEDIA_ROOT_HOST}/tv"
MOVIES_SOURCE = f"{MEDIA_ROOT_HOST}/movies"
TV_LINKED     = f"{MEDIA_ROOT_HOST}/tv-linked" # Will create the dir if doesnt exist

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
# Format: "parsed name" -> "name you want"
NAME_OVERRIDES = {
    #"Scooby-Doo Where Are You":  "Scooby Doo Where Are You",
    #"The Office US":             "The Office (US)",
    #"Mystery Science Theater":   "Mystery Science Theater 3000",
}

# Map bare "Season N" folders (no show name) to the correct show.
# Format: "folder name on disk" -> ("Show Name", season_number)
ORPHAN_OVERRIDES = {
    #"Season 1": ("Little Bear", 1),
    #"Season 2": ("Little Bear", 2),
    #"Wild.Kratts.Season.4":  ("Wild Kratts", 4),
    #"Planet.Earth.1080p.BluRay.x264-CULTHD": ("Planet Earth", 1),
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
    # Part.N as last resort - catches miniseries like "Show.Part.1.mkv"
    m = RE_PART.search(filename)
    if m:
        return 1, int(m.group(1))
    return None


def normalize_show_key(name):
    """Normalize a show name for grouping (case/apostrophe insensitive)."""
    name = name.lower()
    name = re.sub(r"['\u2019`]", '', name)
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


# ---------------------------------------------------------------------------
# Scanners
# ---------------------------------------------------------------------------

def scan_tv_source():
    """Scan /tv/ and group season folders by show. Returns (grouped, passthrough)."""
    grouped     = {}
    name_map    = {}
    passthrough = []

    def canonical(show_name):
        """Register and return the canonical display name for a show."""
        key = normalize_show_key(show_name)
        if key not in name_map:
            name_map[key] = show_name
        return name_map[key]

    with os.scandir(TV_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        name = entry.name

        # Check orphan overrides first
        if name in ORPHAN_OVERRIDES:
            show_name, season_num = ORPHAN_OVERRIDES[name]
            grouped.setdefault(canonical(show_name), []).append((season_num, name))
            continue

        if not entry.is_dir():
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

    # Duplicate seasons within a grouped show
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

    # Grouped show and pass-through folder resolve to the same name
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
    tv_grouped, tv_passthrough = scan_tv_source()
    miniseries = scan_movies_for_miniseries()

    print(f"\n{'[DRY RUN] ' if dry_run else ''}TV Symlink Plan")
    print("=" * 60)

    # Grouped shows from /tv/
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

    # Pass-through (already structured)
    print(f"\n[TV PASS-THROUGH] {len(tv_passthrough)} folders (symlinked as-is):\n")
    for entry in sorted(tv_passthrough):
        print(f"  {entry}")
        link_path = os.path.join(TV_LINKED, entry)
        target    = os.path.join(TV_SOURCE, entry)
        _symlink(link_path, target, dry_run)

    # Miniseries from /movies/
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
            link_name = filename if RE_SXXEXX.search(filename) else f"{show_name}.S{season:02d}E{ep_num:02d}{ext}"
            link_path = os.path.join(season_dir, link_name)
            _symlink(link_path, orig_path, dry_run)

        print()

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