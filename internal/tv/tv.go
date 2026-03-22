package tv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/movies"
	"github.com/AnAngryGoose/medialnk/internal/resolver"
	"github.com/AnAngryGoose/medialnk/internal/state"
)

// Log is the subset of logger methods the TV pipeline uses.
type Log interface {
	Normal(format string, args ...any)
	Verbose(format string, args ...any)
	Debug(format string, args ...any)
}

// ---------------------------------------------------------------------------
// Season container scanning
// ---------------------------------------------------------------------------

// scanSeasonContainer handles a folder with a multi-season range indicator
// (e.g. "Show.S01-S31.1080p"). Extracts the show title, recurses one level to
// find season subfolders, returns them as (show, seasonNum, relPath) tuples.
func scanSeasonContainer(folderName, folderPath string, overrides map[string]string) []struct {
	show   string
	season int
	rel    string
} {
	if !reSeasonRange.MatchString(folderName) {
		return nil
	}
	raw := reContainerStrip.ReplaceAllString(folderName, "")
	if raw == "" {
		raw = folderName
	}
	if !strings.Contains(raw, " ") {
		raw = strings.ReplaceAll(raw, ".", " ")
	}
	showName := strings.Trim(raw, " .-_")
	showName = strings.TrimSpace(reTrailingYear.ReplaceAllString(showName, ""))
	if showName == "" {
		return nil
	}
	if canonical, ok := overrides[showName]; ok {
		showName = canonical
	}
	showName = common.Sanitize(showName)

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil
	}
	var results []struct {
		show   string
		season int
		rel    string
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, snum, ok := showSeason(e.Name(), map[string]string{})
		if !ok {
			if m := reSeasonOnly.FindStringSubmatch(e.Name()); m != nil {
				snum, _ = strconv.Atoi(m[1])
				ok = true
			}
		}
		if ok {
			rel := filepath.Join(folderName, e.Name())
			results = append(results, struct {
				show   string
				season int
				rel    string
			}{showName, snum, rel})
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// Pass 1 scanner
// ---------------------------------------------------------------------------

// passthroughEntry pairs an unprocessed folder name with its source directory.
type passthroughEntry struct {
	name      string
	sourceDir string
}

func scanTV(cfg *config.Config) (map[string][]seasonEntry, []passthroughEntry) {
	grouped := map[string][]seasonEntry{}
	nameMap := map[string]string{} // normKey -> canonical show name
	var pt []passthroughEntry

	canon := func(show string) string {
		k := normKey(show)
		if _, ok := nameMap[k]; !ok {
			nameMap[k] = show
		}
		return nameMap[k]
	}

	for _, srcDir := range cfg.TVSources {
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			nm := e.Name()

			// Orphan overrides take priority.
			if ov, ok := cfg.TVOrphanOverrides[nm]; ok {
				show := canon(ov.Show)
				grouped[show] = append(grouped[show], seasonEntry{ov.Season, nm, srcDir})
				continue
			}

			if !e.IsDir() {
				continue
			}

			folderPath := filepath.Join(srcDir, nm)

			if show, snum, ok := showSeason(nm, cfg.TVNameOverrides); ok {
				show = canon(show)
				grouped[show] = append(grouped[show], seasonEntry{snum, nm, srcDir})
				continue
			}

			if isBareEpFolder(folderPath) {
				show := cleanShow(nm)
				if canonical, ok := cfg.TVNameOverrides[show]; ok {
					show = canonical
				}
				show = common.Sanitize(show)
				show = canon(show)
				grouped[show] = append(grouped[show], seasonEntry{1, nm, srcDir})
				continue
			}

			if seasons := scanSeasonContainer(nm, folderPath, cfg.TVNameOverrides); len(seasons) > 0 {
				for _, s := range seasons {
					show := canon(s.show)
					grouped[show] = append(grouped[show], seasonEntry{s.season, s.rel, srcDir})
				}
				continue
			}

			pt = append(pt, passthroughEntry{nm, srcDir})
		}
	}
	return grouped, pt
}

// ---------------------------------------------------------------------------
// Miniseries scanner (reads movies_source)
// ---------------------------------------------------------------------------

type miniEntry struct {
	folder    string // folder name in source directory
	sourceDir string // absolute path of the source directory
	episodes  []struct {
		season  int
		episode int
		file    string
	}
}

func scanMiniseries(cfg *config.Config) map[string]miniEntry {
	results := map[string]miniEntry{}
	for _, srcDir := range cfg.MoviesSources {
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			folderPath := filepath.Join(srcDir, e.Name())
			subEntries, err := os.ReadDir(folderPath)
			if err != nil {
				continue
			}
			var eps []struct {
				season, episode int
				file            string
			}
			for _, f := range subEntries {
				if f.IsDir() || !common.IsVideo(f.Name()) || common.IsSample(f.Name()) {
					continue
				}
				info := common.EpisodeInfo(f.Name(), true)
				if info != nil {
					eps = append(eps, struct {
						season, episode int
						file            string
					}{info.Season, info.Episode, f.Name()})
				}
			}
			if len(eps) >= 2 {
				sort.Slice(eps, func(i, j int) bool {
					if eps[i].season != eps[j].season {
						return eps[i].season < eps[j].season
					}
					return eps[i].episode < eps[j].episode
				})
				show := cleanShow(e.Name())
				results[show] = miniEntry{e.Name(), srcDir, eps}
			}
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// Duplicate season resolution
// ---------------------------------------------------------------------------

// qualityRank returns a numeric rank for a quality tag (higher = better).
func qualityRank(q string) int {
	ranks := map[string]int{
		"2160P": 5, "UHD": 5, "REMUX": 4,
		"1080P": 3, "BLURAY": 3, "BDRIP": 3,
		"720P": 2, "WEB-DL": 2, "WEBRIP": 2, "HDTV": 2,
		"576P": 1, "480P": 1, "DVDRIP": 1,
	}
	return ranks[strings.ToUpper(q)]
}

func resolveDupes(show string, seasons []seasonEntry, cfg *config.Config, dryRun, auto, nonInteractive bool, log Log) []seasonEntry {
	byS := map[int][]seasonEntry{}
	for _, s := range seasons {
		byS[s.season] = append(byS[s.season], s)
	}
	var snums []int
	for sn := range byS {
		snums = append(snums, sn)
	}
	sort.Ints(snums)

	var resolved []seasonEntry
	for _, sn := range snums {
		entries := byS[sn]
		if len(entries) == 1 {
			resolved = append(resolved, entries[0])
			continue
		}
		log.Normal("    [DUPLICATE] %s S%02d: %d sources", show, sn, len(entries))
		for i, se := range entries {
			q := common.ExtractQuality(se.folder)
			if q == "" {
				q = "unknown"
			}
			log.Normal("      [%d] %s  (%s)", i+1, se.folder, q)
		}
		if dryRun || nonInteractive || auto || cfg.PolicyDuplicateSeason != "prompt" {
			pick := entries[0]
			reason := "policy: skip"
			if dryRun {
				reason = "dry-run"
			} else if nonInteractive {
				reason = "non-interactive"
			} else if auto {
				reason = "auto"
			} else if cfg.PolicyDuplicateSeason == "highest" {
				best := 0
				bestQ := ""
				for i, se := range entries {
					q := common.ExtractQuality(se.folder)
					if qualityRank(q) > qualityRank(bestQ) {
						best, bestQ = i, q
					}
				}
				pick = entries[best]
				reason = "policy: highest"
			}
			log.Normal("      (%s: %s)", reason, pick.folder)
			resolved = append(resolved, pick)
		} else {
			choices := make([]string, len(entries))
			for i := range entries {
				choices[i] = strconv.Itoa(i + 1)
			}
			c := common.PromptChoice(fmt.Sprintf("      Choose [1-%d]: ", len(entries)), choices)
			n, _ := strconv.Atoi(c)
			resolved = append(resolved, entries[n-1])
		}
	}
	return resolved
}

// ---------------------------------------------------------------------------
// Season symlink conversion
// ---------------------------------------------------------------------------

// convertSeason replaces a season folder symlink with a real directory,
// re-linking all episodes from the source folder individually.
// This is required when individual episode symlinks need to coexist
// with a season that was previously symlinked as a whole folder.
func convertSeason(show string, snum int, path string, cfg *config.Config, dryRun bool, log Log, col *state.Collector) bool {
	target, err := os.Readlink(path)
	if err != nil {
		log.Normal("    [ERROR] readlink %s: %v", path, err)
		return false
	}
	// Translate container path back to host.
	if !strings.HasPrefix(target, cfg.ContainerRoot) {
		log.Normal("    [ERROR] target %s does not start with container root %s", target, cfg.ContainerRoot)
		return false
	}
	src := cfg.HostRoot + target[len(cfg.ContainerRoot):]
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		log.Normal("    [ERROR] target missing: %s", src)
		return false
	}

	folderQ := common.ExtractQuality(filepath.Base(src))

	subEntries, err := os.ReadDir(src)
	if err != nil {
		log.Normal("    [ERROR] scan: %v", err)
		return false
	}
	var files []os.DirEntry
	for _, f := range subEntries {
		if f.Type().IsRegular() && common.IsVideo(f.Name()) && !common.IsSample(f.Name()) {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

	log.Verbose("    Converting Season %02d -> real dir (%d episodes)", snum, len(files))
	if !dryRun {
		sp, err := common.NewSafePath(path, cfg.OutputDirs)
		if err != nil {
			log.Normal("    [ERROR] %v", err)
			return false
		}
		if err := common.Remove(sp); err != nil {
			log.Normal("    [ERROR] remove symlink: %v", err)
			return false
		}
		if err := common.MkdirAll(sp, 0o755); err != nil {
			log.Normal("    [ERROR] mkdir: %v", err)
			return false
		}
	}

	for _, f := range files {
		ext := filepath.Ext(f.Name())
		var linkName string
		ep := ParseBareEpisode(f.Name())
		if ep != nil {
			q := ep.Quality
			if q == "" {
				q = folderQ
			}
			linkName = BuildLinkName(show, ep.Season, ep.Episode, q, ext, ep.SecondEp)
		} else {
			linkName = f.Name()
		}
		lp := filepath.Join(path, linkName)
		log.Debug("      [LINK] %s", linkName)
		if !dryRun {
			if _, err := os.Lstat(lp); err == nil {
				continue // already exists
			}
			containerTarget, err := common.HostToContainer(
				filepath.Join(src, f.Name()), cfg.HostRoot, cfg.ContainerRoot)
			if err != nil {
				log.Normal("    [ERROR] %v", err)
				continue
			}
			lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
			if err != nil {
				continue
			}
			if err := common.Symlink(containerTarget, lpSafe); err == nil && ep != nil {
				var sep *int
				if ep.SecondEp >= 0 {
					v := ep.SecondEp
					sep = &v
				}
				col.RecordTVEpisodeLink(show, ep.Season, ep.Episode, sep, ep.Quality, filepath.Join(src, f.Name()), lp)
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Pass 2: bare files
// ---------------------------------------------------------------------------

type conflictInfo struct {
	folder  string // for quality/missing types: source season folder
	quality string // for quality type: existing quality
	seaDir  string // for bare_dir type: season output dir path
}

type bareConflict struct {
	show     string
	season   int
	episode  int
	quality  string
	filePath string
	ctype    string // "quality" | "missing" | "bare_dir"
	info     conflictInfo
	secondEp int // -1 if single episode
}

type bareNew struct {
	show     string
	season   int
	episode  int
	quality  string
	filePath string
	secondEp int
}

type misplacedMovie struct {
	name     string
	filePath string
}

func scanBare(grouped map[string][]seasonEntry, cfg *config.Config, log Log) ([]bareNew, []bareConflict, []string, []misplacedMovie) {
	var newEps []bareNew
	var conflicts []bareConflict
	var unmatched []string
	var misplaced []misplacedMovie

	// Scan tv_linked once so findMatch doesn't re-read it for every bare file.
	var linkedEntries []os.DirEntry
	if info, err := os.Stat(cfg.TVLinked); err == nil && info.IsDir() {
		linkedEntries, _ = os.ReadDir(cfg.TVLinked)
	}

	for _, srcDir := range cfg.TVSources {
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !e.Type().IsRegular() || !common.IsVideo(e.Name()) || common.IsSample(e.Name()) {
				continue
			}
			r := ParseBareEpisode(e.Name())
			if r == nil {
				t := movies.Title(e.Name())
				y := movies.Year(e.Name())
				if t != "" && y != "" {
					misplaced = append(misplaced, misplacedMovie{e.Name(), filepath.Join(srcDir, e.Name())})
				} else {
					unmatched = append(unmatched, e.Name())
				}
				continue
			}

			rawShow := r.Show
			resolved, _ := resolver.ResolveTVName(
				rawShow, cfg.TVNameOverrides, cfg.TMDBApiKey, cfg.TMDBConfidence, log)
			show := common.Sanitize(resolved)
			matched := findMatch(show, grouped, linkedEntries)
			if matched != "" && matched != show {
				show = matched
			}

			sp := filepath.Join(cfg.TVLinked, show, fmt.Sprintf("Season %02d", r.Season))
			var canon string
			if matched != "" {
				if _, ok := grouped[matched]; ok {
					canon = matched
				}
			}

			bareFilePath := filepath.Join(srcDir, e.Name())

			if canon == "" {
				// Show not in grouped — check if output season dir already exists.
				if common.IsBareDir(sp) {
					if epSymlinkExists(sp, r.Episode, r.Season) {
						continue
					}
					conflicts = append(conflicts, bareConflict{
						show: show, season: r.Season, episode: r.Episode,
						quality: r.Quality, filePath: bareFilePath,
						ctype: "bare_dir", info: conflictInfo{seaDir: sp},
						secondEp: r.SecondEp,
					})
				} else {
					newEps = append(newEps, bareNew{
						show: show, season: r.Season, episode: r.Episode,
						quality: r.Quality, filePath: bareFilePath,
						secondEp: r.SecondEp,
					})
				}
				continue
			}

			// Find the source folder for this season.
			var sfolder string
			var sSourceDir string
			for _, se := range grouped[canon] {
				if se.season == r.Season {
					sfolder = se.folder
					sSourceDir = se.sourceDir
					break
				}
			}
			if sfolder == "" {
				newEps = append(newEps, bareNew{
					show: show, season: r.Season, episode: r.Episode,
					quality: r.Quality, filePath: bareFilePath,
					secondEp: r.SecondEp,
				})
				continue
			}

			srcPath := filepath.Join(sSourceDir, sfolder)
			exists, eq := epInFolder(srcPath, r.Episode, r.Season)
			if exists {
				if eq != "" && r.Quality != "" && strings.ToUpper(eq) != strings.ToUpper(r.Quality) {
					if common.IsBareDir(sp) {
						lname := BuildLinkName(show, r.Season, r.Episode, r.Quality,
							filepath.Ext(e.Name()), r.SecondEp)
						if common.IsSymlink(filepath.Join(sp, lname)) {
							continue
						}
						conflicts = append(conflicts, bareConflict{
							show: show, season: r.Season, episode: r.Episode,
							quality: r.Quality, filePath: bareFilePath,
							ctype: "bare_dir", info: conflictInfo{seaDir: sp},
							secondEp: r.SecondEp,
						})
					} else {
						conflicts = append(conflicts, bareConflict{
							show: show, season: r.Season, episode: r.Episode,
							quality: r.Quality, filePath: bareFilePath,
							ctype: "quality", info: conflictInfo{folder: sfolder, quality: eq},
							secondEp: r.SecondEp,
						})
					}
				}
				// Same quality already covered — skip silently.
			} else {
				if common.IsBareDir(sp) {
					if epSymlinkExists(sp, r.Episode, r.Season) {
						continue
					}
					conflicts = append(conflicts, bareConflict{
						show: show, season: r.Season, episode: r.Episode,
						quality: r.Quality, filePath: bareFilePath,
						ctype: "bare_dir", info: conflictInfo{seaDir: sp},
						secondEp: r.SecondEp,
					})
				} else {
					conflicts = append(conflicts, bareConflict{
						show: show, season: r.Season, episode: r.Episode,
						quality: r.Quality, filePath: bareFilePath,
						ctype: "missing", info: conflictInfo{folder: sfolder},
						secondEp: r.SecondEp,
					})
				}
			}
		}
	}
	return newEps, conflicts, unmatched, misplaced
}

func handleNew(newEps []bareNew, cfg *config.Config, dryRun bool, log Log, col *state.Collector) int {
	if len(newEps) == 0 {
		return 0
	}
	// Group by (show, season).
	type key struct{ show string; season int }
	byShow := map[key][]bareNew{}
	for _, n := range newEps {
		k := key{n.show, n.season}
		byShow[k] = append(byShow[k], n)
	}
	// Sort keys for deterministic output.
	keys := make([]key, 0, len(byShow))
	for k := range byShow {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].show != keys[j].show {
			return keys[i].show < keys[j].show
		}
		return keys[i].season < keys[j].season
	})

	count := 0
	for _, k := range keys {
		eps := byShow[k]
		sl := fmt.Sprintf("Season %02d", k.season)
		sd := filepath.Join(cfg.TVLinked, k.show, sl)
		log.Verbose("  %s / %s  (%d ep(s))", k.show, sl, len(eps))

		showDirSafe, err := common.NewSafePath(filepath.Join(cfg.TVLinked, k.show), cfg.OutputDirs)
		if err == nil {
			common.EnsureDir(showDirSafe, dryRun)
		}
		sdSafe, err := common.NewSafePath(sd, cfg.OutputDirs)
		if err == nil {
			common.EnsureDir(sdSafe, dryRun)
		}

		sort.Slice(eps, func(i, j int) bool { return eps[i].episode < eps[j].episode })
		for _, ep := range eps {
			ext := filepath.Ext(ep.filePath)
			ln := BuildLinkName(ep.show, ep.season, ep.episode, ep.quality, ext, ep.secondEp)
			lp := filepath.Join(sd, ln)
			if common.IsSymlink(lp) {
				continue
			}
			lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
			if err != nil {
				continue
			}
			if common.MakeSymlink(lpSafe, ep.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
				log.Verbose("    [LINK] %s", ln)
				var sep *int
				if ep.secondEp >= 0 {
					v := ep.secondEp
					sep = &v
				}
				col.RecordTVEpisodeLink(ep.show, ep.season, ep.episode, sep, ep.quality, ep.filePath, lp)
				count++
			}
		}
	}
	return count
}

func handleConflicts(conflicts []bareConflict, cfg *config.Config, dryRun, auto, nonInteractive bool, log Log, col *state.Collector) int {
	if len(conflicts) == 0 {
		return 0
	}
	if dryRun {
		for _, c := range conflicts {
			ep := fmt.Sprintf("E%02d", c.episode)
			if c.secondEp >= 0 {
				ep += fmt.Sprintf("-E%02d", c.secondEp)
			}
			q := c.quality
			if q == "" {
				q = "?"
			}
			log.Verbose("  %s S%02d%s [%s] (%s)", c.show, c.season, ep, q, c.ctype)
		}
		log.Normal("  %d conflict(s) (use sync without --dry-run)", len(conflicts))
		return 0
	}

	resolved := 0
	for _, c := range conflicts {
		sl := fmt.Sprintf("Season %02d", c.season)
		ext := filepath.Ext(c.filePath)
		ln := BuildLinkName(c.show, c.season, c.episode, c.quality, ext, c.secondEp)
		sp := filepath.Join(cfg.TVLinked, c.show, sl)

		switch c.ctype {
		case "quality", "missing":
			if common.IsBareDir(sp) {
				log.Verbose("  %s S%02dE%02d: already converted, adding", c.show, c.season, c.episode)
				lp := filepath.Join(sp, ln)
				lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
				if err != nil {
					continue
				}
				if common.MakeSymlink(lpSafe, c.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
					col.RecordTVEpisodeLink(c.show, c.season, c.episode, secondEpPtr(c.secondEp), c.quality, c.filePath, lp)
					resolved++
				}
				continue
			}
			if nonInteractive {
				log.Normal("    [WATCH] Skipped conflict: %s S%02dE%02d, needs manual review", c.show, c.season, c.episode)
				continue
			}
			if cfg.PolicyConflictConversion == "prompt" && !auto {
				log.Normal("\n  %s / %s / E%02d", c.show, sl, c.episode)
				log.Normal("    File: %s", filepath.Base(c.filePath))
				if c.ctype == "quality" {
					log.Normal("    Existing: %s, this: %s", c.info.quality, c.quality)
				} else {
					log.Normal("    Not in folder: %s", c.info.folder)
				}
				log.Normal("    Requires converting season symlink to real dir.")
				fmt.Println("    [1] Convert and add  [s] Skip")
				ch := common.PromptChoice("    Choice: ", []string{"1", "s"})
				if ch == "s" {
					continue
				}
			}
			if convertSeason(c.show, c.season, sp, cfg, dryRun, log, col) {
				lp := filepath.Join(sp, ln)
				lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
				if err != nil {
					continue
				}
				if common.MakeSymlink(lpSafe, c.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
					col.RecordTVEpisodeLink(c.show, c.season, c.episode, secondEpPtr(c.secondEp), c.quality, c.filePath, lp)
					resolved++
				}
			}

		case "bare_dir":
			if auto || nonInteractive || cfg.PolicyConflictConversion == "auto" {
				lp := filepath.Join(sp, ln)
				lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
				if err != nil {
					continue
				}
				if common.MakeSymlink(lpSafe, c.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
					col.RecordTVEpisodeLink(c.show, c.season, c.episode, secondEpPtr(c.secondEp), c.quality, c.filePath, lp)
					resolved++
				}
			} else {
				log.Normal("\n  %s / %s / E%02d (season dir exists)", c.show, sl, c.episode)
				fmt.Println("    [1] Add  [s] Skip")
				ch := common.PromptChoice("    Choice: ", []string{"1", "s"})
				if ch == "1" {
					lp := filepath.Join(sp, ln)
					lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
					if err != nil {
						continue
					}
					if common.MakeSymlink(lpSafe, c.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
						col.RecordTVEpisodeLink(c.show, c.season, c.episode, secondEpPtr(c.secondEp), c.quality, c.filePath, lp)
						resolved++
					}
				}
			}
		}
	}
	return resolved
}

// ---------------------------------------------------------------------------
// Warnings
// ---------------------------------------------------------------------------

func warnings(grouped map[string][]seasonEntry, pt []passthroughEntry) []string {
	var w []string
	for show, seasons := range grouped {
		seen := map[int]string{}
		for _, s := range seasons {
			if prev, ok := seen[s.season]; ok {
				w = append(w, fmt.Sprintf("Duplicate season: %s S%02d in '%s' and '%s'",
					show, s.season, prev, s.folder))
			} else {
				seen[s.season] = s.folder
			}
		}
	}
	gn := map[string]string{}
	for n := range grouped {
		gn[normCompare(n)] = n
	}
	for _, p := range pt {
		n := normCompare(p.name)
		if canonical, ok := gn[n]; ok {
			w = append(w, fmt.Sprintf("Name overlap: '%s' and unprocessed '%s'", canonical, p.name))
		}
	}
	return w
}

// secondEpPtr converts the int sentinel (-1 = single episode) to *int for state recording.
func secondEpPtr(v int) *int {
	if v < 0 {
		return nil
	}
	return &v
}

// ---------------------------------------------------------------------------
// Main pipeline
// ---------------------------------------------------------------------------

// Run executes the full two-pass TV pipeline and returns summary counts.
func Run(cfg *config.Config, dryRun, auto, nonInteractive bool, log Log, col *state.Collector) map[string]int {
	tvSafe, err := common.NewSafePath(cfg.TVLinked, cfg.OutputDirs)
	if err != nil {
		log.Normal("[ERROR] tv_linked is not a registered output: %v", err)
		return nil
	}
	if err := common.EnsureDir(tvSafe, dryRun); err != nil {
		log.Normal("[ERROR] Cannot create tv_linked: %v", err)
		return nil
	}

	grouped, pt := scanTV(cfg)
	mini := scanMiniseries(cfg)

	// Pass 1: season folder linking.
	shows, seasonsLinked := 0, 0
	showNames := make([]string, 0, len(grouped))
	for show := range grouped {
		showNames = append(showNames, show)
	}
	sort.Strings(showNames)

	for _, show := range showNames {
		seasons := grouped[show]
		ss := resolveDupes(show, seasons, cfg, dryRun, auto, nonInteractive, log)
		shows++

		showDirPath := filepath.Join(cfg.TVLinked, show)
		showDirSafe, err := common.NewSafePath(showDirPath, cfg.OutputDirs)
		if err == nil {
			common.EnsureDir(showDirSafe, dryRun)
		}

		var strs []string
		for _, s := range ss {
			strs = append(strs, fmt.Sprintf("S%02d", s.season))
		}
		log.Verbose("  %s  (%s)", show, strings.Join(strs, ", "))

		for _, s := range ss {
			lp := filepath.Join(cfg.TVLinked, show, fmt.Sprintf("Season %02d", s.season))
			tgt := filepath.Join(s.sourceDir, s.folder)
			lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
			if err != nil {
				log.Normal("[ERROR] %v", err)
				continue
			}
			if common.MakeSymlink(lpSafe, tgt, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
				col.RecordTVSeasonLink(show, s.season, tgt, lp)
				seasonsLinked++
			} else {
				col.RecordTVSeasonSkip(show, s.season, tgt, lp)
			}
		}
	}

	// Passthrough: unprocessed folders passed through as-is.
	ptCount := 0
	sort.Slice(pt, func(i, j int) bool { return pt[i].name < pt[j].name })
	for _, entry := range pt {
		cn := common.CleanPassthroughName(entry.name)
		lp := filepath.Join(cfg.TVLinked, cn)
		tgt := filepath.Join(entry.sourceDir, entry.name)
		lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
		if err != nil {
			continue
		}
		if common.MakeSymlink(lpSafe, tgt, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
			log.Verbose("  [UNPROCESSED] %s", cn)
			ptCount++
		}
	}

	// Miniseries from movies_source.
	miniCount := 0
	miniNames := make([]string, 0, len(mini))
	for show := range mini {
		miniNames = append(miniNames, show)
	}
	sort.Strings(miniNames)

	for _, show := range miniNames {
		m := mini[show]
		log.Verbose("  [MINI] %s  (%d eps)", show, len(m.episodes))
		sn := m.episodes[0].season
		sd := filepath.Join(cfg.TVLinked, show, fmt.Sprintf("Season %02d", sn))
		sdSafe, err := common.NewSafePath(sd, cfg.OutputDirs)
		if err == nil {
			common.EnsureDir(sdSafe, dryRun)
		}
		for _, ep := range m.episodes {
			op := filepath.Join(m.sourceDir, m.folder, ep.file)
			ext := filepath.Ext(ep.file)
			var ln string
			if common.ReSxxExx.MatchString(ep.file) {
				ln = ep.file
			} else {
				ln = fmt.Sprintf("%s.S%02dE%02d%s", show, ep.season, ep.episode, ext)
			}
			lp := filepath.Join(sd, ln)
			lpSafe, err := common.NewSafePath(lp, cfg.OutputDirs)
			if err != nil {
				continue
			}
			if common.MakeSymlink(lpSafe, op, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
				col.RecordTVEpisodeLink(show, ep.season, ep.episode, nil, "", op, lp)
				miniCount++
			}
		}
	}

	// Pass 2: bare episode files.
	newEps, conflicts, unmatched, misplaced := scanBare(grouped, cfg, log)
	newCount := handleNew(newEps, cfg, dryRun, log, col)
	conflictCount := handleConflicts(conflicts, cfg, dryRun, auto, nonInteractive, log, col)
	col.RecordTVUnmatched(unmatched)

	// Link misplaced movies (bare movie files in TV source).
	misplacedCount := 0
	if len(misplaced) > 0 {
		log.Normal("  [MISPLACED] %d bare movie(s) in TV source", len(misplaced))
		for _, m := range misplaced {
			t := movies.Title(m.name)
			y := movies.Year(m.name)
			q := common.ExtractQuality(m.name)
			folder := fmt.Sprintf("%s (%s)", t, y)
			ext := filepath.Ext(m.filePath)
			var linkName string
			if q != "" {
				linkName = fmt.Sprintf("%s - %s%s", folder, q, ext)
			} else {
				linkName = folder + ext
			}
			linkDirPath := filepath.Join(cfg.MoviesLinked, folder)
			linkDirSafe, err := common.NewSafePath(linkDirPath, cfg.OutputDirs)
			if err != nil {
				log.Verbose("    [SKIP] %s: %v", m.name, err)
				continue
			}
			common.EnsureDir(linkDirSafe, dryRun)
			linkFullPath := filepath.Join(linkDirPath, linkName)
			linkSafe, err := common.NewSafePath(linkFullPath, cfg.OutputDirs)
			if err != nil {
				continue
			}
			if common.MakeSymlink(linkSafe, m.filePath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
				col.RecordMovieLink(t, y, q, m.filePath, linkFullPath)
				misplacedCount++
				log.Verbose("    %s -> %s", m.name, folder)
			}
		}
	}

	if len(unmatched) > 0 {
		log.Normal("  [UNMATCHED] %d bare file(s):", len(unmatched))
		for _, fn := range unmatched {
			log.Verbose("    %s", fn)
		}
	}

	for _, w := range warnings(grouped, pt) {
		log.Normal("  [WARN] %s", w)
	}

	log.Normal("[TV] %d shows (%d seasons), %d unprocessed, %d miniseries, %d bare new, %d conflicts, %d misplaced, %d unmatched",
		shows, seasonsLinked, ptCount, miniCount, newCount, conflictCount, misplacedCount, len(unmatched))

	return map[string]int{
		"shows":       shows,
		"seasons":     seasonsLinked,
		"passthrough": ptCount,
		"miniseries":  miniCount,
		"bare_new":    newCount,
		"conflicts":   conflictCount,
		"misplaced":   misplacedCount,
		"unmatched":   len(unmatched),
	}
}
