package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a medialnk.toml config file interactively",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	if !common.IsTerminal() {
		return fmt.Errorf("medialnk init requires an interactive terminal")
	}

	scanner := bufio.NewScanner(os.Stdin)
	prompt := func(msg, defaultVal string) string {
		if defaultVal != "" {
			fmt.Printf("%s [%s]: ", msg, defaultVal)
		} else {
			fmt.Printf("%s: ", msg)
		}
		if !scanner.Scan() {
			return defaultVal
		}
		val := strings.TrimSpace(scanner.Text())
		if val == "" {
			return defaultVal
		}
		return val
	}

	// Step 1: Media root.
	mediaRoot := prompt("Where are your media files?", "")
	if mediaRoot == "" {
		return fmt.Errorf("media root path is required")
	}
	mediaRoot, _ = filepath.Abs(mediaRoot)
	if info, err := os.Stat(mediaRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("directory not found: %s", mediaRoot)
	}

	// Step 2: Auto-detect source directories.
	moviesSource := "movies"
	tvSource := "tv"

	moviesPath := filepath.Join(mediaRoot, "movies")
	tvPath := filepath.Join(mediaRoot, "tv")

	moviesFound := dirExists(moviesPath)
	tvFound := dirExists(tvPath)

	if moviesFound {
		n := countVideosQuick(moviesPath, 500)
		fmt.Printf("  Found: %s (%d video files)\n", moviesPath, n)
	}
	if tvFound {
		n := countVideosQuick(tvPath, 2000)
		fmt.Printf("  Found: %s (%d video files)\n", tvPath, n)
	}

	if !moviesFound {
		moviesSource = prompt("Movies source directory (relative to media root)", "movies")
		mp := filepath.Join(mediaRoot, moviesSource)
		if !dirExists(mp) {
			fmt.Printf("  [WARN] Directory does not exist: %s\n", mp)
			fmt.Printf("  It will be created when you first run medialnk sync.\n")
		}
	}
	if !tvFound {
		tvSource = prompt("TV source directory (relative to media root)", "tv")
		tp := filepath.Join(mediaRoot, tvSource)
		if !dirExists(tp) {
			fmt.Printf("  [WARN] Directory does not exist: %s\n", tp)
			fmt.Printf("  It will be created when you first run medialnk sync.\n")
		}
	}

	// Step 3: Output root.
	defaultOut := mediaRoot + "-linked"
	outputRoot := prompt("Where should the linked library go?", defaultOut)
	outputRoot, _ = filepath.Abs(outputRoot)

	moviesLinked := filepath.Join(outputRoot, "movies")
	tvLinked := filepath.Join(outputRoot, "tv")

	// Step 4: TMDB API key (optional).
	tmdbKey := prompt("TMDB API key (optional, press enter to skip)", "")

	// Step 5: Choose config output path.
	var configPath string
	homeConfig := filepath.Join(homeDir(), ".config", "medialnk", "medialnk.toml")
	cwdConfig := filepath.Join(mustCwd(), "medialnk.toml")

	fmt.Println()
	fmt.Println("Where should the config file be saved?")
	fmt.Printf("  [1] %s\n", homeConfig)
	fmt.Printf("  [2] %s\n", cwdConfig)
	fmt.Println("  [3] Custom path")
	choice := common.PromptChoice("  Choice: ", []string{"1", "2", "3"})
	switch choice {
	case "1":
		configPath = homeConfig
	case "2":
		configPath = cwdConfig
	case "3":
		configPath = prompt("Config file path", homeConfig)
	}

	// Check if config already exists.
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("\n  Config already exists at %s\n", configPath)
		overwrite := common.PromptChoice("  Overwrite? [y/n]: ", []string{"y", "n"})
		if overwrite != "y" {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	// Step 6: Write config.
	tomlContent := buildTOML(mediaRoot, moviesSource, tvSource, moviesLinked, tvLinked, tmdbKey)

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("cannot create config directory: %w", err)
	}
	if err := os.WriteFile(configPath, []byte(tomlContent), 0o644); err != nil {
		return fmt.Errorf("cannot write config: %w", err)
	}

	// Step 7: Validate by loading it back.
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Printf("\n  [WARN] Config written but has errors: %v\n", err)
		fmt.Printf("  Edit %s to fix.\n", configPath)
		return nil
	}

	fmt.Printf("\nWrote %s\n", configPath)
	fmt.Printf("  movies source: %s\n", strings.Join(cfg.MoviesSources, ", "))
	fmt.Printf("  tv source:     %s\n", strings.Join(cfg.TVSources, ", "))
	fmt.Printf("  movies output: %s\n", cfg.MoviesLinked)
	fmt.Printf("  tv output:     %s\n", cfg.TVLinked)
	if tmdbKey != "" {
		fmt.Println("  TMDB key:      set")
	}
	fmt.Printf("\nRun: medialnk sync --dry-run\n")

	return nil
}

func buildTOML(mediaRoot, moviesSource, tvSource, moviesLinked, tvLinked, tmdbKey string) string {
	var b strings.Builder

	b.WriteString("[paths]\n")
	fmt.Fprintf(&b, "media_root_host = %q\n", mediaRoot)
	fmt.Fprintf(&b, "movies_source   = %q\n", moviesSource)
	fmt.Fprintf(&b, "tv_source       = %q\n", tvSource)
	fmt.Fprintf(&b, "movies_linked   = %q\n", moviesLinked)
	fmt.Fprintf(&b, "tv_linked       = %q\n", tvLinked)

	b.WriteString("\n[tmdb]\n")
	fmt.Fprintf(&b, "api_key = %q\n", tmdbKey)

	b.WriteString("\n[policy]\n")
	b.WriteString("part_n              = \"skip\"\n")
	b.WriteString("duplicate_season    = \"skip\"\n")
	b.WriteString("conflict_conversion = \"auto\"\n")

	b.WriteString("\n[health]\n")
	b.WriteString("enabled         = true\n")
	b.WriteString("min_source_files = 10\n")

	b.WriteString("\n[sync]\n")
	b.WriteString("clean_after_sync = false\n")

	b.WriteString("\n[logging]\n")
	b.WriteString("verbosity = \"normal\"\n")

	return b.String()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func countVideosQuick(dir string, limit int) int {
	count := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
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

func mustCwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
