package movies

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/resolver"
	"github.com/AnAngryGoose/medialnk/internal/state"
)

// Log is the subset of logger methods the movies pipeline uses.
type Log interface {
	Normal(format string, args ...any)
	Verbose(format string, args ...any)
	Debug(format string, args ...any)
}

// movieEntry is an internal categorized movie entry.
type movieEntry struct {
	sourceName string // original dir/file name in source directory
	sourceDir  string // absolute path of the source directory
	title      string
	year       string
	videoPath  string // absolute host path of the primary video file
	quality    string // may be empty for single-version movies
}

// flaggedItem is a source entry that couldn't be parsed.
type flaggedItem struct {
	name      string
	reason    string
	sourceDir string
}

// ambiguousItem is a Part.N folder needing manual routing.
type ambiguousItem struct {
	name      string
	parts     []string
	sourceDir string
}

// scan categorizes entries across all movie source directories.
// Returns (movies, flagged, skipped, ambiguous).
// - movies: resolved entries ready for linking
// - flagged: entries that couldn't be parsed
// - skipped: miniseries entry names
// - ambiguous: Part.N folders
func scan(cfg *config.Config) ([]movieEntry, []flaggedItem, []string, []ambiguousItem) {
	seen := map[string][]movieEntry{}
	var flagged []flaggedItem
	var skipped []string
	var ambiguous []ambiguousItem

	for _, srcDir := range cfg.MoviesSources {
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			name := e.Name()

			if e.Type().IsRegular() {
				// Bare file in source directory.
				if !common.IsVideo(name) || common.IsSample(name) {
					continue
				}
				if common.IsEpisodeFile(name, false) {
					skipped = append(skipped, name)
					continue
				}
				y := Year(name)
				t := Title(name)
				if override, ok := cfg.MovieTitleOverrides[t]; ok {
					t = override
				}
				q := common.ExtractQuality(name)
				vp := filepath.Join(srcDir, name)
				if t == "" {
					flagged = append(flagged, flaggedItem{name, "no title parsed", srcDir})
					continue
				}
				if y == "" {
					flagged = append(flagged, flaggedItem{name, "no year found", srcDir})
					continue
				}
				key := fmt.Sprintf("%s (%s)", t, y)
				seen[key] = append(seen[key], movieEntry{name, srcDir, t, y, vp, q})

			} else if e.IsDir() {
				folderPath := filepath.Join(srcDir, name)
				if isMiniseries(folderPath) {
					skipped = append(skipped, name)
					continue
				}
				ambig, parts := isAmbiguousParts(folderPath)
				if ambig {
					ambiguous = append(ambiguous, ambiguousItem{name, parts, srcDir})
					continue
				}
				vids, _ := common.FindVideos(folderPath, false, true, true)
				if len(vids) == 0 {
					flagged = append(flagged, flaggedItem{name, "no video file", srcDir})
					continue
				}
				primary := common.LargestVideo(vids)
				y := Year(name)
				if y == "" {
					y = Year(primary.Name)
				}
				t := Title(name)
				if override, ok := cfg.MovieTitleOverrides[t]; ok {
					t = override
				}
				q := common.ExtractQuality(name)
				if q == "" {
					q = common.ExtractQuality(primary.Name)
				}
				if t == "" {
					flagged = append(flagged, flaggedItem{name, "no title parsed", srcDir})
					continue
				}
				if y == "" {
					flagged = append(flagged, flaggedItem{name, "no year found", srcDir})
					continue
				}
				key := fmt.Sprintf("%s (%s)", t, y)
				seen[key] = append(seen[key], movieEntry{name, srcDir, t, y, primary.Path, q})
			}
		}
	}

	movies := resolveVersions(seen)
	return movies, flagged, skipped, ambiguous
}

// resolveVersions flattens the grouped map into a sorted slice of movieEntry,
// assigning quality labels to multi-version groups and numbering same-quality dupes.
func resolveVersions(seen map[string][]movieEntry) []movieEntry {
	var movies []movieEntry
	for _, versions := range seen {
		if len(versions) == 1 {
			v := versions[0]
			v.quality = "" // no quality label for single-version movies
			movies = append(movies, v)
			continue
		}
		// Ensure every version has a quality label.
		resolved := make([]movieEntry, len(versions))
		for i, v := range versions {
			if v.quality == "" {
				v.quality = common.ExtractQuality(filepath.Base(v.videoPath))
			}
			if v.quality == "" {
				v.quality = "UNKNOWN"
			}
			resolved[i] = v
		}
		// Count how many times each quality label appears.
		qcount := map[string]int{}
		for _, v := range resolved {
			qcount[v.quality]++
		}
		qseen := map[string]int{}
		for _, v := range resolved {
			if qcount[v.quality] == 1 {
				// unique quality — use as-is
			} else {
				qseen[v.quality]++
				if qseen[v.quality] > 1 {
					v.quality = fmt.Sprintf("%s.%d", v.quality, qseen[v.quality])
				}
			}
			movies = append(movies, v)
		}
	}
	// Sort by title then year (case-insensitive title).
	sort.Slice(movies, func(i, j int) bool {
		ai := strings.ToLower(movies[i].title) + movies[i].year
		aj := strings.ToLower(movies[j].title) + movies[j].year
		return ai < aj
	})
	return movies
}

// Run executes the full movies pipeline and returns summary counts.
func Run(cfg *config.Config, dryRun, auto, nonInteractive bool, log Log, col *state.Collector) map[string]int {
	// Ensure output directory exists.
	mlSafe, err := common.NewSafePath(cfg.MoviesLinked, cfg.OutputDirs)
	if err != nil {
		log.Normal("[ERROR] movies_linked is not a registered output: %v", err)
		return nil
	}
	if err := common.EnsureDir(mlSafe, dryRun); err != nil {
		log.Normal("[ERROR] Cannot create movies_linked: %v", err)
		return nil
	}

	movies, flagged, skipped, ambiguous := scan(cfg)

	linked := 0
	for _, m := range movies {
		folder := fmt.Sprintf("%s (%s)", m.title, m.year)
		ext := filepath.Ext(m.videoPath)
		var linkName string
		if m.quality != "" {
			linkName = fmt.Sprintf("%s - %s%s", folder, m.quality, ext)
		} else {
			linkName = folder + ext
		}
		linkDirPath := filepath.Join(cfg.MoviesLinked, folder)
		linkDirSafe, err := common.NewSafePath(linkDirPath, cfg.OutputDirs)
		if err != nil {
			log.Normal("[ERROR] %v", err)
			continue
		}
		if m.quality != "" {
			log.Verbose("  %s  [%s]", folder, m.quality)
		} else {
			log.Verbose("  %s", folder)
		}
		if err := common.EnsureDir(linkDirSafe, dryRun); err != nil {
			log.Normal("[ERROR] Cannot create dir %s: %v", linkDirPath, err)
			continue
		}
		linkFullPath := filepath.Join(linkDirPath, linkName)
		linkSafe, err := common.NewSafePath(linkFullPath, cfg.OutputDirs)
		if err != nil {
			log.Normal("[ERROR] %v", err)
			continue
		}
		if common.MakeSymlink(linkSafe, m.videoPath, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
			linked++
			col.RecordMovieLink(m.title, m.year, m.quality, m.videoPath, linkFullPath)
		} else {
			col.RecordMovieSkip(m.title, m.year, m.quality, m.videoPath, linkFullPath)
		}
	}

	// Flagged entries — record non-yearless immediately; defer yearless until after TMDB.
	var noYear []flaggedItem
	for _, f := range flagged {
		log.Verbose("  [NO LINK] %s: %s", f.name, f.reason)
		if f.reason == "no year found" {
			noYear = append(noYear, f)
		} else {
			col.RecordMovieFlagged(f.name, f.reason)
		}
	}

	// Skipped miniseries
	for _, s := range skipped {
		log.Debug("  [SKIP] %s", s)
	}

	// Ambiguous Part.N folders
	if len(ambiguous) > 0 {
		log.Normal("  [AMBIGUOUS] %d Part.N folders", len(ambiguous))
		if !dryRun {
			for _, a := range ambiguous {
				t := Title(a.name)
				y := Year(a.name)
				var label string
				if y != "" {
					label = fmt.Sprintf("%s (%s)", t, y)
				} else {
					label = t
				}
				if nonInteractive || cfg.PolicyPartN == "skip" {
					if nonInteractive {
						log.Normal("    [WATCH] Skipped ambiguous Part.N: %s", label)
					} else {
						log.Normal("    [SKIP] Ambiguous Part.N: %s (policy: skip)", label)
					}
					col.RecordMovieFlagged(a.name, "ambiguous Part.N, needs manual review")
					continue
				}
				if auto {
					log.Verbose("    [AUTO] %s -> movie", label)
					routeMovie(a.name, t, y, a.sourceDir, cfg, dryRun, log, col)
				} else {
					log.Normal("    %s (%d parts)", label, len(a.parts))
					fmt.Println("    [1] Movie  [2] TV (skip)  [s] Skip")
					c := common.PromptChoice("    Choice: ", []string{"1", "2", "s"})
					if c == "1" {
						routeMovie(a.name, t, y, a.sourceDir, cfg, dryRun, log, col)
					}
				}
			}
		}
	}

	// TMDB resolution for yearless entries
	tmdbCount := 0
	var handled map[string]bool
	if len(noYear) > 0 && cfg.TMDBApiKey != "" && (auto || !dryRun) {
		tmdbCount, handled = tmdbResolve(noYear, cfg, dryRun, log, col)
	}
	// Flag yearless entries that TMDB didn't handle.
	for _, f := range noYear {
		if !handled[f.name] {
			col.RecordMovieFlagged(f.name, f.reason)
		}
	}

	log.Normal("[MOVIES] %d entries: %d linked, %d flagged, %d skipped, %d ambiguous, %d TMDB resolved",
		len(movies), linked, len(flagged), len(skipped), len(ambiguous), tmdbCount)

	return map[string]int{
		"total":         len(movies),
		"linked":        linked,
		"flagged":       len(flagged),
		"skipped":       len(skipped),
		"ambiguous":     len(ambiguous),
		"tmdb_resolved": tmdbCount,
	}
}

// routeMovie handles an ambiguous Part.N folder confirmed as a movie.
func routeMovie(entryName, t, y, sourceDir string, cfg *config.Config, dryRun bool, log Log, col *state.Collector) {
	folderPath := filepath.Join(sourceDir, entryName)
	vids, _ := common.FindVideos(folderPath, false, true, true)
	if len(vids) == 0 {
		log.Normal("    [FAIL] No video files in %s", entryName)
		return
	}
	primary := common.LargestVideo(vids)
	if y == "" {
		y = Year(primary.Name)
	}
	if t == "" {
		t = Title(primary.Name)
	}
	q := common.ExtractQuality(entryName)
	if q == "" {
		q = common.ExtractQuality(primary.Name)
	}
	if y == "" {
		log.Normal("    [WARN] No year for %s", t)
		return
	}
	folderName := fmt.Sprintf("%s (%s)", t, y)
	ext := filepath.Ext(primary.Path)
	var linkName string
	if q != "" {
		linkName = fmt.Sprintf("%s - %s%s", folderName, q, ext)
	} else {
		linkName = folderName + ext
	}
	linkDirPath := filepath.Join(cfg.MoviesLinked, folderName)
	linkDirSafe, err := common.NewSafePath(linkDirPath, cfg.OutputDirs)
	if err != nil {
		log.Normal("[ERROR] %v", err)
		return
	}
	common.EnsureDir(linkDirSafe, dryRun)
	linkFullPath := filepath.Join(linkDirPath, linkName)
	linkSafe, err := common.NewSafePath(linkFullPath, cfg.OutputDirs)
	if err != nil {
		log.Normal("[ERROR] %v", err)
		return
	}
	if common.MakeSymlink(linkSafe, primary.Path, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
		col.RecordMovieLink(t, y, q, primary.Path, linkFullPath)
	}
}

// tmdbResolve runs concurrent TMDB lookups for yearless entries (up to 8 at a time).
// Returns the count of resolved entries and a set of entry names that were handled.
func tmdbResolve(noYear []flaggedItem, cfg *config.Config, dryRun bool, log Log, col *state.Collector) (int, map[string]bool) {
	log.Normal("  [TMDB] Resolving %d yearless entries...", len(noYear))

	type result struct {
		entry     string
		sourceDir string
		found     string
		yr        string
	}

	sem := make(chan struct{}, 8)
	results := make(chan result, len(noYear))
	var wg sync.WaitGroup

	for _, item := range noYear {
		wg.Add(1)
		go func(e, sd string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			t := Title(e)
			if t == "" || len(t) < 4 {
				results <- result{e, sd, "", ""}
				return
			}
			found, yr, _ := resolver.SearchMovie(t, cfg.TMDBApiKey, cfg.TMDBConfidence, log)
			results <- result{e, sd, found, yr}
		}(item.name, item.sourceDir)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	handled := make(map[string]bool)
	count := 0
	for r := range results {
		handled[r.entry] = true
		if r.found == "" {
			// TMDB miss — link with parsed name as fallback.
			t := Title(r.entry)
			if t == "" || len(t) < 4 {
				log.Verbose("    [MISS] %s (no usable title)", r.entry)
				col.RecordMovieUnmatched(r.entry)
				continue
			}
			ep := filepath.Join(r.sourceDir, r.entry)
			var vp string
			info, err := os.Stat(ep)
			if err == nil && info.Mode().IsRegular() {
				vp = ep
			} else if err == nil && info.IsDir() {
				vids, _ := common.FindVideos(ep, false, true, true)
				if len(vids) > 0 {
					vp = common.LargestVideo(vids).Path
				}
			}
			if vp == "" {
				col.RecordMovieUnmatched(r.entry)
				continue
			}
			ext := filepath.Ext(vp)
			linkDirPath := filepath.Join(cfg.MoviesLinked, t)
			linkDirSafe, err := common.NewSafePath(linkDirPath, cfg.OutputDirs)
			if err != nil {
				col.RecordMovieUnmatched(r.entry)
				continue
			}
			common.EnsureDir(linkDirSafe, dryRun)
			linkFullPath := filepath.Join(linkDirPath, t+ext)
			linkSafe, err := common.NewSafePath(linkFullPath, cfg.OutputDirs)
			if err != nil {
				col.RecordMovieUnmatched(r.entry)
				continue
			}
			if common.MakeSymlink(linkSafe, vp, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
				col.RecordMovieLinkUnverified(t, "", "", vp, linkFullPath)
				log.Verbose("    [TMDB] %s -> linked as '%s' (unverified)", r.entry, t)
				count++
			}
			continue
		}
		// Determine video path: bare file or folder.
		ep := filepath.Join(r.sourceDir, r.entry)
		var vp string
		info, err := os.Stat(ep)
		if err == nil && info.Mode().IsRegular() {
			vp = ep
		} else if err == nil && info.IsDir() {
			vids, _ := common.FindVideos(ep, false, true, true)
			if len(vids) > 0 {
				vp = common.LargestVideo(vids).Path
			}
		}
		if vp == "" {
			continue
		}
		var folderName string
		if r.yr != "" {
			folderName = fmt.Sprintf("%s (%s)", r.found, r.yr)
		} else {
			folderName = r.found
		}
		ext := filepath.Ext(vp)
		linkDirPath := filepath.Join(cfg.MoviesLinked, folderName)
		linkDirSafe, err := common.NewSafePath(linkDirPath, cfg.OutputDirs)
		if err != nil {
			continue
		}
		common.EnsureDir(linkDirSafe, dryRun)
		linkFullPath := filepath.Join(linkDirPath, folderName+ext)
		linkSafe, err := common.NewSafePath(linkFullPath, cfg.OutputDirs)
		if err != nil {
			continue
		}
		if common.MakeSymlink(linkSafe, vp, dryRun, cfg.HostRoot, cfg.ContainerRoot) {
			col.RecordMovieLink(r.found, r.yr, "", vp, linkFullPath)
			count++
		}
	}
	return count, handled
}
