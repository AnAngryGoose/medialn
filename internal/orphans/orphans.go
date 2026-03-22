// Package orphans finds source video files that have no corresponding
// symlink in the output (presentation) layer.
package orphans

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
)

// OrphanFile represents a source video file with no output symlink.
type OrphanFile struct {
	Path   string `json:"path"`
	Folder string `json:"folder"`
	Size   int64  `json:"size"`
}

// Report holds the complete orphan scan results.
type Report struct {
	MoviesOrphans []OrphanFile `json:"movies_orphans"`
	TVOrphans     []OrphanFile `json:"tv_orphans"`
	MoviesCovered int          `json:"movies_covered"`
	TVCovered     int          `json:"tv_covered"`
}

// TotalOrphans returns the total count of orphan files.
func (r *Report) TotalOrphans() int {
	return len(r.MoviesOrphans) + len(r.TVOrphans)
}

// TotalSource returns the total count of source video files.
func (r *Report) TotalSource() int {
	return r.MoviesCovered + len(r.MoviesOrphans) + r.TVCovered + len(r.TVOrphans)
}

// CoveragePct returns the percentage of source files that are covered.
func (r *Report) CoveragePct() float64 {
	total := r.TotalSource()
	if total == 0 {
		return 100.0
	}
	return float64(total-r.TotalOrphans()) / float64(total) * 100.0
}

// Scan walks source and output directories, cross-references symlink targets
// against source files, and returns orphans (source files with no symlink).
func Scan(cfg *config.Config) (*Report, error) {
	// Step 1: Build the "covered" set from ALL output directories.
	// This catches miniseries (movies_source files linked into tv_linked).
	covered := make(map[string]bool)
	for _, outDir := range cfg.OutputDirs {
		if err := collectCovered(outDir, cfg.HostRoot, cfg.ContainerRoot, covered); err != nil {
			return nil, err
		}
	}

	// Step 2: Walk each source directory.
	var moviesSource []sourceFile
	for _, src := range cfg.MoviesSources {
		moviesSource = append(moviesSource, collectSourceVideos(src)...)
	}
	var tvSource []sourceFile
	for _, src := range cfg.TVSources {
		tvSource = append(tvSource, collectSourceVideos(src)...)
	}

	// Step 3: Compute orphans.
	report := &Report{}

	for _, sf := range moviesSource {
		if covered[sf.path] {
			report.MoviesCovered++
		} else {
			report.MoviesOrphans = append(report.MoviesOrphans, OrphanFile{
				Path:   sf.path,
				Folder: sf.folder,
				Size:   sf.size,
			})
		}
	}

	for _, sf := range tvSource {
		if covered[sf.path] {
			report.TVCovered++
		} else {
			report.TVOrphans = append(report.TVOrphans, OrphanFile{
				Path:   sf.path,
				Folder: sf.folder,
				Size:   sf.size,
			})
		}
	}

	return report, nil
}

// sourceFile is an internal type for source directory walking.
type sourceFile struct {
	path   string
	folder string
	size   int64
}

// collectSourceVideos walks a source directory and returns all video files,
// excluding samples.
func collectSourceVideos(dir string) []sourceFile {
	var files []sourceFile
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !common.IsVideo(d.Name()) {
			return nil
		}
		if common.IsSample(d.Name()) {
			return nil
		}
		var size int64
		if info, err := d.Info(); err == nil {
			size = info.Size()
		}
		folder := filepath.Base(filepath.Dir(path))
		files = append(files, sourceFile{path: path, folder: folder, size: size})
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		if files[i].folder != files[j].folder {
			return files[i].folder < files[j].folder
		}
		return files[i].path < files[j].path
	})
	return files
}

// collectCovered walks an output directory and resolves symlink targets back
// to host paths, adding them to the covered set.
func collectCovered(outDir, hostRoot, containerRoot string, covered map[string]bool) error {
	if _, err := os.Stat(outDir); err != nil {
		return nil // output dir doesn't exist yet, nothing covered
	}

	return filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// Symlink (file or directory): resolve target and collect covered paths.
		// WalkDir does not follow symlinks, so we handle them manually.
		if d.Type()&os.ModeSymlink != 0 {
			handleSymlink(path, hostRoot, containerRoot, covered)
			return nil
		}

		// Real directory: WalkDir descends into it automatically.
		// Regular files (state files, etc.): skip.
		return nil
	})
}

// handleSymlink resolves a symlink at path and adds its target(s) to covered.
func handleSymlink(path, hostRoot, containerRoot string, covered map[string]bool) {
	target, err := os.Readlink(path)
	if err != nil {
		return
	}

	hostTarget := common.ContainerToHost(target, hostRoot, containerRoot)

	info, err := os.Stat(hostTarget)
	if err != nil {
		// Broken symlink — target doesn't exist. Still record it so
		// it doesn't show as an orphan (the source file is gone too).
		covered[hostTarget] = true
		return
	}

	if info.IsDir() {
		// Directory symlink (season folder / passthrough).
		// Walk the resolved target to discover all video files inside.
		filepath.WalkDir(hostTarget, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !de.IsDir() && common.IsVideo(de.Name()) {
				covered[p] = true
			}
			return nil
		})
		return
	}

	// File symlink.
	covered[hostTarget] = true
}
