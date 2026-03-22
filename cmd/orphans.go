package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AnAngryGoose/medialnk/internal/config"
	"github.com/AnAngryGoose/medialnk/internal/logger"
	"github.com/AnAngryGoose/medialnk/internal/orphans"
)

var (
	orphansJSON    bool
	orphansQuiet   bool
	orphansVerbose int
)

var orphansCmd = &cobra.Command{
	Use:   "orphans",
	Short: "Report source files with no corresponding symlink",
	RunE:  runOrphans,
}

func init() {
	orphansCmd.Flags().BoolVar(&orphansJSON, "json", false, "Machine-readable JSON output")
	orphansCmd.Flags().BoolVarP(&orphansQuiet, "quiet", "q", false, "Print counts only")
	orphansCmd.Flags().CountVarP(&orphansVerbose, "verbose", "v", "Verbose output")
}

func runOrphans(cmd *cobra.Command, args []string) error {
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

	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Printf("[ERROR] %s\n", e)
		}
		return fmt.Errorf("config validation failed")
	}

	report, err := orphans.Scan(cfg)
	if err != nil {
		return err
	}

	if orphansJSON {
		return printOrphansJSON(report)
	}
	if orphansQuiet {
		return printOrphansQuiet(report)
	}
	return printOrphansHuman(cfg, report)
}

// jsonReport is the wire format for --json output.
type jsonReport struct {
	Movies       jsonPipeline `json:"movies"`
	TV           jsonPipeline `json:"tv"`
	TotalOrphans int          `json:"total_orphans"`
	TotalSource  int          `json:"total_source"`
	CoveragePct  float64      `json:"coverage_pct"`
}

type jsonPipeline struct {
	Orphans []orphans.OrphanFile `json:"orphans"`
	Covered int                  `json:"covered"`
	Total   int                  `json:"total"`
}

func printOrphansJSON(r *orphans.Report) error {
	moviesOrphans := r.MoviesOrphans
	if moviesOrphans == nil {
		moviesOrphans = []orphans.OrphanFile{}
	}
	tvOrphans := r.TVOrphans
	if tvOrphans == nil {
		tvOrphans = []orphans.OrphanFile{}
	}

	jr := jsonReport{
		Movies: jsonPipeline{
			Orphans: moviesOrphans,
			Covered: r.MoviesCovered,
			Total:   r.MoviesCovered + len(r.MoviesOrphans),
		},
		TV: jsonPipeline{
			Orphans: tvOrphans,
			Covered: r.TVCovered,
			Total:   r.TVCovered + len(r.TVOrphans),
		},
		TotalOrphans: r.TotalOrphans(),
		TotalSource:  r.TotalSource(),
		CoveragePct:  r.CoveragePct(),
	}

	enc := json.NewEncoder(cmd_stdout())
	enc.SetIndent("", "  ")
	return enc.Encode(jr)
}

func printOrphansQuiet(r *orphans.Report) error {
	fmt.Printf("Movies: %d orphans / %d source\n", len(r.MoviesOrphans), r.MoviesCovered+len(r.MoviesOrphans))
	fmt.Printf("TV:     %d orphans / %d source\n", len(r.TVOrphans), r.TVCovered+len(r.TVOrphans))
	fmt.Printf("Total:  %d orphans / %d source (%.1f%% coverage)\n",
		r.TotalOrphans(), r.TotalSource(), r.CoveragePct())
	return nil
}

func printOrphansHuman(cfg *config.Config, r *orphans.Report) error {
	log, err := logger.New("normal", "")
	if err != nil {
		return err
	}
	defer log.Close()

	log.Normal("medialnk v%s orphans\n", Version)

	printPipelineOrphans(log, "Movies", cfg.MoviesSources, r.MoviesOrphans, r.MoviesCovered)
	printPipelineOrphans(log, "TV", cfg.TVSources, r.TVOrphans, r.TVCovered)

	log.Normal("Summary: %d orphans / %d source files (%.1f%% coverage)",
		r.TotalOrphans(), r.TotalSource(), r.CoveragePct())

	return nil
}

func printPipelineOrphans(log *logger.Logger, label string, sourceDirs []string, items []orphans.OrphanFile, covered int) {
	total := covered + len(items)
	if len(items) == 0 {
		log.Normal("%s: 0 orphans / %d source files\n", label, total)
		return
	}

	log.Normal("%s (%d orphans):", label, len(items))

	lastFolder := ""
	for _, o := range items {
		rel := o.Path
		for _, sd := range sourceDirs {
			if r, err := filepath.Rel(sd, o.Path); err == nil && !strings.HasPrefix(r, "..") {
				rel = r
				break
			}
		}
		folder := filepath.Dir(rel)
		if folder != lastFolder {
			log.Normal("  %s/", folder)
			lastFolder = folder
		}
		log.Normal("    %s  (%s)", filepath.Base(o.Path), formatSize(o.Size))
	}
	log.Normal("")
}

func formatSize(bytes int64) string {
	const (
		gb = 1024 * 1024 * 1024
		mb = 1024 * 1024
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// cmd_stdout returns os.Stdout. Factored out for potential test overriding.
func cmd_stdout() *os.File {
	return os.Stdout
}
