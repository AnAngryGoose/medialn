package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/logger"
	"github.com/AnAngryGoose/medialnk/internal/movies"
	"github.com/AnAngryGoose/medialnk/internal/state"
	"github.com/AnAngryGoose/medialnk/internal/tv"
)

var (
	syncDryRun     bool
	syncYes        bool
	syncTVOnly     bool
	syncMoviesOnly bool
	syncVerbose    int
	syncQuiet      bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Scan source directories and create symlinks",
	RunE:  runSync,
}

func init() {
	syncCmd.Flags().BoolVar(&syncDryRun, "dry-run", false, "Preview only, no writes")
	syncCmd.Flags().BoolVarP(&syncYes, "yes", "y", false, "Auto-accept all prompts")
	syncCmd.Flags().BoolVar(&syncTVOnly, "tv-only", false, "Skip movies pipeline")
	syncCmd.Flags().BoolVar(&syncMoviesOnly, "movies-only", false, "Skip TV pipeline")
	syncCmd.Flags().CountVarP(&syncVerbose, "verbose", "v", "Verbose output (-v or -vv for debug)")
	syncCmd.Flags().BoolVarP(&syncQuiet, "quiet", "q", false, "Quiet: errors and warnings only")
}

func runSync(cmd *cobra.Command, args []string) error {
	cfgPath, err := config.FindConfig(cfgPath)
	if err != nil {
		return err
	}
	if cfgPath == "" {
		return fmt.Errorf("no config file found. Create medialnk.toml or use --config")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// Apply CLI verbosity overrides.
	level := cfg.Verbosity
	switch {
	case syncQuiet:
		level = "quiet"
	case syncVerbose >= 2:
		level = "debug"
	case syncVerbose == 1:
		level = "verbose"
	}

	// Log file: timestamped, not created in dry-run.
	var logFile string
	if !syncDryRun {
		ts := time.Now().Format("2006-01-02_15-04-05")
		logFile = fmt.Sprintf("%s/%s_sync.log", cfg.LogDir, ts)
	}

	log, err := logger.New(level, logFile)
	if err != nil {
		return err
	}
	defer log.Close()

	log.Normal("medialnk v%s sync", Version)
	log.Normal("Config: %s", cfgPath)
	if syncDryRun {
		log.Normal("[DRY RUN]")
	}
	log.Verbose(cfg.Summary())
	log.Normal("")

	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			log.Quiet("[ERROR] %s", e)
		}
		os.Exit(1)
	}

	if err := cfg.ValidatePathGuard(); err != nil {
		log.Quiet("[ERROR] PathGuard: %v", err)
		os.Exit(1)
	}
	log.Verbose("[GUARD] %d source(s), %d output(s)", len(cfg.SourceDirs), len(cfg.OutputDirs))

	for _, d := range cfg.OutputDirs {
		if _, err := common.ValidateOutputDir(d, syncDryRun); err != nil {
			os.Exit(1)
		}
	}

	col := state.New()

	if !syncTVOnly {
		movies.Run(cfg, syncDryRun, syncYes, log, col)
	}
	if !syncMoviesOnly {
		tv.Run(cfg, syncDryRun, syncYes, log, col)
	}

	// Write state files (skip in dry-run).
	if !syncDryRun {
		if !syncTVOnly {
			if sp, err := common.NewSafePath(
				filepath.Join(cfg.MoviesLinked, ".medialnk-state.json"), cfg.OutputDirs); err == nil {
				if err := col.WriteMovies(sp, Version); err != nil {
					log.Normal("[WARN] movies state: %v", err)
				}
			}
		}
		if !syncMoviesOnly {
			if sp, err := common.NewSafePath(
				filepath.Join(cfg.TVLinked, ".medialnk-state.json"), cfg.OutputDirs); err == nil {
				if err := col.WriteTV(sp, Version); err != nil {
					log.Normal("[WARN] tv state: %v", err)
				}
			}
		}
	}

	log.Normal("")
	if syncDryRun {
		log.Normal("Dry run complete.")
	} else {
		log.Normal("Sync complete. Log: %s", logFile)
	}
	return nil
}
