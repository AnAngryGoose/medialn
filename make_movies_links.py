#!/usr/bin/env python3
"""
make_movies_links.py  [v1.1]

Builds a Jellyfin/Radarr-compatible symlink tree from a disorganized movies
folder. Reads from /movies/, writes clean structure to /movies-linked/.
Original files are never modified — torrent clients keep seeding unaffected.

Output structure:
  movies-linked/
    Movie Name (Year)/
      Movie Name (Year).mkv            single version
      Movie Name (Year) - 1080P.mkv    \
      Movie Name (Year) - 2160P.mkv    /  multiple versions of same title+year
      Movie Name (Year) - 1080P.2.mkv     two copies at same resolution

  To change mount paths, update only these two constants at the top of the file:
    MEDIA_ROOT_HOST      = "/mnt/storage/data/media"
    MEDIA_ROOT_CONTAINER = "/data/media"

Features:
    - Automatically moves a disorganized movies folder setup into a clean jellyfin/arr stack
      readable library
        - This will only symlink files, in order to keep seeding from a torrent client
          working without issue.
            - This will always use **absolute** symlinks using container path.
                - /data/media/movies-linked/Movie (2020)/Movie (2020).mkv → /data/media/movies/original.mkv
                - Relative symlinks break when the container's working directory or mount structure differs from the host
                - No hardlinks:
                    -  On MergerFs, hardlinks can become a problem as there is a fundamental requirement of hardlinks
                       that the source and destination must be on the same underlying filesystem (same inode table).
                    - On mergerfs, movies/ and movies-linked/ could land on different pool branches, which causes
                      os.link() to fail with EXDEV (cross-device link).

    - Episode detection: Skips multi-episode folders (those belong in tv-linked via make_tv_links.py).
        - Avoids tagging TV shows.
        - Helpful for if you downloaded a miniseries from a movie tracker and its gets
          placed in /movies/
        - make_tv_links.py will handle these and move to correct /tv/ folder.

    - TMDB auto-lookup: Flags entries where a year cannot be extracted.
        - You can choose to either attempt to match (TMDB API Key needed)
          or just leave it alone as is, and fix manually later

    - Multi-version grouping:  Two or more verisons of the same file are handled as
        - Same title+year each get a quality suffix (1080P, 2160P, REMUX, etc.)
          instead of being discarded.
        - In the case of 2 versions with same resolution - it will tag them as
          " - 1080p.mkv" and "- 1080p.2.mkv"

    - Year/title matching protection:
        - Year must be preceded by a separator (dot, space, bracket, paren)
        - This will prevent a movie like "1917" being tagged as FROM 1917
            -  Man that'd be a different movie, much darker.

    - Various misc stuff:
        - Sample exclusion: word-boundary match (\bsample\b) avoids false positives
             like example.mkv.
        - os.scandir() used throughout to avoid redundant stat() calls.
        - Filename sanitization: characters illegal on Windows/network mounts
          (/ : \\ ? * " < > |) are replaced with - in all generated names.

Original files are NEVER modified. Torrent clients keeps seeding from original paths.

Usage:
  python3 make_movies_links.py --dry-run   # preview without creating anything
  python3 make_movies_links.py             # create symlinks
  python3 make_movies_links.py --clean     # remove broken symlinks + empty dirs, then rebuild

TMDB API docs: https://developer.themoviedb.org/reference/search-movie
"""

import os
import re
import json
import argparse
import urllib.request
import urllib.parse
from concurrent.futures import ThreadPoolExecutor, as_completed

##################################
### ----- CONFIG SECTION ----- ###
##################################
# Host path prefix and its equivalent inside the Docker container.
# Symlink targets are written using MEDIA_ROOT_CONTAINER so they resolve
# correctly inside Jellyfin regardless of host mount paths.
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"
MEDIA_ROOT_CONTAINER = "/data/media"

MOVIES_SOURCE = f"{MEDIA_ROOT_HOST}/movies"
MOVIES_LINKED = f"{MEDIA_ROOT_HOST}/movies-linked"

# Get a free API key at https://www.themoviedb.org/settings/api
TMDB_API_KEY = os.environ.get("TMDB_API_KEY", "")

VIDEO_EXTS = {".mkv", ".mp4", ".avi", ".ts", ".m4v"}

##################################
### --- END CONFIG SECTION --- ###
##################################

# Episode detection — folders matching these are miniseries, skip them
RE_SXXEXX  = re.compile(r'[Ss](\d{1,2})[Ee](\d{2})', re.IGNORECASE)
RE_XNOTATION = re.compile(r'\d{1,2}x\d{2}', re.IGNORECASE)   # e.g. 1x01, 01x09
RE_EPISODE = re.compile(r'[Ee]pisode[. _](\d{1,3})', re.IGNORECASE)
RE_NOF     = re.compile(r'[\(]?(\d{1,2})of(\d{1,2})[\)]?', re.IGNORECASE)
# Bare E01 format (no season prefix) — e.g. Band.Of.Brothers.E01, BBC.Life.E02
# Negative lookbehind prevents matching S01E01 (already caught by RE_SXXEXX)
RE_BARE_EPISODE = re.compile(r'(?<![Ss\d])E(\d{2,3})\b')

# Sample file detection — word-boundary match to avoid false positives like "example.mkv"
RE_SAMPLE = re.compile(r'\bsample\b', re.IGNORECASE)

# Year extraction — must be preceded by a separator to avoid matching titles like "1917"
RE_YEAR = re.compile(r'(?<=[.\s\[\(])((?:19|20)\d{2})(?=[.\s\]\)]|$)')

# Quality tag extraction for multi-version naming
RE_QUALITY = re.compile(
    r'(2160p|1080p|720p|576p|480p|REMUX|BluRay|BDRip|WEB-DL|WEBRip|HDTV|UHD)',
    re.IGNORECASE
)

# Strip everything from first quality/source tag onward (for title cleaning)
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

# Characters illegal on Windows filesystems / problematic on network mounts
RE_ILLEGAL_CHARS = re.compile(r'[/:\\?*"<>|]')


def is_video(filename):
    return os.path.splitext(filename)[1].lower() in VIDEO_EXTS


def is_sample(filename):
    return bool(RE_SAMPLE.search(filename))


def is_episode(filename):
    return (RE_SXXEXX.search(filename) or
            RE_XNOTATION.search(filename) or
            RE_EPISODE.search(filename) or
            RE_NOF.search(filename) or
            RE_BARE_EPISODE.search(filename))


def is_miniseries_folder(folder_path):
    """True if folder contains 2+ files that look like episodes."""
    episodes = 0
    with os.scandir(folder_path) as it:
        for entry in it:
            if entry.is_file() and is_video(entry.name) and is_episode(entry.name):
                episodes += 1
            if episodes >= 2:
                return True
    return False


def extract_year(name):
    m = RE_YEAR.search(name)
    return m.group(1) if m else None


def extract_quality(name):
    """Extract a short quality label from a filename for multi-version naming."""
    m = RE_QUALITY.search(name)
    return m.group(1).upper() if m else None


def clean_title(name):
    """Strip quality tags and trailing dots/spaces from a name."""
    name = os.path.splitext(name)[0]
    stripped = RE_STRIP.sub('', name)
    if ' ' not in stripped:
        stripped = stripped.replace('.', ' ')
    # Remove leading date prefixes like "2011.12.31." or "2011 12 31 "
    stripped = re.sub(r'^\d{4}[\s.]\d{2}[\s.]\d{2}[\s.]+', '', stripped)
    stripped = re.sub(r'[\[\(][^\[\(]*$', '', stripped)
    return stripped.strip(' .-_[]()').strip()


def sanitize_filename(name):
    """Replace characters illegal on Windows filesystems or problematic on network mounts."""
    return RE_ILLEGAL_CHARS.sub('-', name)


def host_to_container(path):
    """Translate a host-side absolute path to its container-side equivalent."""
    return path.replace(MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER, 1)


def make_symlink(link_path, target_host_path, dry_run):
    """
    Create a symlink at link_path pointing to the container-side absolute path.
    Using absolute container paths ensures symlinks resolve correctly inside Docker.
    """
    if os.path.exists(link_path) or os.path.islink(link_path):
        print(f"    [SKIP] {os.path.basename(link_path)}")
        return
    container_target = host_to_container(target_host_path)
    print(f"    [LINK] {os.path.basename(link_path)}")
    print(f"        -> {container_target}")
    if not dry_run:
        os.symlink(container_target, link_path)


def ensure_dir(path, dry_run):
    if not dry_run:
        os.makedirs(path, exist_ok=True)


def clean_broken_symlinks(directory):
    """Remove broken symlinks from directory tree. Used by --clean."""
    removed = 0
    for dirpath, _, filenames in os.walk(directory):
        for fname in filenames:
            fpath = os.path.join(dirpath, fname)
            if os.path.islink(fpath) and not os.path.exists(fpath):
                print(f"  [REMOVE] {fpath}")
                os.remove(fpath)
                removed += 1
    # Remove empty directories left behind
    for dirpath, dirnames, filenames in os.walk(directory, topdown=False):
        if dirpath == directory:
            continue
        if not os.listdir(dirpath):
            os.rmdir(dirpath)
    print(f"  Removed {removed} broken symlink(s).\n")


def tmdb_search(title):
    """
    Search TMDB for a movie title. Returns (canonical_title, year) or None.
    Refuses to search if the title is too short to produce a meaningful result.
    API reference: https://developer.themoviedb.org/reference/search-movie
    """
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


def scan_movies():
    """
    Scan MOVIES_SOURCE.

    Multiple versions of the same movie (same title + year) are kept — each gets
    a quality suffix appended to the link filename so they coexist in one folder.

    Returns:
      movies   — [(entry, title, year, video_path, quality), ...]
      flagged  — [(entry, reason), ...]
      skipped  — [entry, ...]  miniseries folders
    """
    movies  = []
    flagged = []
    skipped = []
    # link_key -> list of (entry, video_path, quality) for collision handling
    seen = {}

    with os.scandir(MOVIES_SOURCE) as it:
        entries = sorted(it, key=lambda e: e.name)

    for entry in entries:
        name = entry.name

        # --- bare video file at root ---
        if entry.is_file():
            if not is_video(name):
                continue
            if is_episode(name):
                skipped.append(name)
                continue
            if is_sample(name):
                continue
            year       = extract_year(name)
            title      = clean_title(name)
            quality    = extract_quality(name)
            video_path = entry.path

        # --- folder ---
        elif entry.is_dir():
            if is_miniseries_folder(entry.path):
                skipped.append(name)
                continue

            videos = []
            with os.scandir(entry.path) as it2:
                for f in it2:
                    if f.is_file() and is_video(f.name) and not is_episode(f.name) and not is_sample(f.name):
                        videos.append(f)

            if not videos:
                flagged.append((name, "no video file found in folder"))
                continue

            # Pick the largest file as primary
            primary = max(videos, key=lambda f: f.stat().st_size)
            video_path = primary.path
            year       = extract_year(name) or extract_year(primary.name)
            title      = clean_title(name)
            quality    = extract_quality(name) or extract_quality(primary.name)

        else:
            continue

        # --- shared validation ---
        if not title:
            flagged.append((name, "could not parse title"))
            continue
        if not year:
            flagged.append((name, "no year found — needs manual match"))
            continue

        link_key = f"{title} ({year})"
        seen.setdefault(link_key, []).append((name, video_path, quality))

    # Resolve seen into final movies list — multi-version gets quality suffix.
    # If two versions share the same quality tag, append .2, .3, etc. to disambiguate.
    for link_key, versions in seen.items():
        m = re.match(r'^(.+) \((\d{4})\)$', link_key)
        title = m.group(1)
        year  = m.group(2)

        if len(versions) == 1:
            entry_name, video_path, quality = versions[0]
            movies.append((entry_name, title, year, video_path, None))
        else:
            # Resolve quality for every version first
            resolved = []
            for entry_name, video_path, quality in versions:
                if not quality:
                    quality = extract_quality(os.path.basename(video_path)) or "unknown"
                resolved.append((entry_name, video_path, quality))

            # Count how many times each quality tag appears
            quality_counts = {}
            for _, _, quality in resolved:
                quality_counts[quality] = quality_counts.get(quality, 0) + 1

            # Assign suffixes — unique qualities get no counter, duplicates get .2, .3, ...
            quality_seen = {}
            for entry_name, video_path, quality in resolved:
                if quality_counts[quality] == 1:
                    label = quality
                else:
                    quality_seen[quality] = quality_seen.get(quality, 0) + 1
                    count = quality_seen[quality]
                    label = quality if count == 1 else f"{quality}.{count}"
                movies.append((entry_name, title, year, video_path, label))

    movies.sort(key=lambda x: (x[1].lower(), x[2]))
    return movies, flagged, skipped


def resolve_flagged_via_tmdb(flagged, dry_run):
    """
    For each flagged 'no year' entry: search TMDB concurrently, create symlinks.
    Skips entries flagged for other reasons.
    """
    print(f"\n[TMDB LOOKUP] {'(dry run) ' if dry_run else ''}Resolving flagged entries...\n")

    no_year = [(entry, reason) for entry, reason in flagged
               if reason == "no year found — needs manual match"]

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
        results = []
        for future in as_completed(futures):
            results.append(future.result())

    # Sort for consistent output
    results.sort(key=lambda r: r[0])

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
            videos = []
            with os.scandir(entry_path) as it:
                for f in it:
                    if f.is_file() and is_video(f.name) and not is_episode(f.name) and not is_sample(f.name):
                        videos.append(f)
            if not videos:
                print(f"  [FAIL]  {entry}")
                print(f"          TMDB found: {found_title} ({year}) but no video file in folder")
                continue
            video_path = max(videos, key=lambda f: f.stat().st_size).path

        folder_name = f"{found_title} ({year})" if year else found_title
        ext         = os.path.splitext(video_path)[1]
        link_name   = f"{folder_name}{ext}"
        link_dir    = os.path.join(MOVIES_LINKED, folder_name)
        link_path   = os.path.join(link_dir, link_name)

        print(f"  [FIND]  {clean_title(entry)!r}  ->  {found_title} ({year})")
        ensure_dir(link_dir, dry_run)
        make_symlink(link_path, video_path, dry_run)


def main(dry_run, clean):
    if clean:
        if os.path.isdir(MOVIES_LINKED):
            print(f"[CLEAN] Removing broken symlinks from {MOVIES_LINKED}...\n")
            clean_broken_symlinks(MOVIES_LINKED)
        else:
            print(f"[CLEAN] {MOVIES_LINKED} does not exist, nothing to clean.\n")

    ensure_dir(MOVIES_LINKED, dry_run)

    movies, flagged, skipped = scan_movies()

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
        make_symlink(link_path, video_path, dry_run)

    if flagged:
        print(f"\n[FLAGGED] {len(flagged)} entries need attention:\n")
        for entry, reason in flagged:
            print(f"  [WARN] {entry}")
            print(f"         reason: {reason}")

    print(f"\n[SKIPPED] {len(skipped)} miniseries folders (handled by make_tv_links.py):\n")
    for entry in skipped:
        print(f"  {entry}")

    print("\n" + "=" * 60)
    if dry_run:
        print("DRY RUN complete — no files or folders created.")
        print("Run without --dry-run to apply.")
    else:
        print(f"\nDone. Point Jellyfin/Radarr at: {MOVIES_LINKED}")

    no_year_flagged = [f for f in flagged if f[1] == "no year found — needs manual match"]
    if no_year_flagged:
        print(f"\n{len(no_year_flagged)} entries flagged (no year found).")
        if dry_run:
            print("  (run without --dry-run to use TMDB auto-lookup)")
        else:
            print("  [1] Auto-lookup via TMDB and create symlinks")
            print("  [2] Skip — match manually later")
            choice = input("\nChoice: ").strip()
            if choice == "1":
                resolve_flagged_via_tmdb(no_year_flagged, dry_run)
            else:
                print("Skipped. Flagged entries will need manual matching in Radarr/Jellyfin.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true",
                        help="Preview without creating anything")
    parser.add_argument("--clean", action="store_true",
                        help="Remove broken symlinks before rebuilding")
    args = parser.parse_args()
    main(args.dry_run, args.clean)