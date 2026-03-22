package common

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Shared regex patterns
// ---------------------------------------------------------------------------

var (
	ReSxxExx      = regexp.MustCompile(`(?i)[Ss](\d{1,2})[Ee](\d{2})`)
	ReXNotation   = regexp.MustCompile(`(?i)(\d{1,2})x(\d{2})`)
	ReEpisode     = regexp.MustCompile(`(?i)[Ee]pisode[. _](\d{1,3})`)
	ReNof         = regexp.MustCompile(`(?i)[\(]?(\d{1,2})of(\d{1,2})[\)]?`)
	// ReBareEpisode matches bare E-notation episodes (e.g. "pe.E01.mkv").
	// Python used a negative lookbehind (?<![Ss\d]) which RE2 doesn't support.
	// Equivalent: E must be preceded by start-of-string or a non-[Ss\d] char.
	// The outer non-capturing group is not a captured group, so group 1 = digits.
	ReBareEpisode = regexp.MustCompile(`(?:^|[^Ss\d])E(\d{2,3})\b`)
	ReMultiEp     = regexp.MustCompile(`(?i)[-.]?[Ee](\d{2})`)
	RePart        = regexp.MustCompile(`(?i)[.\s\-_](?:Part|Pt)[.\s\-_]?(\d{1,2})\b`)
	ReSample      = regexp.MustCompile(`(?i)\bsample\b`)
	ReIllegal     = regexp.MustCompile(`[/:\\?*"<>|]`)
	ReQuality     = regexp.MustCompile(`(?i)(2160p|1080p|720p|576p|480p|REMUX|BluRay|BDRip|WEB-DL|WEBRip|HDTV|UHD)`)
)

// ---------------------------------------------------------------------------
// Detection helpers
// ---------------------------------------------------------------------------

// IsSample reports whether a filename looks like a sample file.
func IsSample(filename string) bool {
	return ReSample.MatchString(filename)
}

// EpisodeResult holds season and episode numbers parsed from a filename.
type EpisodeResult struct {
	Season  int
	Episode int
}

// EpisodeInfo extracts (season, episode) from a filename using a regex cascade.
// Returns nil if no episode pattern is found.
// includePart controls whether Part.N patterns are considered.
func EpisodeInfo(filename string, includePart bool) *EpisodeResult {
	if m := ReSxxExx.FindStringSubmatch(filename); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return &EpisodeResult{Season: s, Episode: e}
	}
	if m := ReXNotation.FindStringSubmatch(filename); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return &EpisodeResult{Season: s, Episode: e}
	}
	if m := ReEpisode.FindStringSubmatch(filename); m != nil {
		e, _ := strconv.Atoi(m[1])
		return &EpisodeResult{Season: 1, Episode: e}
	}
	if m := ReBareEpisode.FindStringSubmatch(filename); m != nil {
		e, _ := strconv.Atoi(m[1])
		return &EpisodeResult{Season: 1, Episode: e}
	}
	if m := ReNof.FindStringSubmatch(filename); m != nil {
		e, _ := strconv.Atoi(m[1])
		return &EpisodeResult{Season: 1, Episode: e}
	}
	if includePart {
		if m := RePart.FindStringSubmatch(filename); m != nil {
			e, _ := strconv.Atoi(m[1])
			return &EpisodeResult{Season: 1, Episode: e}
		}
	}
	return nil
}

// ExtractQuality returns the first quality tag found in name (uppercased),
// or empty string if none.
func ExtractQuality(name string) string {
	if m := ReQuality.FindString(name); m != "" {
		return strings.ToUpper(m)
	}
	return ""
}

// Sanitize replaces filesystem-illegal characters with '-'.
func Sanitize(name string) string {
	return ReIllegal.ReplaceAllString(name, "-")
}

// CleanPassthroughName applies safe cosmetic cleanup to a folder name:
// dots are converted to spaces only when the name contains no spaces,
// then whitespace is normalized. No year or metadata stripping.
func CleanPassthroughName(folderName string) string {
	name := folderName
	if !strings.Contains(name, " ") {
		name = strings.ReplaceAll(name, ".", " ")
	}
	return strings.Join(strings.Fields(name), " ")
}

// ---------------------------------------------------------------------------
// Terminal detection
// ---------------------------------------------------------------------------

// IsTerminal reports whether stdin is connected to a terminal (character device).
// Returns false when input is piped or redirected.
func IsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ---------------------------------------------------------------------------
// Interactive prompt
// ---------------------------------------------------------------------------

var stdinReader = bufio.NewReader(os.Stdin)

// PromptChoice loops until the user enters one of the valid choices.
// Input is lowercased before comparison.
func PromptChoice(message string, valid []string) string {
	validSet := make(map[string]bool, len(valid))
	for _, v := range valid {
		validSet[v] = true
	}
	for {
		fmt.Print(message)
		line, _ := stdinReader.ReadString('\n')
		c := strings.ToLower(strings.TrimSpace(line))
		if validSet[c] {
			return c
		}
		fmt.Printf("    Enter one of: %s\n", strings.Join(valid, ", "))
	}
}
