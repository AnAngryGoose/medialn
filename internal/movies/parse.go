// Package movies implements the movie scanning, categorization, and
// symlink creation pipeline.
package movies

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AnAngryGoose/medialnk/internal/common"
)

// RE_YEAR extracts a 4-digit year from a normalized filename.
// Requires the year to be preceded by a dot, space, [, or ( and followed
// by a dot, space, ], ) or end of string.
var reYear = regexp.MustCompile(`(?:[.\s\[\(])((?:19|20)\d{2})(?:[.\s\]\)]|$)`)

// reStrip removes year, quality, codec, release group, and other scene
// noise tags from a title string, along with everything that follows.
var reStrip = regexp.MustCompile(`(?i)[. \(](?:` +
	`(?:19|20)\d{2}|` +
	`2160p|1080p|720p|576p|480p|` +
	`REPACK\d*|BluRay|BDRip|Blu-ray|` +
	`WEB-DL|WEBRip|AMZN|NF|HMAX|PMTP|HDTV|DVDRip|DVDrip|UHD|VHS|` +
	`HDR\d*|DV|DDP[\d.]*|DD[\+\d.]*|DTS|FLAC[\d.]*|AAC[\d.]*|AC3|Opus|` +
	`x264|x265|H\.264|H\.265|h264|h265|AVC|HEVC|` +
	`REMASTERED|EXTENDED|UNRATED|LIMITED|DOCU|CRITERION|PROPER|Uncut|` +
	`(?-i:[A-Z]{2,}-[A-Z][A-Za-z0-9]+))` +
	`.*$`)

// reDate strips leading date prefixes like "2023.04.15." from titles.
var reDate = regexp.MustCompile(`^\d{4}[\s.]\d{2}[\s.]\d{2}[\s.]+`)

// reTrailingBracket strips a trailing open bracket and everything after
// (e.g. "[REMUX]" at the end of a partial strip).
var reTrailingBracket = regexp.MustCompile(`[\[\(][^\[\(]*$`)

// normalize replaces underscores with dots so that older scene releases
// and VHS rips with underscore separators are handled correctly by the
// year and strip regexes, which expect dot/space prefixes.
func normalize(name string) string {
	return strings.ReplaceAll(name, "_", ".")
}

// year extracts the first 4-digit year from name after normalization.
// Returns empty string if none found.
func Year(name string) string {
	m := reYear.FindStringSubmatch(normalize(name))
	if m == nil {
		return ""
	}
	return m[1]
}

// title extracts a clean human-readable title from a scene-format name.
func Title(name string) string {
	name = strings.TrimSuffix(normalize(name), filepath.Ext(name))
	s := reStrip.ReplaceAllString(name, "")
	if !strings.Contains(s, " ") {
		s = strings.ReplaceAll(s, ".", " ")
	}
	s = reDate.ReplaceAllString(s, "")
	s = reTrailingBracket.ReplaceAllString(s, "")
	return strings.Trim(s, " .-_[]()")
}

// isMiniseries returns true if folder contains ≥2 video files with episode
// notation, indicating it's a TV miniseries rather than a movie.
func isMiniseries(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if common.IsVideo(e.Name()) && common.IsEpisodeFile(e.Name(), true) {
			count++
			if count >= 2 {
				return true
			}
		}
	}
	return false
}

// isAmbiguousParts reports whether folder contains ≥2 Part.N video files
// that are not episodes by other notation. Returns the sorted part filenames.
func isAmbiguousParts(folder string) (bool, []string) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false, nil
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !common.IsVideo(name) || common.IsSample(name) {
			continue
		}
		if common.IsEpisodeFile(name, false) {
			continue
		}
		if common.RePart.MatchString(name) {
			parts = append(parts, name)
		}
	}
	// sort is stable for strings
	for i := 0; i < len(parts)-1; i++ {
		for j := i + 1; j < len(parts); j++ {
			if parts[j] < parts[i] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return len(parts) >= 2, parts
}
