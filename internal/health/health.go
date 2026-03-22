// Package health validates source directories before sync.
// Catches silently unmounted mergerfs drives and other scenarios
// where source directories exist but are unexpectedly empty.
package health

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
)

// Result holds the health check outcome for a single source directory.
type Result struct {
	Dir        string // absolute path
	Label      string // "movies_source" or "tv_source"
	VideoCount int    // video files found (capped at min threshold)
	SentinelOK bool   // true if sentinel exists or not configured
	Pass       bool
	Reason     string // human-readable explanation on failure
}

// Check runs health checks against all source directories in cfg.
// Returns one Result per source dir and an overall pass bool.
// This function is read-only and never modifies the filesystem.
func Check(cfg *config.Config) ([]Result, bool) {
	var sources [][2]string
	for i, src := range cfg.MoviesSources {
		label := "movies_source"
		if len(cfg.MoviesSources) > 1 {
			label = fmt.Sprintf("movies_source[%d]", i)
		}
		sources = append(sources, [2]string{label, src})
	}
	for i, src := range cfg.TVSources {
		label := "tv_source"
		if len(cfg.TVSources) > 1 {
			label = fmt.Sprintf("tv_source[%d]", i)
		}
		sources = append(sources, [2]string{label, src})
	}

	allPass := true
	results := make([]Result, 0, len(sources))

	for _, s := range sources {
		r := Result{
			Dir:        s[1],
			Label:      s[0],
			SentinelOK: true,
		}

		// Sentinel file check.
		if cfg.HealthSentinelFile != "" {
			sentinel := cfg.HealthSentinelFile
			if !filepath.IsAbs(sentinel) {
				sentinel = filepath.Join(s[1], sentinel)
			}
			if _, err := os.Stat(sentinel); err != nil {
				r.SentinelOK = false
				r.Reason = fmt.Sprintf("sentinel file not found: %s", sentinel)
				r.Pass = false
				allPass = false
				results = append(results, r)
				continue
			}
		}

		// Minimum video file count.
		r.VideoCount = countVideos(s[1], cfg.HealthMinFiles)
		if r.VideoCount < cfg.HealthMinFiles {
			r.Reason = fmt.Sprintf("found %d video files, minimum is %d", r.VideoCount, cfg.HealthMinFiles)
			r.Pass = false
			allPass = false
		} else {
			r.Pass = true
		}

		results = append(results, r)
	}

	return results, allPass
}

// countVideos walks dir counting video files, stopping early once limit is reached.
func countVideos(dir string, limit int) int {
	count := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() && common.IsVideo(d.Name()) {
			count++
			if count >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return count
}
