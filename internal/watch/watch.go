// Package watch implements a poll-based filesystem watcher that detects new
// content in source directories and triggers sync. Polling is used instead of
// inotify because the primary deployment target (MergerFS) is FUSE and does
// not reliably propagate inotify events.
//
// This package is read-only on source directories. It stats files and reads
// directory listings. It never writes to source paths.
package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/health"
)

// Log is the subset of logger methods the watcher uses.
type Log interface {
	Normal(format string, args ...any)
	Verbose(format string, args ...any)
}

// Entry tracks a detected new item in a source directory.
type Entry struct {
	Path         string
	Pipeline     string // "movies" or "tv"
	DetectedAt   time.Time
	LastSize     int64
	StableChecks int
}

// Watcher polls source directories and triggers sync when new stable
// content is detected.
type Watcher struct {
	cfg             *config.Config
	log             Log
	debounce        time.Duration
	pollInterval    time.Duration
	localRoot       string // whichever of HostRoot/ContainerRoot is accessible
	pending         map[string]*Entry
	knownPaths      map[string]bool
	syncFunc        func() error
	stop            chan struct{}
	lastPollAt      atomic.Value // stores time.Time
}

// New creates a Watcher. syncFunc is called when stable new content is ready
// to be processed. It runs a full idempotent sync in non-interactive mode.
//
// localRoot is auto-detected: whichever of HostRoot or ContainerRoot is
// actually accessible to this process. This handles both bare-metal (systemd)
// and Docker deployments without config changes.
func New(cfg *config.Config, log Log, syncFunc func() error) (*Watcher, error) {
	root, err := detectLocalRoot(cfg)
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		cfg:          cfg,
		log:          log,
		localRoot:    root,
		debounce:     time.Duration(cfg.WatchDebounce) * time.Second,
		pollInterval: time.Duration(cfg.WatchPollInterval) * time.Second,
		pending:      make(map[string]*Entry),
		knownPaths:   make(map[string]bool),
		syncFunc:     syncFunc,
		stop:         make(chan struct{}),
	}
	w.lastPollAt.Store(time.Now())
	return w, nil
}

// detectLocalRoot figures out which root path this process can actually reach.
// On the host (systemd), that is HostRoot. Inside a Docker container, that is
// ContainerRoot. If both are accessible (same path), HostRoot wins.
func detectLocalRoot(cfg *config.Config) (string, error) {
	if info, err := os.Stat(cfg.HostRoot); err == nil && info.IsDir() {
		return cfg.HostRoot, nil
	}
	if info, err := os.Stat(cfg.ContainerRoot); err == nil && info.IsDir() {
		return cfg.ContainerRoot, nil
	}
	return "", &RootError{Host: cfg.HostRoot, Container: cfg.ContainerRoot}
}

// RootError is returned when neither media root is accessible.
type RootError struct {
	Host      string
	Container string
}

func (e *RootError) Error() string {
	return "neither media_root_host (" + e.Host + ") nor media_root_container (" + e.Container + ") is accessible"
}

// LastPollAt returns the time of the last successful poll.
// Used by the optional health check endpoint.
func (w *Watcher) LastPollAt() time.Time {
	return w.lastPollAt.Load().(time.Time)
}

// Run starts the poll loop. Blocks until Stop() is called or the stop
// channel is closed.
func (w *Watcher) Run() {
	w.log.Normal("[WATCH] Starting watch mode, polling every %s, debounce %s",
		w.pollInterval, w.debounce)
	w.log.Normal("[WATCH] Local root: %s", w.localRoot)

	// Initial snapshot so existing content is not treated as "new."
	w.snapshot()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			w.log.Normal("[WATCH] Stopping watch mode")
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

// Stop signals the watcher to shut down.
func (w *Watcher) Stop() {
	close(w.stop)
}

// localPath translates a config-resolved absolute path (relative to HostRoot)
// into a path accessible from this process (relative to localRoot). When
// localRoot == HostRoot, this is a no-op.
func (w *Watcher) localPath(cfgAbsPath string) string {
	rel := strings.TrimPrefix(cfgAbsPath, w.cfg.HostRoot)
	return filepath.Join(w.localRoot, rel)
}

// sourceDirs returns the locally-accessible paths of source directories.
func (w *Watcher) sourceDirs() []string {
	return []string{
		w.localPath(w.cfg.MoviesSource),
		w.localPath(w.cfg.TVSource),
	}
}

// pipelineForDir returns "movies" or "tv" based on the source directory.
func (w *Watcher) pipelineForDir(dir string) string {
	if dir == w.localPath(w.cfg.MoviesSource) {
		return "movies"
	}
	return "tv"
}

// snapshot records the current contents of source directories without
// triggering sync. Used on startup so existing content is not treated as new.
func (w *Watcher) snapshot() {
	for _, dir := range w.sourceDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			w.log.Normal("[WATCH] [WARN] Cannot read %s: %v", dir, err)
			continue
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			w.knownPaths[full] = true
		}
	}
	w.log.Verbose("[WATCH] Initial snapshot: %d known entries", len(w.knownPaths))
}

// poll checks source directories for new entries, manages debounce timers,
// runs stability checks, and triggers sync when entries are ready.
func (w *Watcher) poll() {
	w.lastPollAt.Store(time.Now())

	// Detect new entries.
	for _, dir := range w.sourceDirs() {
		pipeline := w.pipelineForDir(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			w.log.Normal("[WATCH] [WARN] Cannot read %s: %v", dir, err)
			continue
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if w.knownPaths[full] {
				continue
			}
			if _, pending := w.pending[full]; pending {
				continue
			}
			w.log.Normal("[WATCH] New entry detected: %s (%s)", e.Name(), pipeline)
			w.pending[full] = &Entry{
				Path:       full,
				Pipeline:   pipeline,
				DetectedAt: time.Now(),
			}
		}
	}

	// Check pending entries for readiness.
	var ready []string
	for path, entry := range w.pending {
		elapsed := time.Since(entry.DetectedAt)
		if elapsed < w.debounce {
			continue
		}

		// Check for incomplete download markers.
		if hasIncompleteMarkers(path) {
			w.log.Verbose("[WATCH] Incomplete markers found in %s, waiting", filepath.Base(path))
			entry.DetectedAt = time.Now() // reset debounce
			continue
		}

		// File stability check.
		currentSize := totalSize(path)
		if currentSize != entry.LastSize {
			w.log.Verbose("[WATCH] Size changed for %s (%d -> %d), resetting debounce",
				filepath.Base(path), entry.LastSize, currentSize)
			entry.LastSize = currentSize
			entry.DetectedAt = time.Now()
			continue
		}

		// Entry is stable and past debounce.
		entry.StableChecks++
		if entry.StableChecks >= 2 {
			ready = append(ready, path)
		}
	}

	if len(ready) == 0 {
		return
	}

	// Health check before sync.
	_, healthy := health.Check(w.cfg)
	if !healthy {
		w.log.Normal("[WATCH] [ERROR] Health check failed. Skipping sync.")
		return
	}

	// Trigger sync.
	w.log.Normal("[WATCH] Triggering sync for %d new entries", len(ready))
	for _, p := range ready {
		w.log.Normal("[WATCH]   %s", filepath.Base(p))
	}
	if err := w.syncFunc(); err != nil {
		w.log.Normal("[WATCH] [ERROR] Sync failed: %v", err)
		// Do not mark as known on failure so they retry next cycle.
		return
	}

	// Mark processed entries as known.
	for _, path := range ready {
		w.knownPaths[path] = true
		delete(w.pending, path)
	}
}

// totalSize returns the total byte size of a path. For directories, it sums
// all files recursively. For single files, it returns the file size.
// This function is read-only.
func totalSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// hasIncompleteMarkers checks for known download client incomplete file
// indicators within a path. Returns true if any marker is found.
// This function is read-only.
func hasIncompleteMarkers(path string) bool {
	suffixes := []string{".!qB", ".part", ".aria2"}
	prefixes := []string{"_UNPACK_"}
	found := false
	filepath.Walk(path, func(name string, info os.FileInfo, err error) error {
		if err != nil || found {
			return nil
		}
		base := filepath.Base(name)
		for _, s := range suffixes {
			if strings.HasSuffix(base, s) {
				found = true
				return nil
			}
		}
		for _, p := range prefixes {
			if strings.HasPrefix(base, p) {
				found = true
				return nil
			}
		}
		return nil
	})
	return found
}
