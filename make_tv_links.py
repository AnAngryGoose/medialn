#!/usr/bin/env python3
"""
make_tv_links.py  [v1.1]

Builds a Jellyfin/Sonarr-compatible symlink tree from disorganized TV and
miniseries folders. Reads from /tv/ and /movies/, writes clean structure
to /tv-linked/. Original files are never modified — torrent clients keep
seeding unaffected.

Output structure:
  tv-linked/
    Show Name/
      Season 01/  -> absolute container path to original season folder (/tv/)
      Season 02/
    Show Name (already structured)/  -> symlinked as-is (pass-through)
    Miniseries Title/
      Season 01/
        Miniseries.S01E01.mkv  -> absolute container path (/movies/...)



Sources:
  /tv/      Bare season folders (Show.Name.S01.720p...) are grouped by show
            and symlinked under Show Name/Season XX/. Folders already in
            correct Jellyfin structure are symlinked as-is (pass-through).
  /movies/  Folders containing 2+ episode files are treated as miniseries
            and symlinked into tv-linked/ — they are NOT moved on disk.

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

  - Show grouping: bare season folders (Show.S01.quality...) are parsed,
    grouped by show name, and placed under Show Name/Season XX/.

  - Pass-through: folders already in correct Jellyfin structure are symlinked
    as-is without renaming.
        - There is also a detection for something that is passed already and
          then another season/show is found as a match as well
          - This works for duplicate seasons, as well as name overlaps
            - Duplicate: Show S07 (matched) and Show S07 (passed through)
            - Name overlap: 'The Joy of Painting' and pass-through 'The.Joy.of.Painting.COMPLETE.S01-S31.DVDRip-Mixed'
          - You'll get a warning about this at end of run.

  - Miniseries detection: folders in /movies/ containing 2+ episode files
    (S01E01, 1x01, Episode.N, NofN) are routed here instead of movies-linked/.
        - Helpful if you download a miniseries from a movie tracker
          or if you happen to have tv shows and movies in a mixed folder.

  - Name overrides: hardcoded corrections for known naming inconsistencies
    (e.g. "The Office US" -> "The Office (US)").
        - Allows you to manually set how you'd want this handled.

  - Orphan overrides: bare "Season N" folders with no show context are mapped
    to their correct show via ORPHAN_OVERRIDES.
        - Allows for manual setting of an unknown folder with generic naming.

  - Episode format detection covers: S01E01, 1x01, Episode.N, NofN.

  - Various misc stuff:
        - Sample exclusion: word-boundary match (\bsample\b) avoids false positives
             like example.mkv.
        - os.scandir() used throughout to avoid redundant stat() calls.
        - Filename sanitization: characters illegal on Windows/network mounts
          (/ : \\ ? * " < > |) are replaced with - in all generated names.

Usage:
  python3 make_tv_links.py --dry-run   # preview without creating anything
  python3 make_tv_links.py             # create symlinks
  python3 make_tv_links.py --clean     # remove broken symlinks + empty dirs, then rebuild

TVDB/Sonarr naming reference: https://wiki.servarr.com/sonarr/faq#what-is-the-correct-folder-structure-for-sonarr
Hardlink limitation: https://trash-guides.info/File-and-Folder-Structure/How-to-set-up/Hardlinks/
"""


import os
import re
import argparse

##################################
### ----- CONFIG SECTION ----- ###
### Manual overrides are below ###
##################################

# Host path prefix and its equivalent inside the Docker container.
# Symlink targets are written using MEDIA_ROOT_CONTAINER so they resolve
# correctly inside Jellyfin regardless of host mount paths.
MEDIA_ROOT_HOST      = "/mnt/storage/data/media"
MEDIA_ROOT_CONTAINER = "/data/media"

TV_SOURCE     = f"{MEDIA_ROOT_HOST}/tv"
MOVIES_SOURCE = f"{MEDIA_ROOT_HOST}/movies"
TV_LINKED     = f"{MEDIA_ROOT_HOST}/tv-linked"

VIDEO_EXTS = {".mkv", ".mp4", ".avi", ".ts", ".m4v"}

######################################
### ----- END CONFIG SECTION ----- ###
######################################


SEASON_RE    = re.compile(r'^(.+?)[. ]([Ss])(\d{2})([Ee]\d+.*|[. ].*)$')
RE_SXXEXX    = re.compile(r'[Ss](\d{1,2})[Ee](\d{2})', re.IGNORECASE)
RE_XNOTATION = re.compile(r'\d{1,2}x\d{2}', re.IGNORECASE)  # e.g. 1x01, 01x09
RE_EPISODE   = re.compile(r'[Ee]pisode[. _](\d{1,3})', re.IGNORECASE)
RE_NOF       = re.compile(r'[\(]?(\d{1,2})of(\d{1,2})[\)]?', re.IGNORECASE)
RE_SAMPLE    = re.compile(r'\bsample\b', re.IGNORECASE)
# Bare E01 format (no season prefix) — e.g. Band.Of.Brothers.E01, BBC.Life.E02
RE_BARE_EPISODE = re.compile(r'(?<![Ss\d])E(\d{2,3})\b')

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

# Characters illegal on Windows filesystems / problematic on network mounts
RE_ILLEGAL_CHARS = re.compile(r'[/:\\?*"<>|]')

##############################
### --- NAME OVERRIDES --- ###
##############################

# NAME_OVERRIDES: fix show names that parse inconsistently across seasons.
# Add an entry when two season folders produce different show names that
# should be grouped together, or when the parsed name does not match TVDB.
# Run --dry-run first and check the [TV SOURCE] section to see parsed names.
#
# Format: "parsed name as it appears in folder" -> "name you want"
#
# Examples:
#   "The Office US": "The Office (US)",
#   "Scooby-Doo Where Are You": "Scooby Doo Where Are You",

#    # "3000" is stripped as a number token — override to preserve full title
#    "Mystery Science Theater":   "Mystery Science Theater 3000",
NAME_OVERRIDES = {

#   "The Office US":             "The Office (US)",

}

# ORPHAN_OVERRIDES: map bare "Season N" folders (no show name in the
# folder itself) to their correct show. Run --dry-run first to identify
# orphans — they appear in [TV PASS-THROUGH] without a parent show name.
#
# Format: "folder name on disk" -> ("Show Name", season_number)
ORPHAN_OVERRIDES = {
    # Little Bear — bare Season N folders with no show name in the folder itself
    #"Season 1": ("Little Bear", 1),

    # Folders using "Season.N" naming instead of "S04" — files inside are
    # standard S04E01 format so is_bare_episode_folder() won't catch them
    # "The Blue Planet Season 1": ("The Blue Planet", 1),

    # Planet Earth — folder has no S01 and files use abbreviated "pe.s01e01" prefix
    # so neither SEASON_RE nor is_bare_episode_folder() can detect it
    #"Planet.Earth.1080p.BluRay.x264-CULTHD": ("Planet Earth", 1),
}

#############################
### --- END OVERRIDES --- ###
#############################

def is_video(filename):
    return os.path.splitext(filename)[1].lower() in VIDEO_EXTS


def is_sample(filename):
    return bool(RE_SAMPLE.search(filename))


def is_episode(filename):
    return (RE_SXXEXX.search(filename) or
            RE_XNOTATION.search(filename) or
            RE_EPISODE.search(filename) or
            RE_NOF.search(filename))


def sanitize_filename(name):
    """Replace characters illegal on Windows filesystems or problematic on network mounts."""
    return RE_ILLEGAL_CHARS.sub('-', name)


def normalize_show_key(name):
    """
    Normalize a show name to a stable grouping key.
    Case-insensitive and apostrophe/punctuation-agnostic so that variants like
    'Ask The Storybots', 'Ask the StoryBots', "Blue's Clues", 'Blues Clues',
    "Chef's Table", 'Chefs Table' all resolve to the same group.
    """
    name = name.lower()
    name = re.sub(r"['\u2019`]", '', name)   # strip apostrophes (straight + curly)
    name = re.sub(r'\s+', ' ', name)          # collapse whitespace
    return name.strip()


def host_to_container(path):
    """Translate a host-side absolute path to its container-side equivalent."""
    return path.replace(MEDIA_ROOT_HOST, MEDIA_ROOT_CONTAINER, 1)


def episode_info(filename):
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
    return None


def is_bare_episode_folder(folder_path):
    """
    True if a folder contains 2+ bare E\\d+ episode entries — either:
    - Direct video files named with bare E\\d+ (e.g. Band.Of.Brothers.E01....mkv)
    - Subdirectories named with bare E\\d+ (e.g. BBC.Life.E01.../BBC.Frozen.Planet.E02...)
      where each subdir holds one episode file
    """
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


def extract_show_and_season(folder_name):
    m = SEASON_RE.match(folder_name)
    if not m:
        return None, None
    raw_name   = m.group(1)
    season_num = int(m.group(3))
    show_name  = raw_name.replace('.', ' ').strip() if ' ' not in raw_name else raw_name.strip()
    show_name  = re.sub(r'\s+\d{4}$', '', show_name)   # strip trailing year e.g. "Bluey 2018"
    show_name  = sanitize_filename(NAME_OVERRIDES.get(show_name, show_name))
    return show_name, season_num


def clean_show_name(folder_name):
    name = folder_name
    if ' ' not in name:
        name = name.replace('.', ' ')
    name = RE_STRIP.sub('', name)
    return sanitize_filename(name.strip(' .-_'))


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
    """Remove broken symlinks and empty directories. Used by --clean."""
    removed = 0
    for dirpath, _, filenames in os.walk(directory):
        for fname in filenames:
            fpath = os.path.join(dirpath, fname)
            if os.path.islink(fpath) and not os.path.exists(fpath):
                print(f"  [REMOVE] {fpath}")
                os.remove(fpath)
                removed += 1
    # Also check for broken directory symlinks (season folders are dir symlinks)
    for dirpath, dirnames, _ in os.walk(directory):
        for dname in dirnames:
            dpath = os.path.join(dirpath, dname)
            if os.path.islink(dpath) and not os.path.exists(dpath):
                print(f"  [REMOVE] {dpath}")
                os.remove(dpath)
                removed += 1
    # Remove empty directories left behind
    for dirpath, dirnames, filenames in os.walk(directory, topdown=False):
        if dirpath == directory:
            continue
        if not os.listdir(dirpath):
            os.rmdir(dirpath)
    print(f"  Removed {removed} broken symlink(s).\n")


def scan_tv_source():
    grouped     = {}   # canonical_name -> [(season_num, folder), ...]
    name_map    = {}   # normalized_key -> canonical display name
    passthrough = []

    def canonical(show_name):
        """Return the canonical display name for show_name, registering on first use."""
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
            continue

        show_name, season_num = extract_show_and_season(name)
        if show_name:
            grouped.setdefault(canonical(show_name), []).append((season_num, name))
        elif is_bare_episode_folder(entry.path):
            # Folder has no S01 in name but contains bare E01-format episode files.
            # Derive show name from folder name and treat as Season 1.
            show_name = clean_show_name(name)
            show_name = sanitize_filename(NAME_OVERRIDES.get(show_name, show_name))
            grouped.setdefault(canonical(show_name), []).append((1, name))
        else:
            passthrough.append(name)

    return grouped, passthrough


def scan_movies_for_miniseries():
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
            if not f.is_file():
                continue
            if not is_video(f.name) or is_sample(f.name):
                continue
            info = episode_info(f.name)
            if info:
                episodes.append((info[0], info[1], f.name))

        if len(episodes) < 2:
            continue

        show_name = clean_show_name(entry.name)
        results[show_name] = (entry.name, sorted(episodes))

    return results


def normalize_for_compare(name):
    """
    Strip year, TVDB tags, quality tags, and punctuation from a folder name
    so grouped and pass-through entries for the same show can be compared.
    e.g. "Curious George (2006) {tvdb-79429}" -> "curious george"
         "The.Joy.of.Painting.COMPLETE.S01-S31.DVDRip-Mixed" -> "the joy of painting"
    """
    name = re.sub(r'\{[^}]+\}', '', name)       # strip {tvdb-...} tags
    name = re.sub(r'\(\d{4}\)', '', name)        # strip (year)
    name = clean_show_name(name)                 # strip quality/codec/release tokens
    return name.lower().strip()


def collect_warnings(tv_grouped, tv_passthrough):
    """
    Detect two classes of issue and return a list of warning strings:

    1. Duplicate season numbers within a grouped show — two source folders
       on disk both parsed to the same show + season. The second symlink will
       be silently skipped; the duplicate source folder should be investigated.

    2. Name overlap between grouped shows and pass-through folders — a
       pass-through folder normalizes to the same name as a grouped show,
       meaning tv-linked/ will contain two separate entries for the same show
       (one built from bare season folders, one pass-through). Jellyfin may
       treat these as separate shows.
    """
    warnings = []

    # 1. Duplicate seasons
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

    # 2. Grouped / pass-through name overlap
    grouped_normalized = {normalize_for_compare(name): name for name in tv_grouped}
    for pt_entry in tv_passthrough:
        norm = normalize_for_compare(pt_entry)
        if norm in grouped_normalized:
            warnings.append(
                f"Name overlap: grouped show '{grouped_normalized[norm]}' and "
                f"pass-through '{pt_entry}' resolve to the same name — "
                f"tv-linked/ will have two separate entries"
            )

    return warnings


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

    # /tv/ grouped shows
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
            make_symlink(link_path, target, dry_run)

    # /tv/ pass-through (already structured)
    print(f"\n[TV PASS-THROUGH] {len(tv_passthrough)} folders (symlinked as-is):\n")
    for entry in sorted(tv_passthrough):
        print(f"  {entry}")
        link_path = os.path.join(TV_LINKED, entry)
        target    = os.path.join(TV_SOURCE, entry)
        make_symlink(link_path, target, dry_run)

    # /movies/ miniseries
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
            make_symlink(link_path, orig_path, dry_run)

        print()

    # --- warnings ---
    warnings = collect_warnings(tv_grouped, tv_passthrough)
    if warnings:
        print(f"\n[WARNINGS] {len(warnings)} issue(s) need attention:\n")
        for w in warnings:
            print(f"  [WARN] {w}")
        print()

    print("=" * 60)
    if dry_run:
        print("DRY RUN complete — no files or folders created.")
        print("Run without --dry-run to apply.")
    else:
        print(f"\nDone. Point Jellyfin/Sonarr at: {TV_LINKED}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true",
                        help="Preview without creating anything")
    parser.add_argument("--clean", action="store_true",
                        help="Remove broken symlinks + empty dirs before rebuilding")
    args = parser.parse_args()
    main(args.dry_run, args.clean)