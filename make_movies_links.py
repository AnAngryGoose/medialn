#!/usr/bin/env python3
"""
make_movies_links.py  [v0.23]

Builds a Jellyfin/Radarr-ready symlink tree from a messy movies folder.
Reads from /movies/, writes to /movies-linked/. Original files are never
touched - torrent seeding keeps working.

Output:
  movies-linked/
    Movie Name (Year)/
      Movie Name (Year).mkv              single version
      Movie Name (Year) - 1080P.mkv      multiple versions, quality tagged
      Movie Name (Year) - 2160P.mkv
      Movie Name (Year) - 1080P.2.mkv    same-resolution duplicates get .2, .3

How it works:
  - Scans /movies/ for video files and folders
  - Extracts title and year from folder/file names
  - Skips miniseries folders (2+ episode files) - those go to make_tv_links.py
  - Flags Part.N folders as ambiguous (could be multi-part movie or miniseries)
  - Groups multiple versions of the same movie with quality suffixes
  - Creates absolute symlinks using container paths (works inside Docker)
  - No hardlinks - avoids EXDEV on mergerfs pools
  - TMDB lookup available for entries missing a year

Setup:
  1. Set MEDIA_ROOT_HOST and MEDIA_ROOT_CONTAINER below
  2. (Optional) Set TMDB_API_KEY for auto-lookup of yearless entries
     Get a free key at https://www.themoviedb.org/settings/api

Usage:
  python3 make_movies_links.py --dry-run   # preview, no changes
  python3 make_movies_links.py             # create symlinks
  python3 make_movies_links.py --clean     # remove broken links, then rebuild

TMDB API: https://developer.themoviedb.org/reference/search-movie
"""

import os
import re
import json
import argparse
import urllib.request
import urllib.parse
from concurrent.futures import ThreadPoolExecutor, as_completed

from common import (
    VIDEO_EXTS, RE_SXXEXX, RE_XNOTATION, RE_EPISODE, RE_NOF,
    RE_BARE_EPISODE, RE_SAMPLE, RE_ILLEGAL_CHARS, RE_PART,
    is_video, is_sample, is_episode, sanitize_filename, extract_quality,
    make_symlink, ensure_dir, clean_broken_symlinks,
    find_videos_in_folder, largest_video,
)

# ---------------------------------------------------------------------------
# Config - set these to match your setup
# ---------------------------------------------------------------------------
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"   # path on the host
MEDIA_ROOT_CONTAINER = "/data/media"               # same path inside Docker

MOVIES_SOURCE = f"{MEDIA_ROOT_HOST}/movies"
MOVIES_LINKED = f"{MEDIA_ROOT_HOST}/movies-linked"

# Get a free API key at https://www.themoviedb.org/settings/api
TMDB_API_KEY = os.environ.get("TMDB_API_KEY", "")

# ---------------------------------------------------------------------------
# Regex patterns (movies-specific)
# ---------------------------------------------------------------------------

# Year extraction - must be preceded by a separator so "1917" isn't read as a year
RE_YEAR = re.compile(r'(?<=[.\s\[\(])((?:19|20)\d{2})(?=[.\s\]\)]|$)')

# Strip everything from first quality/source tag onward (for title cleaning).
# Release group pattern uses (?-i:...) so hyphenated words like "Half-Blood"
# don't get stripped as release groups.
RE_STRIP = re.compile(
    r'[. \(](?:'
    r'(?:19|20)\d{2}|'
    r'2160p|1080p|720p|576p|480p|'
    r'REPACK\d*|BluRay|BDRip|Blu-ray|'
    r'WEB-DL|WEBRip|AMZN|NF|HMAX|PMTP|HDTV|DVDRip|DVDrip|UHD|'
    r'HDR\d*|DV|DDP[\d.]*|DD[\+\d.]*|DTS|FLAC[\d.]*|AAC[\d.]*|AC3|Opus|'
    r'x264|x265|H\.264|H\.265|h264|h265|AVC|HEVC|'
    r'REMASTERED|EXTENDED|UNRATED|LIMITED|DOCU|CRITERION|PROPER|'
    r'(?-i:[A-Z]{2,}-[A-Z][A-Za-z0-9]+))'
    r'.*$',
    re.IGNORECASE
)


# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

def is_miniseries_folder(folder_path):
    """True if folder contains 2+ files that look like episodes."""
    count = 0
    with os.scandir(folder_path) as it:
        for entry in it:
            if entry.is_file() and is_video(entry.name) and is_episode(entry.name):
                count += 1
            if count >= 2:
                return True
    return False


def is_ambiguous_parts_folder(folder_path):
    """True if folder has 2+ Part.N video files but no standard episode markers."""
    part_files = []
    with os.scandir(folder_path) as it:
        for entry in it:
            if (entry.is_file()
                    and is_video(entry.name)
                    and not is_sample(entry.name)
                    and not is_episode(entry.name)
                    and RE_PART.search(entry.name)):
                part_files.append(entry.name)
    part_files.sort()
    return len(part_files) >= 2, part_files


def extract_year(name):
    """Pull a 4-digit year from a name, requires a preceding separator."""
    m = RE_YEAR.search(name)
    return m.group(1) if m else None


def clean_title(name):
    """Strip extension, quality tags, trailing dots/brackets from a name."""
    name = os.path.splitext(name)[0]
    stripped = RE_STRIP.sub('', name)
    if ' ' not in stripped:
        stripped = stripped.replace('.', ' ')
    # Remove leading date prefixes like "2011.12.31."
    stripped = re.sub(r'^\d{4}[\s.]\d{2}[\s.]\d{2}[\s.]+', '', stripped)
    # Remove trailing unclosed bracket groups
    stripped = re.sub(r'[\[\(][^\[\(]*$', '', stripped)
    return stripped.strip(' .-_[]()').strip()


def _symlink(link_path, target_host_path, dry_run):
    """Wrapper around common.make_symlink with module-level config."""
    make_symlink(link_path, target_host_path, dry_run,
                 MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER)


# ---------------------------------------------------------------------------
# TMDB lookup
# ---------------------------------------------------------------------------

def tmdb_search(title):
    """Search TMDB for a movie title. Returns (title, year) or None."""
    if len(title) < 4:
        return None
    params = urllib.parse.urlencode({"query": title, "api_key": TMDB_API_KEY})
    url = f"https://api.themoviedb.org/3/search/movie?{params}"
    try:
        with urllib.request.urlopen(url, timeout=8) as resp:
            data = json.loads(resp.read())
        results = data.get("results", [])
        if not results:
            return None
        top = results[0]
        year = top.get("release_date", "")[:4] or None
        found_title = sanitize_filename(top.get("title", title))
        return found_title, year
    except Exception:
        return None


# ---------------------------------------------------------------------------
# Scanner
# ---------------------------------------------------------------------------

def scan_movies():
    """
    Scan MOVIES_SOURCE and categorize each entry.

    Returns:
      movies    - [(entry, title, year, video_path, quality_label), ...]
      flagged   - [(entry, reason), ...]
      skipped   - [entry, ...]  (miniseries folders)
      ambiguous - [(entry, part_files), ...]  (Part.N folders needing manual routing)
    """
    raw_entries = []
    flagged     = []
    skipped     = []
    ambiguous   = []

    # seen tracks title+year groupings: key -> [(entry, video_path, quality)]
    seen = {}

    with os.scandir(MOVIES_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        name = entry.name

        # Bare video file at root
        if entry.is_file():
            if not is_video(name) or is_sample(name):
                continue
            if is_episode(name):
                skipped.append(name)
                continue
            year       = extract_year(name)
            title      = clean_title(name)
            quality    = extract_quality(name)
            video_path = entry.path

        # Folder
        elif entry.is_dir():
            is_parts, part_files = is_ambiguous_parts_folder(entry.path)
            if is_parts:
                ambiguous.append((name, part_files))
                continue
            if is_miniseries_folder(entry.path):
                skipped.append(name)
                continue

            videos = find_videos_in_folder(entry.path)
            if not videos:
                flagged.append((name, "no video file found in folder"))
                continue

            primary    = largest_video(videos)
            video_path = primary.path
            year       = extract_year(name) or extract_year(primary.name)
            title      = clean_title(name)
            quality    = extract_quality(name) or extract_quality(primary.name)
        else:
            continue

        # Validate
        if not title:
            flagged.append((name, "could not parse title"))
            continue
        if not year:
            flagged.append((name, "no year found - needs manual match"))
            continue

        link_key = f"{title} ({year})"
        seen.setdefault(link_key, []).append((name, title, year, video_path, quality))

    # Resolve multi-version grouping
    movies = _resolve_versions(seen)
    movies.sort(key=lambda x: (x[1].lower(), x[2]))
    return movies, flagged, skipped, ambiguous


def _resolve_versions(seen):
    """
    Turn the seen dict into a flat movies list. Single versions get no quality
    label. Multiple versions each get a quality suffix, with .2/.3 for
    same-resolution duplicates.
    """
    movies = []
    for link_key, versions in seen.items():
        if len(versions) == 1:
            entry, title, year, video_path, _ = versions[0]
            movies.append((entry, title, year, video_path, None))
            continue

        # Fill in missing quality tags
        resolved = []
        for entry, title, year, video_path, quality in versions:
            if not quality:
                quality = extract_quality(os.path.basename(video_path)) or "UNKNOWN"
            resolved.append((entry, title, year, video_path, quality))

        # Count per-quality for duplicate suffixing
        quality_counts = {}
        for _, _, _, _, q in resolved:
            quality_counts[q] = quality_counts.get(q, 0) + 1

        quality_seen = {}
        for entry, title, year, video_path, quality in resolved:
            if quality_counts[quality] == 1:
                label = quality
            else:
                quality_seen[quality] = quality_seen.get(quality, 0) + 1
                count = quality_seen[quality]
                label = quality if count == 1 else f"{quality}.{count}"
            movies.append((entry, title, year, video_path, label))

    return movies


# ---------------------------------------------------------------------------
# TMDB resolution for flagged entries
# ---------------------------------------------------------------------------

def resolve_flagged_via_tmdb(flagged, dry_run):
    """Search TMDB concurrently for flagged 'no year' entries and create symlinks."""
    print(f"\n[TMDB LOOKUP] {'(dry run) ' if dry_run else ''}Resolving flagged entries...\n")

    no_year = [(entry, reason) for entry, reason in flagged
               if reason == "no year found - needs manual match"]

    def lookup(entry):
        title = clean_title(entry)
        if not title or len(title) < 4:
            return entry, None, None, f"parsed title too short: {title!r}"
        result = tmdb_search(title)
        if not result:
            return entry, None, None, f"no TMDB result for: {title!r}"
        found_title, year = result
        return entry, found_title, year, None

    with ThreadPoolExecutor(max_workers=8) as pool:
        futures = {pool.submit(lookup, entry): entry for entry, _ in no_year}
        results = sorted(
            (future.result() for future in as_completed(futures)),
            key=lambda r: r[0]
        )

    for entry, found_title, year, error in results:
        if error:
            tag = "[FAIL]" if "too short" in error else "[MISS]"
            print(f"  {tag}  {entry}")
            print(f"          {error}")
            continue

        entry_path = os.path.join(MOVIES_SOURCE, entry)
        if os.path.isfile(entry_path):
            video_path = entry_path
        else:
            videos = find_videos_in_folder(entry_path)
            if not videos:
                print(f"  [FAIL]  {entry}")
                print(f"          TMDB found: {found_title} ({year}) but no video file in folder")
                continue
            video_path = largest_video(videos).path

        folder_name = f"{found_title} ({year})" if year else found_title
        ext         = os.path.splitext(video_path)[1]
        link_name   = f"{folder_name}{ext}"
        link_dir    = os.path.join(MOVIES_LINKED, folder_name)
        link_path   = os.path.join(link_dir, link_name)

        print(f"  [FIND]  {clean_title(entry)!r}  ->  {found_title} ({year})")
        ensure_dir(link_dir, dry_run)
        _symlink(link_path, video_path, dry_run)


# ---------------------------------------------------------------------------
# Ambiguous Part.N interactive handler
# ---------------------------------------------------------------------------

def handle_ambiguous(ambiguous, dry_run):
    """Prompt user to route Part.N folders as movie or TV."""
    print(f"\n[AMBIGUOUS - PART FILES] {len(ambiguous)} entries need manual routing:\n")

    if dry_run:
        for entry, part_files in ambiguous:
            title = clean_title(entry)
            year  = extract_year(entry)
            label = f"{title} ({year})" if year else title
            print(f"  {label}")
            for pf in part_files:
                print(f"    {pf}")
        print("\n  (run without --dry-run to route these interactively)")
        return

    for entry, part_files in ambiguous:
        title = clean_title(entry)
        year  = extract_year(entry)
        label = f"{title} ({year})" if year else title

        print(f"\n  {label}")
        for pf in part_files:
            print(f"    {pf}")
        print()
        print("    [1] Movie        -> movies-linked/  (picks largest file)")
        print("    [2] TV/Miniseries -> skip  (make_tv_links.py will handle)")
        print("    [s] Skip for now")

        while True:
            choice = input("    Choice: ").strip().lower()
            if choice in ("1", "2", "s"):
                break
            print("    Invalid choice - enter 1, 2, or s")

        if choice == "1":
            _route_ambiguous_as_movie(entry, title, year, part_files, dry_run)
        elif choice == "2":
            print(f"    [TV] Skipped - run make_tv_links.py to route to tv-linked/")
        else:
            print(f"    [SKIP] {label} - skipped for now")


def _route_ambiguous_as_movie(entry, title, year, part_files, dry_run):
    """Symlink an ambiguous Part.N folder as a movie (largest file wins)."""
    folder_path = os.path.join(MOVIES_SOURCE, entry)
    videos = find_videos_in_folder(folder_path, exclude_episodes=False)
    if not videos:
        print(f"    [FAIL] No video files found in {entry}")
        return

    primary     = largest_video(videos)
    video_path  = primary.path
    movie_year  = year or extract_year(primary.name)
    movie_title = title or clean_title(primary.name)
    quality     = extract_quality(entry) or extract_quality(primary.name)

    if not movie_year:
        print(f"    [WARN] No year found for {movie_title} - skipping, add manually")
        return

    folder_name = f"{movie_title} ({movie_year})"
    ext         = os.path.splitext(video_path)[1]
    link_name   = f"{folder_name} - {quality}{ext}" if quality else f"{folder_name}{ext}"
    link_dir    = os.path.join(MOVIES_LINKED, folder_name)
    link_path   = os.path.join(link_dir, link_name)

    print(f"    [MOVIE] {folder_name}")
    if len(part_files) > 1:
        print(f"    [NOTE]  Only largest file linked - {len(part_files) - 1} part file(s) not linked")

    ensure_dir(link_dir, dry_run)
    _symlink(link_path, video_path, dry_run)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main(dry_run, clean):
    if clean:
        if os.path.isdir(MOVIES_LINKED):
            print(f"[CLEAN] Removing broken symlinks from {MOVIES_LINKED}...\n")
            clean_broken_symlinks(MOVIES_LINKED, MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER)
        else:
            print(f"[CLEAN] {MOVIES_LINKED} does not exist, nothing to clean.\n")

    ensure_dir(MOVIES_LINKED, dry_run)
    movies, flagged, skipped, ambiguous = scan_movies()

    print(f"\n{'[DRY RUN] ' if dry_run else ''}Movies Symlink Plan")
    print("=" * 60)
    print(f"\n[MOVIES] {len(movies)} entries:\n")

    for entry, title, year, video_path, quality in movies:
        folder_name = f"{title} ({year})"
        ext         = os.path.splitext(video_path)[1]
        link_name   = f"{folder_name} - {quality}{ext}" if quality else f"{folder_name}{ext}"
        link_dir    = os.path.join(MOVIES_LINKED, folder_name)
        link_path   = os.path.join(link_dir, link_name)

        print(f"  {folder_name}{f'  [{quality}]' if quality else ''}")
        print(f"    source: {os.path.relpath(video_path, MOVIES_SOURCE)}")

        ensure_dir(link_dir, dry_run)
        _symlink(link_path, video_path, dry_run)

    if flagged:
        print(f"\n[FLAGGED] {len(flagged)} entries need attention:\n")
        for entry, reason in flagged:
            print(f"  [WARN] {entry}")
            print(f"         reason: {reason}")

    print(f"\n[SKIPPED] {len(skipped)} miniseries folders (handled by make_tv_links.py):\n")
    for entry in skipped:
        print(f"  {entry}")

    if ambiguous:
        handle_ambiguous(ambiguous, dry_run)

    print("\n" + "=" * 60)
    if dry_run:
        print("DRY RUN complete - no files or folders created.")
        print("Run without --dry-run to apply.")
    else:
        print(f"\nDone. Point Jellyfin/Radarr at: {MOVIES_LINKED}")

    # Offer TMDB lookup for yearless entries
    no_year_flagged = [f for f in flagged if f[1] == "no year found - needs manual match"]
    if no_year_flagged:
        print(f"\n{len(no_year_flagged)} entries flagged (no year found).")
        if dry_run:
            print("  (run without --dry-run to use TMDB auto-lookup)")
        else:
            print("  [1] Auto-lookup via TMDB and create symlinks")
            print("  [2] Skip - match manually later")
            choice = input("\nChoice: ").strip()
            if choice == "1":
                resolve_flagged_via_tmdb(no_year_flagged, dry_run)
            else:
                print("Skipped. Flagged entries will need manual matching in Radarr/Jellyfin.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Build a Jellyfin/Radarr-ready symlink tree from a messy movies folder."
    )
    parser.add_argument("--dry-run", action="store_true",
                        help="Preview without creating anything")
    parser.add_argument("--clean", action="store_true",
                        help="Remove broken symlinks before rebuilding")
    args = parser.parse_args()
    main(args.dry_run, args.clean)