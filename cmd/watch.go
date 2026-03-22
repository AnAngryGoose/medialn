package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/AnAngryGoose/medialnk/internal/common"
	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/health"
	"github.com/AnAngryGoose/medialnk/internal/logger"
	"github.com/AnAngryGoose/medialnk/internal/movies"
	"github.com/AnAngryGoose/medialnk/internal/orphans"
	"github.com/AnAngryGoose/medialnk/internal/state"
	"github.com/AnAngryGoose/medialnk/internal/tv"
	"github.com/AnAngryGoose/medialnk/internal/watch"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch source directories and sync automatically when new content arrives",
	Long: `Runs as a daemon, polling source directories for new content.
When a new download is detected and confirmed complete, triggers
a sync to create symlinks for the new content.

Requires [watch] enabled = true in config.
All interactive prompts are skipped in watch mode; ambiguous
entries are logged for manual review on the next manual sync.`,
	RunE: runWatch,
}

func runWatch(cmd *cobra.Command, args []string) error {
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

	if !cfg.WatchEnabled {
		return fmt.Errorf("watch mode is not enabled in config; set [watch] enabled = true")
	}

	// Logger: single persistent log file for the daemon session.
	ts := time.Now().Format("2006-01-02_15-04-05")
	logFile := fmt.Sprintf("%s/%s_watch.log", cfg.LogDir, ts)

	log, err := logger.New(cfg.Verbosity, logFile)
	if err != nil {
		return err
	}
	defer log.Close()

	log.Normal("medialnk v%s watch", Version)
	log.Normal("Config: %s", cfgPath)
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

	// syncFunc: full idempotent sync in non-interactive mode.
	syncFunc := func() error {
		// Health check before sync.
		if cfg.HealthEnabled {
			results, healthy := health.Check(cfg)
			for _, r := range results {
				if r.Pass {
					log.Verbose("[HEALTH] %s: OK (%d+ video files)", r.Label, r.VideoCount)
				} else {
					log.Normal("[ERROR] Health check failed: %s: %s", r.Label, r.Reason)
				}
			}
			if !healthy {
				return fmt.Errorf("health check failed")
			}
		}

		for _, d := range cfg.OutputDirs {
			if _, err := common.ValidateOutputDir(d, false, true); err != nil {
				return err
			}
		}

		col := state.New()

		movies.Run(cfg, false, true, true, log, col)
		tv.Run(cfg, false, true, true, log, col)

		// Write state files.
		if sp, err := common.NewSafePath(
			filepath.Join(cfg.MoviesLinked, ".medialnk-state.json"), cfg.OutputDirs); err == nil {
			if err := col.WriteMovies(sp, Version); err != nil {
				log.Normal("[WARN] movies state: %v", err)
			}
		}
		if sp, err := common.NewSafePath(
			filepath.Join(cfg.TVLinked, ".medialnk-state.json"), cfg.OutputDirs); err == nil {
			if err := col.WriteTV(sp, Version); err != nil {
				log.Normal("[WARN] tv state: %v", err)
			}
		}

		// Auto-clean broken symlinks.
		if cfg.CleanAfterSync {
			totalCleaned := 0
			for _, d := range cfg.OutputDirs {
				if sp, err := common.NewSafePath(d, cfg.OutputDirs); err == nil {
					if n, err := common.CleanBrokenSymlinks(sp, cfg.HostRoot, cfg.ContainerRoot); err == nil && n > 0 {
						log.Normal("[CLEAN] %s: removed %d broken symlink(s)", d, n)
						totalCleaned += n
					}
				}
			}
			if totalCleaned > 0 {
				log.Normal("[CLEAN] %d broken symlink(s) removed total", totalCleaned)
			}
		}

		// Orphan count.
		if report, err := orphans.Scan(cfg); err == nil {
			if n := report.TotalOrphans(); n > 0 {
				log.Normal("[ORPHANS] %d source files unlinked (%.1f%% coverage)",
					n, report.CoveragePct())
			}
		}

		return nil
	}

	w, err := watch.New(cfg, log, syncFunc)
	if err != nil {
		return fmt.Errorf("watch init failed: %w", err)
	}

	// Optional HTTP health endpoint for Docker HEALTHCHECK / monitoring.
	if cfg.HealthPort > 0 {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/health", func(rw http.ResponseWriter, r *http.Request) {
				last := w.LastPollAt()
				stale := time.Duration(cfg.WatchPollInterval*2) * time.Second
				if time.Since(last) > stale {
					http.Error(rw, "stale", http.StatusServiceUnavailable)
					return
				}
				rw.Write([]byte("ok"))
			})
			addr := fmt.Sprintf(":%d", cfg.HealthPort)
			log.Normal("[WATCH] Health endpoint listening on %s", addr)
			if err := http.ListenAndServe(addr, mux); err != nil {
				log.Normal("[WATCH] [WARN] Health endpoint failed: %v", err)
			}
		}()
	}

	// Graceful shutdown on SIGTERM and SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Normal("[WATCH] Received signal %s, shutting down", sig)
		w.Stop()
	}()

	w.Run()
	return nil
}
