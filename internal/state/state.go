// Package state provides sync run state tracking for medialnk.
// A nil-safe Collector is threaded through the movie and TV pipelines,
// recording every link, skip, flag, and unmatched entry. After both
// pipelines complete, the collected state is written to a hidden JSON
// file in each output directory.
package state

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/AnAngryGoose/medialnk/internal/common"
)

// Entry represents a single linked or skipped item in the state file.
type Entry struct {
	Type           string `json:"type"`                      // "movie" | "tv_season" | "tv_episode"
	Title          string `json:"title,omitempty"`            // movie title
	Year           string `json:"year,omitempty"`             // movie year
	Show           string `json:"show,omitempty"`             // TV show name
	Season         int    `json:"season,omitempty"`            // TV season number
	Episode        int    `json:"episode,omitempty"`           // TV episode number
	SecondEp       *int   `json:"second_ep,omitempty"`         // multi-ep second episode; nil = single
	Quality        string `json:"quality,omitempty"`           // detected quality tag
	SourcePath     string `json:"source_path"`                 // absolute source file/folder path
	LinkPath       string `json:"link_path"`                   // absolute symlink path in output
	LinkedAt       string `json:"linked_at,omitempty"`         // RFC3339 timestamp; only on linked entries
	TMDBUnverified bool   `json:"tmdb_unverified,omitempty"`   // linked with parsed name, TMDB not confirmed
}

// FlaggedEntry represents a source entry that could not be parsed or routed.
type FlaggedEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// RunMeta holds metadata about the sync run.
type RunMeta struct {
	Version     string `json:"version"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	DurationMs  int64  `json:"duration_ms"`
	DryRun      bool   `json:"dry_run"`
}

// pipelineState is the top-level structure written to each state file.
type pipelineState struct {
	Run       RunMeta        `json:"run"`
	Linked    []Entry        `json:"linked"`
	Skipped   []Entry        `json:"skipped"`
	Flagged   []FlaggedEntry `json:"flagged,omitempty"`
	Unmatched []string       `json:"unmatched,omitempty"`
}

// Collector accumulates state from both pipelines during a sync run.
// All Record methods are nil-safe — they no-op on a nil receiver.
type Collector struct {
	mu        sync.Mutex
	movies    pipelineState
	tv        pipelineState
	startedAt time.Time
}

// New creates a new Collector with the current time as the run start.
func New() *Collector {
	return &Collector{startedAt: time.Now().UTC()}
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// Movie recording
// ---------------------------------------------------------------------------

// RecordMovieLink records a newly created movie symlink.
func (c *Collector) RecordMovieLink(title, year, quality, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.movies.Linked = append(c.movies.Linked, Entry{
		Type:       "movie",
		Title:      title,
		Year:       year,
		Quality:    quality,
		SourcePath: src,
		LinkPath:   link,
		LinkedAt:   now(),
	})
}

// RecordMovieLinkUnverified records a movie symlink created without TMDB confirmation.
func (c *Collector) RecordMovieLinkUnverified(title, year, quality, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.movies.Linked = append(c.movies.Linked, Entry{
		Type:           "movie",
		Title:          title,
		Year:           year,
		Quality:        quality,
		SourcePath:     src,
		LinkPath:       link,
		LinkedAt:       now(),
		TMDBUnverified: true,
	})
}

// RecordMovieSkip records a movie symlink that already existed (skip).
func (c *Collector) RecordMovieSkip(title, year, quality, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.movies.Skipped = append(c.movies.Skipped, Entry{
		Type:       "movie",
		Title:      title,
		Year:       year,
		Quality:    quality,
		SourcePath: src,
		LinkPath:   link,
	})
}

// RecordMovieFlagged records a source entry that could not be parsed.
func (c *Collector) RecordMovieFlagged(name, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.movies.Flagged = append(c.movies.Flagged, FlaggedEntry{Name: name, Reason: reason})
}

// RecordMovieUnmatched records a single yearless entry that TMDB could not resolve.
func (c *Collector) RecordMovieUnmatched(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.movies.Unmatched = append(c.movies.Unmatched, name)
}

// ---------------------------------------------------------------------------
// TV recording
// ---------------------------------------------------------------------------

// RecordTVSeasonLink records a newly created TV season folder symlink.
func (c *Collector) RecordTVSeasonLink(show string, season int, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tv.Linked = append(c.tv.Linked, Entry{
		Type:       "tv_season",
		Show:       show,
		Season:     season,
		SourcePath: src,
		LinkPath:   link,
		LinkedAt:   now(),
	})
}

// RecordTVSeasonSkip records a TV season symlink that already existed.
func (c *Collector) RecordTVSeasonSkip(show string, season int, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tv.Skipped = append(c.tv.Skipped, Entry{
		Type:       "tv_season",
		Show:       show,
		Season:     season,
		SourcePath: src,
		LinkPath:   link,
	})
}

// RecordTVEpisodeLink records a newly created TV episode symlink.
func (c *Collector) RecordTVEpisodeLink(show string, season, episode int, secondEp *int, quality, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tv.Linked = append(c.tv.Linked, Entry{
		Type:       "tv_episode",
		Show:       show,
		Season:     season,
		Episode:    episode,
		SecondEp:   secondEp,
		Quality:    quality,
		SourcePath: src,
		LinkPath:   link,
		LinkedAt:   now(),
	})
}

// RecordTVEpisodeSkip records a TV episode symlink that already existed.
func (c *Collector) RecordTVEpisodeSkip(show string, season, episode int, secondEp *int, quality, src, link string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tv.Skipped = append(c.tv.Skipped, Entry{
		Type:       "tv_episode",
		Show:       show,
		Season:     season,
		Episode:    episode,
		SecondEp:   secondEp,
		Quality:    quality,
		SourcePath: src,
		LinkPath:   link,
	})
}

// RecordTVUnmatched records bare episode filenames that could not be matched.
func (c *Collector) RecordTVUnmatched(names []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tv.Unmatched = append(c.tv.Unmatched, names...)
}

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------

// SummaryData holds aggregate counts from both pipelines for display.
type SummaryData struct {
	MoviesLinked    int
	MoviesSkipped   int
	MoviesFlagged   int
	MoviesUnmatched int
	TVLinked        int
	TVSkipped       int
	TVUnmatched     int
	TMDBUnverified  int
	Flagged         []FlaggedEntry
	Unmatched       []string
}

// Summary returns aggregate counts from both pipelines.
func (c *Collector) Summary() SummaryData {
	if c == nil {
		return SummaryData{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var flagged []FlaggedEntry
	flagged = append(flagged, c.movies.Flagged...)

	var unmatched []string
	unmatched = append(unmatched, c.movies.Unmatched...)
	unmatched = append(unmatched, c.tv.Unmatched...)

	unverified := 0
	for _, e := range c.movies.Linked {
		if e.TMDBUnverified {
			unverified++
		}
	}
	for _, e := range c.tv.Linked {
		if e.TMDBUnverified {
			unverified++
		}
	}

	return SummaryData{
		MoviesLinked:    len(c.movies.Linked),
		MoviesSkipped:   len(c.movies.Skipped),
		MoviesFlagged:   len(c.movies.Flagged),
		MoviesUnmatched: len(c.movies.Unmatched),
		TVLinked:        len(c.tv.Linked),
		TVSkipped:       len(c.tv.Skipped),
		TVUnmatched:     len(c.tv.Unmatched),
		TMDBUnverified:  unverified,
		Flagged:         flagged,
		Unmatched:       unmatched,
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

// finalize sets the completion metadata on a pipelineState.
func (c *Collector) finalize(ps *pipelineState, version string) {
	end := time.Now().UTC()
	ps.Run = RunMeta{
		Version:     version,
		StartedAt:   c.startedAt.Format(time.RFC3339),
		CompletedAt: end.Format(time.RFC3339),
		DurationMs:  end.Sub(c.startedAt).Milliseconds(),
		DryRun:      false,
	}
	if ps.Linked == nil {
		ps.Linked = []Entry{}
	}
	if ps.Skipped == nil {
		ps.Skipped = []Entry{}
	}
}

// WriteMovies writes the movies pipeline state to the given SafePath.
func (c *Collector) WriteMovies(path common.SafePath, version string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalize(&c.movies, version)
	data, err := json.MarshalIndent(c.movies, "", "  ")
	if err != nil {
		return err
	}
	return common.WriteFile(path, data, 0o644)
}

// WriteTV writes the TV pipeline state to the given SafePath.
func (c *Collector) WriteTV(path common.SafePath, version string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalize(&c.tv, version)
	data, err := json.MarshalIndent(c.tv, "", "  ")
	if err != nil {
		return err
	}
	return common.WriteFile(path, data, 0o644)
}
