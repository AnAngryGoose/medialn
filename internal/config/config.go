// Package config handles TOML configuration loading, path resolution, and validation.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// raw TOML shapes — unexported, only used during decode.
type rawPaths struct {
	MediaRootHost      string      `toml:"media_root_host"`
	MediaRootContainer string      `toml:"media_root_container"`
	MoviesSource       interface{} `toml:"movies_source"`
	TVSource           interface{} `toml:"tv_source"`
	MoviesLinked       string      `toml:"movies_linked"`
	TVLinked           string      `toml:"tv_linked"`
}

type rawTMDB struct {
	APIKey          string `toml:"api_key"`
	ConfidenceCheck *bool  `toml:"confidence_check"`
}

type rawLogging struct {
	LogDir    string `toml:"log_dir"`
	Verbosity string `toml:"verbosity"`
}

// OrphanOverride holds the resolved show name and season number for a
// bare "Season N" folder that has no show name context.
type OrphanOverride struct {
	Show   string
	Season int
}

type rawOrphanValue struct {
	Show   string `toml:"show"`
	Season int    `toml:"season"`
}

type rawOverrides struct {
	TVNames     map[string]string          `toml:"tv_names"`
	TVOrphans   map[string]rawOrphanValue  `toml:"tv_orphans"`
	MovieTitles map[string]string          `toml:"movie_titles"`
}

type rawHealth struct {
	Enabled        *bool  `toml:"enabled"`
	MinSourceFiles int    `toml:"min_source_files"`
	SentinelFile   string `toml:"sentinel_file"`
	Port           int    `toml:"port"`
}

type rawWatch struct {
	Enabled             *bool `toml:"enabled"`
	DebounceSeconds     int   `toml:"debounce_seconds"`
	PollIntervalSeconds int   `toml:"poll_interval_seconds"`
}

type rawSync struct {
	CleanAfterSync *bool `toml:"clean_after_sync"`
}

type rawPolicy struct {
	PartN              string `toml:"part_n"`
	DuplicateSeason    string `toml:"duplicate_season"`
	ConflictConversion string `toml:"conflict_conversion"`
}

type rawConfig struct {
	Paths     rawPaths     `toml:"paths"`
	TMDB      rawTMDB      `toml:"tmdb"`
	Logging   rawLogging   `toml:"logging"`
	Overrides rawOverrides `toml:"overrides"`
	Health    rawHealth    `toml:"health"`
	Sync      rawSync      `toml:"sync"`
	Watch     rawWatch     `toml:"watch"`
	Policy    rawPolicy    `toml:"policy"`
}

// Config is the resolved, validated configuration for a medialnk run.
// All paths are absolute.
type Config struct {
	// Roots
	HostRoot      string
	ContainerRoot string

	// Source directories (host absolute paths — read-only)
	MoviesSources []string
	TVSources     []string

	// Output directories (host absolute paths — safe to write via PathGuard)
	MoviesLinked string
	TVLinked     string

	// Convenience slices
	SourceDirs []string
	OutputDirs []string

	// TMDB
	TMDBApiKey    string
	TMDBConfidence bool

	// Logging
	LogDir    string
	Verbosity string // "quiet" | "normal" | "verbose" | "debug"

	// Overrides
	TVNameOverrides    map[string]string
	TVOrphanOverrides  map[string]OrphanOverride
	MovieTitleOverrides map[string]string

	// Health checks
	HealthEnabled      bool
	HealthMinFiles     int
	HealthSentinelFile string
	HealthPort         int // HTTP health endpoint port, 0 = disabled

	// Sync behavior
	CleanAfterSync bool

	// Watch mode
	WatchEnabled      bool
	WatchDebounce     int // seconds, default 30
	WatchPollInterval int // seconds, default 60

	// Policy defaults for prompt behavior
	PolicyPartN              string // "skip" | "prompt" (default: skip)
	PolicyDuplicateSeason    string // "skip" | "prompt" | "highest" (default: skip)
	PolicyConflictConversion string // "auto" | "prompt" (default: auto)
}

func resolve(val, defaultVal, root string) string {
	if val == "" {
		val = defaultVal
	}
	if filepath.IsAbs(val) {
		return val
	}
	return filepath.Join(root, val)
}

// resolveMulti handles a TOML value that may be a single string or an array of strings.
// Returns a slice of resolved absolute paths.
func resolveMulti(val interface{}, defaultVal, root string) []string {
	switch v := val.(type) {
	case string:
		return []string{resolve(v, defaultVal, root)}
	case []interface{}:
		if len(v) == 0 {
			return []string{resolve("", defaultVal, root)}
		}
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = resolve(fmt.Sprintf("%v", item), defaultVal, root)
		}
		return out
	default:
		return []string{resolve("", defaultVal, root)}
	}
}

// Load reads a TOML config file and returns a fully resolved Config.
func Load(path string) (*Config, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	hostRoot := raw.Paths.MediaRootHost
	if hostRoot == "" {
		return nil, fmt.Errorf("paths.media_root_host is required")
	}

	containerRoot := raw.Paths.MediaRootContainer
	if containerRoot == "" {
		containerRoot = hostRoot
	}

	cfg := &Config{
		HostRoot:      hostRoot,
		ContainerRoot: containerRoot,
		MoviesSources: resolveMulti(raw.Paths.MoviesSource, "movies", hostRoot),
		TVSources:     resolveMulti(raw.Paths.TVSource, "tv", hostRoot),
		MoviesLinked:  resolve(raw.Paths.MoviesLinked, "movies-linked", hostRoot),
		TVLinked:      resolve(raw.Paths.TVLinked, "tv-linked", hostRoot),
	}
	cfg.SourceDirs = append(cfg.MoviesSources, cfg.TVSources...)
	cfg.OutputDirs = []string{cfg.MoviesLinked, cfg.TVLinked}

	// TMDB: env var takes priority over config file
	cfg.TMDBApiKey = os.Getenv("TMDB_API_KEY")
	if cfg.TMDBApiKey == "" {
		cfg.TMDBApiKey = raw.TMDB.APIKey
	}
	cfg.TMDBConfidence = true // default
	if raw.TMDB.ConfidenceCheck != nil {
		cfg.TMDBConfidence = *raw.TMDB.ConfidenceCheck
	}

	// Logging
	logDir := raw.Logging.LogDir
	if logDir == "" {
		logDir = "logs"
	}
	cfg.LogDir = resolve(logDir, "logs", hostRoot)
	cfg.Verbosity = raw.Logging.Verbosity
	if cfg.Verbosity == "" {
		cfg.Verbosity = "normal"
	}

	// Overrides
	cfg.TVNameOverrides = raw.Overrides.TVNames
	if cfg.TVNameOverrides == nil {
		cfg.TVNameOverrides = map[string]string{}
	}
	cfg.TVOrphanOverrides = make(map[string]OrphanOverride, len(raw.Overrides.TVOrphans))
	for k, v := range raw.Overrides.TVOrphans {
		cfg.TVOrphanOverrides[k] = OrphanOverride{Show: v.Show, Season: v.Season}
	}
	cfg.MovieTitleOverrides = raw.Overrides.MovieTitles
	if cfg.MovieTitleOverrides == nil {
		cfg.MovieTitleOverrides = map[string]string{}
	}

	// Health checks — default enabled with 10-file minimum.
	cfg.HealthEnabled = true
	if raw.Health.Enabled != nil {
		cfg.HealthEnabled = *raw.Health.Enabled
	}
	cfg.HealthMinFiles = 10
	if raw.Health.MinSourceFiles > 0 {
		cfg.HealthMinFiles = raw.Health.MinSourceFiles
	}
	cfg.HealthSentinelFile = raw.Health.SentinelFile
	cfg.HealthPort = raw.Health.Port

	// Watch mode — disabled by default, must be explicitly enabled.
	if raw.Watch.Enabled != nil {
		cfg.WatchEnabled = *raw.Watch.Enabled
	}
	cfg.WatchDebounce = 30
	if raw.Watch.DebounceSeconds > 0 {
		cfg.WatchDebounce = raw.Watch.DebounceSeconds
	}
	cfg.WatchPollInterval = 60
	if raw.Watch.PollIntervalSeconds > 0 {
		cfg.WatchPollInterval = raw.Watch.PollIntervalSeconds
	}

	// Sync behavior — clean_after_sync defaults to false.
	if raw.Sync.CleanAfterSync != nil {
		cfg.CleanAfterSync = *raw.Sync.CleanAfterSync
	}

	// Policy defaults — non-blocking defaults for unattended operation.
	cfg.PolicyPartN = "skip"
	if raw.Policy.PartN == "prompt" {
		cfg.PolicyPartN = "prompt"
	}
	cfg.PolicyDuplicateSeason = "skip"
	switch raw.Policy.DuplicateSeason {
	case "prompt":
		cfg.PolicyDuplicateSeason = "prompt"
	case "highest":
		cfg.PolicyDuplicateSeason = "highest"
	}
	cfg.PolicyConflictConversion = "auto"
	if raw.Policy.ConflictConversion == "prompt" {
		cfg.PolicyConflictConversion = "prompt"
	}

	return cfg, nil
}

// Validate checks that required directories exist.
// Returns a slice of error strings (empty means valid).
func (c *Config) Validate() []string {
	var errs []string
	if info, err := os.Stat(c.HostRoot); err != nil || !info.IsDir() {
		errs = append(errs, fmt.Sprintf("media_root_host not found: %s", c.HostRoot))
	}
	for _, src := range c.MoviesSources {
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("movies_source not found: %s", src))
		}
	}
	for _, src := range c.TVSources {
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("tv_source not found: %s", src))
		}
	}
	return errs
}

// Summary returns a human-readable description of the resolved config.
func (c *Config) Summary() string {
	tmdbStatus := "not set"
	if c.TMDBApiKey != "" {
		tmdbStatus = "set"
	}
	healthStatus := "enabled"
	if !c.HealthEnabled {
		healthStatus = "disabled"
	}
	moviesSrc := strings.Join(c.MoviesSources, ", ")
	tvSrc := strings.Join(c.TVSources, ", ")
	return fmt.Sprintf(
		"  Host root:      %s\n"+
			"  Container root: %s\n"+
			"  Movies source:  %s\n"+
			"  TV source:      %s\n"+
			"  Movies linked:  %s\n"+
			"  TV linked:      %s\n"+
			"  TMDB key:       %s\n"+
			"  TV overrides:   %d names, %d orphans\n"+
			"  Health checks:  %s (min %d files)",
		c.HostRoot, c.ContainerRoot,
		moviesSrc, tvSrc,
		c.MoviesLinked, c.TVLinked,
		tmdbStatus,
		len(c.TVNameOverrides), len(c.TVOrphanOverrides),
		healthStatus, c.HealthMinFiles,
	)
}

// ValidatePathGuard checks that no output directory is inside a source directory.
// Returns an error describing the conflict if one is found.
func (c *Config) ValidatePathGuard() error {
	for _, output := range c.OutputDirs {
		for _, source := range c.SourceDirs {
			if output == source || strings.HasPrefix(output, source+string(os.PathSeparator)) {
				return fmt.Errorf("output inside source.\n  Out: %s\n  Src: %s", output, source)
			}
		}
	}
	return nil
}

// FindConfig searches for a config file in the standard locations.
// Returns the path if found, empty string if not found, or an error if a
// CLI-specified path doesn't exist.
func FindConfig(cliPath string) (string, error) {
	if cliPath != "" {
		if _, err := os.Stat(cliPath); err != nil {
			return "", fmt.Errorf("config not found: %s", cliPath)
		}
		return cliPath, nil
	}
	candidates := []string{
		filepath.Join(mustCwd(), "medialnk.toml"),
		filepath.Join(homeDir(), ".config", "medialnk", "medialnk.toml"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", nil
}

func mustCwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
