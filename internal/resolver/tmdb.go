package resolver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/AnAngryGoose/medialnk/internal/common"
)

// Logger is the subset of logging methods the resolver needs.
type Logger interface {
	Verbose(format string, args ...any)
}

// cache holds TMDB results to avoid duplicate API calls across a run.
// A nil value means a confirmed miss (no results / confidence fail).
// Transient request failures are not cached so the next call retries.
var (
	cacheMu sync.Mutex
	cache   = map[string]any{} // value is (tvResult | movieResult | nil)
)

type tvResult struct {
	Title string
	ID    int
}

type movieResult struct {
	Title string
	Year  string
	ID    int
}

// ClearCache resets the global TMDB result cache. Call between test runs.
func ClearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[string]any{}
}

var httpClient = &http.Client{Timeout: 8 * time.Second}

func tmdbGet(endpoint string, query string, apiKey string) ([]byte, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("api_key", apiKey)
	rawURL := fmt.Sprintf("https://api.themoviedb.org/3/%s?%s", endpoint, params.Encode())
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// SearchTV looks up a TV show name on TMDB and returns the canonical title and
// TMDB ID, or ("", 0) if not found / confidence check fails.
// Results are cached; the same query is never sent twice per run.
// Transient request failures are not cached so the next call retries.
func SearchTV(name, apiKey string, confidence bool, log Logger) (string, int) {
	key := "tv:" + name
	cacheMu.Lock()
	if v, ok := cache[key]; ok {
		cacheMu.Unlock()
		if v == nil {
			return "", 0
		}
		r := v.(tvResult)
		return r.Title, r.ID
	}
	cacheMu.Unlock()

	store := func(v any) {
		cacheMu.Lock()
		cache[key] = v
		cacheMu.Unlock()
	}

	if apiKey == "" || len(name) < 3 {
		store(nil)
		return "", 0
	}
	body, err := tmdbGet("search/tv", name, apiKey)
	if err != nil {
		return "", 0 // transient — do not cache
	}
	var resp struct {
		Results []struct {
			Name string `json:"name"`
			ID   int    `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Results) == 0 {
		store(nil)
		return "", 0
	}
	title := common.Sanitize(resp.Results[0].Name)
	id := resp.Results[0].ID
	if confidence && !wordOverlap(name, title) {
		if log != nil {
			log.Verbose("    [TMDB] Rejected: '%s' -> '%s' (low confidence)", name, title)
		}
		store(nil)
		return "", 0
	}
	store(tvResult{Title: title, ID: id})
	return title, id
}

// SearchMovie looks up a movie title on TMDB and returns the canonical
// (title, year, id) tuple, or ("", "", 0) if not found / confidence check fails.
// Results are cached per query.
// Transient request failures are not cached so the next call retries.
func SearchMovie(title, apiKey string, confidence bool, log Logger) (string, string, int) {
	key := "movie:" + title
	cacheMu.Lock()
	if v, ok := cache[key]; ok {
		cacheMu.Unlock()
		if v == nil {
			return "", "", 0
		}
		mr := v.(movieResult)
		return mr.Title, mr.Year, mr.ID
	}
	cacheMu.Unlock()

	store := func(v any) {
		cacheMu.Lock()
		cache[key] = v
		cacheMu.Unlock()
	}

	if apiKey == "" || len(title) < 4 {
		store(nil)
		return "", "", 0
	}
	body, err := tmdbGet("search/movie", title, apiKey)
	if err != nil {
		return "", "", 0 // transient — do not cache
	}
	var resp struct {
		Results []struct {
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
			ID          int    `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Results) == 0 {
		store(nil)
		return "", "", 0
	}
	found := common.Sanitize(resp.Results[0].Title)
	year := ""
	if rd := resp.Results[0].ReleaseDate; len(rd) >= 4 {
		year = rd[:4]
	}
	id := resp.Results[0].ID
	if confidence && !wordOverlap(title, found) {
		if log != nil {
			log.Verbose("    [TMDB] Rejected: '%s' -> '%s' (low confidence)", title, found)
		}
		store(nil)
		return "", "", 0
	}
	store(movieResult{Title: found, Year: year, ID: id})
	return found, year, id
}

// ResolveTVName returns the canonical show name and TMDB ID using
// override → TMDB → parsed fallback. ID is 0 when resolved via override or fallback.
func ResolveTVName(parsed string, overrides map[string]string, apiKey string, confidence bool, log Logger) (string, int) {
	if canonical, ok := overrides[parsed]; ok {
		return canonical, 0
	}
	if canonical, id := SearchTV(parsed, apiKey, confidence, log); canonical != "" {
		return canonical, id
	}
	return parsed, 0
}
