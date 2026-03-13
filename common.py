"""
common.py

Shared utilities for medialn scripts (make_movies_links.py, make_tv_links.py).

Contains regex patterns, filesystem helpers, and symlink logic used by both
scripts. Keeps everything in one place so the two scripts stay in sync.
"""

import os
import re

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

VIDEO_EXTS = {".mkv", ".mp4", ".avi", ".ts", ".m4v"}

# ---------------------------------------------------------------------------
# Regex patterns (shared across both scripts)
# ---------------------------------------------------------------------------

# Episode detection patterns
RE_SXXEXX      = re.compile(r'[Ss](\d{1,2})[Ee](\d{2})', re.IGNORECASE)
RE_XNOTATION   = re.compile(r'\d{1,2}x\d{2}', re.IGNORECASE)
RE_EPISODE     = re.compile(r'[Ee]pisode[. _](\d{1,3})', re.IGNORECASE)
RE_NOF         = re.compile(r'[\(]?(\d{1,2})of(\d{1,2})[\)]?', re.IGNORECASE)
RE_BARE_EPISODE = re.compile(r'(?<![Ss\d])E(\d{2,3})\b')

# Sample file detection - word-boundary match avoids false positives like "example.mkv"
RE_SAMPLE = re.compile(r'\bsample\b', re.IGNORECASE)

# Characters illegal on Windows/network mounts
RE_ILLEGAL_CHARS = re.compile(r'[/:\\?*"<>|]')

# Part.N detection - matches ".Part.1", ".Part1", " Part 2" etc.
# \d{1,2} intentionally excludes 4-digit years (e.g. "Bande a part 1964").
RE_PART = re.compile(r'[.\s\-_](?:Part|Pt)[.\s\-_]?(\d{1,2})\b', re.IGNORECASE)


# ---------------------------------------------------------------------------
# Utility functions
# ---------------------------------------------------------------------------

def is_video(filename):
    """Check if filename has a recognized video extension."""
    return os.path.splitext(filename)[1].lower() in VIDEO_EXTS


def is_sample(filename):
    """Check if filename looks like a sample file."""
    return bool(RE_SAMPLE.search(filename))


def is_episode(filename):
    """Check if filename matches any known episode naming pattern."""
    return (RE_SXXEXX.search(filename) or
            RE_XNOTATION.search(filename) or
            RE_EPISODE.search(filename) or
            RE_NOF.search(filename) or
            RE_BARE_EPISODE.search(filename))


def sanitize_filename(name):
    """Replace characters illegal on Windows/network mounts with '-'."""
    return RE_ILLEGAL_CHARS.sub('-', name)


def host_to_container(path, host_root, container_root):
    """Translate a host-side absolute path to its container-side equivalent."""
    return path.replace(host_root, container_root, 1)


def make_symlink(link_path, target_host_path, dry_run, host_root, container_root):
    """Create an absolute container-side symlink. Skips if link already exists."""
    if os.path.exists(link_path) or os.path.islink(link_path):
        print(f"    [SKIP] {os.path.basename(link_path)}")
        return
    container_target = host_to_container(target_host_path, host_root, container_root)
    print(f"    [LINK] {os.path.basename(link_path)}")
    print(f"        -> {container_target}")
    if not dry_run:
        os.symlink(container_target, link_path)


def ensure_dir(path, dry_run):
    """Create directory (and parents) if not in dry-run mode."""
    if not dry_run:
        os.makedirs(path, exist_ok=True)


def clean_broken_symlinks(directory):
    """Remove broken file and directory symlinks, then prune empty directories."""
    removed = 0

    # Broken file symlinks
    for dirpath, _, filenames in os.walk(directory):
        for fname in filenames:
            fpath = os.path.join(dirpath, fname)
            if os.path.islink(fpath) and not os.path.exists(fpath):
                print(f"  [REMOVE] {fpath}")
                os.remove(fpath)
                removed += 1

    # Broken directory symlinks (e.g. season folder symlinks in tv-linked)
    for dirpath, dirnames, _ in os.walk(directory):
        for dname in dirnames:
            dpath = os.path.join(dirpath, dname)
            if os.path.islink(dpath) and not os.path.exists(dpath):
                print(f"  [REMOVE] {dpath}")
                os.remove(dpath)
                removed += 1

    # Remove empty directories left behind
    for dirpath, _, _ in os.walk(directory, topdown=False):
        if dirpath == directory:
            continue
        if not os.listdir(dirpath):
            os.rmdir(dirpath)

    print(f"  Removed {removed} broken symlink(s).\n")


def find_videos_in_folder(folder_path, exclude_episodes=True, exclude_samples=True):
    """Scan a folder for video files, with optional episode/sample filtering."""
    videos = []
    with os.scandir(folder_path) as it:
        for f in it:
            if not f.is_file() or not is_video(f.name):
                continue
            if exclude_samples and is_sample(f.name):
                continue
            if exclude_episodes and is_episode(f.name):
                continue
            videos.append(f)
    return videos


def largest_video(videos):
    """Return the largest video file from a list of DirEntry objects."""
    return max(videos, key=lambda f: f.stat().st_size)