package tv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AnAngryGoose/medialnk/internal/common"
)

// ---------------------------------------------------------------------------
// TV-specific regex patterns
// ---------------------------------------------------------------------------

// seasonRE parses "ShowName.S01.720p..." or "ShowName S01 text..." folder names.
var seasonRE = regexp.MustCompile(`^(.+?)[. ]([Ss])(\d{2})([Ee]\d+.*|[. ].*)$`)

// seasonTextRE parses "Show Name Season N" / "Show.Name.Season.4" folder names.
var seasonTextRE = regexp.MustCompile(`(?i)^(.+?)[. _\-]+Season[. _](\d{1,2})(?:[. _\-]|$)`)

// reSeasonRange detects a multi-season range indicator like S01-S31 in a folder name.
var reSeasonRange = regexp.MustCompile(`[Ss]\d{1,2}-[Ss]\d{1,2}`)

// reSeasonOnly matches a bare "Season N" or "Season 01" folder name.
var reSeasonOnly = regexp.MustCompile(`(?i)^[Ss]eason[. _](\d{1,2})(?:[. _]|$)`)

// reContainerStrip removes "complete/pack/series/full" and range indicators from
// a container folder name, leaving just the show title.
var reContainerStrip = regexp.MustCompile(`(?i)[. _](?:complete|collection|pack|series|full)[. _].*$|[. _][Ss]\d{1,2}-[Ss]\d{1,2}.*$`)

// reStrip removes scene metadata from a folder name, leaving just the show title.
var reStrip = regexp.MustCompile(`(?i)[. ]([Ss]\d{2}([Ee]\d{2})?|` +
	`\d{4}|` +
	`2160p|1080p|720p|576p|480p|` +
	`REPACK\d*|BluRay|BDRip|Blu-ray|` +
	`WEB-DL|WEBRip|AMZN|NF|HMAX|PMTP|HDTV|DVDRip|DVDrip|UHD|NTSC|PAL|` +
	`HDR\d*|DV|DDP[\d.]*|DD[\+\d.]*|DTS|FLAC[\d.]*|AAC[\d.]*|AC3|Opus|` +
	`x264|x265|H\.264|H\.265|h264|h265|AVC|HEVC|` +
	`REMASTERED|EXTENDED|UNRATED|LIMITED|DOCU|CRITERION|` +
	`[A-Z0-9]+-[A-Z][A-Za-z0-9]+).*$`)

// reTrailingYear strips a trailing 4-digit year from a show name.
var reTrailingYear = regexp.MustCompile(`\s+\d{4}$`)

// ---------------------------------------------------------------------------
// Normalization functions — MUST NOT be merged
// ---------------------------------------------------------------------------

// reApostrophe strips apostrophe-style characters for light normalization.
var reApostrophe = regexp.MustCompile(`['\x{2019}` + "`]" + ``)

// rePossessive strips possessive and article patterns for aggressive normalization.
var rePossessive = regexp.MustCompile(`['\x{2019}` + "`]" + `s?\b`)
var reArticle = regexp.MustCompile(`^(the|a|an)\s+`)
var reStudio = regexp.MustCompile(`^(marvels?|dcs?|disneys?|nbc|bbc)\s+`)
var reNonAlnum = regexp.MustCompile(`[^a-z0-9\s]`)
var reMultiSpace = regexp.MustCompile(`\s+`)

// NormKey is light normalization for Pass 1 folder grouping.
// Lowercase, strip apostrophes, normalize whitespace.
// Groups "Schitts Creek S01" and "Schitt's Creek S02" under the same show.
func normKey(name string) string {
	name = strings.ToLower(name)
	name = reApostrophe.ReplaceAllString(name, "")
	return strings.TrimSpace(reMultiSpace.ReplaceAllString(name, " "))
}

// normMatch is aggressive normalization for cross-source matching.
// Used when comparing bare episode file show names against Pass 1 results.
func normMatch(name string) string {
	name = strings.ToLower(name)
	name = rePossessive.ReplaceAllString(name, "")
	name = reArticle.ReplaceAllString(name, "")
	name = reStudio.ReplaceAllString(name, "")
	name = reTrailingYear.ReplaceAllString(name, "")
	name = reNonAlnum.ReplaceAllString(name, "")
	return strings.TrimSpace(reMultiSpace.ReplaceAllString(name, " "))
}

// ---------------------------------------------------------------------------
// Folder name parsing
// ---------------------------------------------------------------------------

// showSeason parses a season folder name into (show, seasonNum).
// Returns ("", 0, false) if the folder doesn't match either format.
func showSeason(folder string, overrides map[string]string) (string, int, bool) {
	if m := seasonRE.FindStringSubmatch(folder); m != nil {
		raw := m[1]
		snum, _ := strconv.Atoi(m[3])
		show := raw
		if !strings.Contains(show, " ") {
			show = strings.ReplaceAll(show, ".", " ")
		}
		show = strings.TrimSpace(show)
		show = reTrailingYear.ReplaceAllString(show, "")
		if canonical, ok := overrides[show]; ok {
			show = canonical
		}
		return common.Sanitize(show), snum, true
	}
	if m := seasonTextRE.FindStringSubmatch(folder); m != nil {
		raw := m[1]
		snum, _ := strconv.Atoi(m[2])
		show := raw
		if !strings.Contains(show, " ") {
			show = strings.ReplaceAll(show, ".", " ")
		}
		show = strings.TrimSpace(show)
		show = reTrailingYear.ReplaceAllString(show, "")
		show = strings.Trim(show, " -")
		if canonical, ok := overrides[show]; ok {
			show = canonical
		}
		return common.Sanitize(show), snum, true
	}
	return "", 0, false
}

// cleanShow strips scene metadata from a folder name, returning just the show title.
func cleanShow(folder string) string {
	name := folder
	if !strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, ".", " ")
	}
	name = reStrip.ReplaceAllString(name, "")
	return common.Sanitize(strings.Trim(name, " .-_"))
}

// isBarEpFolder returns true if folder contains ≥2 individually-named episode
// files or directories (using episode notation in the name).
func isBareEpFolder(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && common.IsVideo(name) && common.IsEpisodeFile(name, true) {
			count++
		} else if e.IsDir() && common.ReBareEpisode.MatchString(name) {
			count++
		}
		if count >= 2 {
			return true
		}
	}
	return false
}

// BareEpisode holds the parsed fields from a bare episode filename.
type BareEpisode struct {
	Show     string
	Season   int
	Episode  int
	Quality  string
	SecondEp int // -1 if single episode
}

// ParseBareEpisode parses show/season/episode/quality from a bare episode filename.
// Returns nil if no episode pattern is found.
// Handles SxxExx (+ multi-ep), NxNN, Episode.N formats.
func ParseBareEpisode(filename string) *BareEpisode {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	if m := common.ReSxxExx.FindStringSubmatchIndex(name); m != nil {
		sn, _ := strconv.Atoi(name[m[2]:m[3]])
		en, _ := strconv.Atoi(name[m[4]:m[5]])
		raw := strings.Trim(name[:m[0]], " .-_")
		show := raw
		if !strings.Contains(show, " ") {
			show = strings.ReplaceAll(show, ".", " ")
		}
		show = strings.TrimSpace(reTrailingYear.ReplaceAllString(show, ""))
		second := -1
		if mc := common.ReMultiEp.FindStringSubmatch(name[m[1]:]); mc != nil {
			second, _ = strconv.Atoi(mc[1])
		}
		return &BareEpisode{show, sn, en, common.ExtractQuality(name), second}
	}

	if m := common.ReXNotation.FindStringSubmatchIndex(name); m != nil {
		sn, _ := strconv.Atoi(name[m[2]:m[3]])
		en, _ := strconv.Atoi(name[m[4]:m[5]])
		raw := strings.Trim(name[:m[0]], " .-_")
		show := raw
		if !strings.Contains(show, " ") {
			show = strings.ReplaceAll(show, ".", " ")
		}
		show = strings.TrimSpace(reTrailingYear.ReplaceAllString(show, ""))
		return &BareEpisode{show, sn, en, common.ExtractQuality(name), -1}
	}

	if m := common.ReEpisode.FindStringSubmatchIndex(name); m != nil {
		en, _ := strconv.Atoi(name[m[2]:m[3]])
		raw := strings.Trim(name[:m[0]], " .-_")
		show := raw
		if !strings.Contains(show, " ") {
			show = strings.ReplaceAll(show, ".", " ")
		}
		show = strings.TrimSpace(reTrailingYear.ReplaceAllString(show, ""))
		return &BareEpisode{show, 1, en, common.ExtractQuality(name), -1}
	}

	return nil
}

// findMatch finds the canonical show name in grouped or a pre-scanned list of
// tv_linked directory entries that matches the normalized show name.
// Returns "" if no match.
func findMatch(show string, grouped map[string][]seasonEntry, linkedEntries []os.DirEntry) string {
	key := normMatch(show)
	for g := range grouped {
		if normMatch(g) == key {
			return g
		}
	}
	for _, e := range linkedEntries {
		if e.IsDir() && normMatch(e.Name()) == key {
			return e.Name()
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Episode state helpers
// ---------------------------------------------------------------------------

// epInFolder checks if episode (season, ep) exists in the source folder.
// Returns (found, quality).
func epInFolder(folder string, episode, season int) (bool, string) {
	needle := fmt.Sprintf("S%02dE%02d", season, episode)
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false, ""
	}
	for _, e := range entries {
		if e.Type().IsRegular() && common.IsVideo(e.Name()) &&
			strings.Contains(strings.ToUpper(e.Name()), needle) {
			return true, common.ExtractQuality(e.Name())
		}
	}
	return false, ""
}

// epSymlinkExists checks if a symlink for episode (season, ep) exists in seasonDir.
func epSymlinkExists(seasonDir string, episode, season int) bool {
	needle := fmt.Sprintf("S%02dE%02d", season, episode)
	entries, err := os.ReadDir(seasonDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if common.IsSymlink(filepath.Join(seasonDir, e.Name())) &&
			strings.Contains(strings.ToUpper(e.Name()), needle) {
			return true
		}
	}
	return false
}

// seasonEntry holds a (seasonNum, folderRelPath) pair in the grouped map.
type seasonEntry struct {
	season int
	folder string // relative path within tv_source
}

// normCompare is used for warning detection: strips TVDB IDs, years, then cleans.
func normCompare(name string) string {
	name = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(name, "")
	name = regexp.MustCompile(`\(\d{4}\)`).ReplaceAllString(name, "")
	return strings.ToLower(strings.TrimSpace(cleanShow(name)))
}
