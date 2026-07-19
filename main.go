// Command zauditor audits a repository for how expensive it is to work in
// with limited context — the cost an AI developer (or a new human) pays before
// making a correct change.
//
// The core is static, offline and deterministic: no network, no LLM calls, no
// timestamps in the output. The same tree always produces the same report.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zauditor/config"
	"zauditor/internal/core"
	"zauditor/internal/report"
	"zauditor/internal/score"
	"zauditor/internal/walker"

	// Analyzers register themselves via init(). This blank import is the only
	// place the binary needs to know they exist — adding an analyzer means
	// adding a file to that package, nothing else.
	_ "zauditor/internal/analyzers"
)

const version = "0.1.0"

type flags struct {
	json        bool
	markdown    bool
	minScore    float64
	only        string
	skip        string
	configPath  string
	pluginPath  string
	maxFindings int
	verbose     bool
	noColor     bool
	list        bool
	showVersion bool
	path        string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "zauditor: %v\n", err)
		os.Exit(2)
	}
}

func run(argv []string) error {
	fs, f, err := parseFlags(argv)
	if err != nil {
		return err
	}
	if f.showVersion {
		fmt.Println("zauditor " + version)
		return nil
	}
	if f.list {
		listAnalyzers(os.Stdout)
		return nil
	}
	if f.json && f.markdown {
		return fmt.Errorf("--json and --markdown are mutually exclusive")
	}
	if f.pluginPath != "" {
		// External plugin loading is intentionally not implemented yet; see
		// README "Extending zauditor". Failing loudly beats silently auditing
		// with fewer analyzers than the user asked for.
		return fmt.Errorf("--plugin is reserved for a future release (see README: Extending zauditor)")
	}

	root := f.path
	if root == "" {
		if fs.NArg() > 0 {
			root = fs.Arg(0)
		} else {
			root = "."
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", root, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}

	cfg := config.Default()
	if f.configPath != "" {
		if cfg, err = config.Load(f.configPath); err != nil {
			return err
		}
	}

	chosen, unknown := core.Select(core.Selection{
		Only:     splitList(f.only),
		Skip:     splitList(f.skip),
		Disabled: cfg.Disabled(),
	})
	if len(unknown) > 0 {
		return fmt.Errorf("unknown analyzer(s): %s (available: %s)",
			strings.Join(unknown, ", "), strings.Join(analyzerIDs(), ", "))
	}
	if len(chosen) == 0 {
		return fmt.Errorf("no analyzers selected")
	}

	files, err := walker.Walk(abs, walker.Options{})
	if err != nil {
		return fmt.Errorf("walk %s: %w", root, err)
	}
	ctx := core.NewRepoContext(abs, files, cfg)
	rep := score.Compute(ctx, chosen)

	opts := report.Options{
		Color:       !f.noColor && report.ColorEnabled(os.Stdout),
		MaxFindings: f.maxFindings,
		Verbose:     f.verbose,
	}
	switch {
	case f.json:
		if err := report.JSON(os.Stdout, rep); err != nil {
			return err
		}
	case f.markdown:
		report.Markdown(os.Stdout, rep, opts)
	default:
		report.Terminal(os.Stdout, rep, opts)
	}

	// CI gate: --min-score is compared against the 0..100 score.
	if f.minScore > 0 && rep.Score100 < f.minScore {
		fmt.Fprintf(os.Stderr, "zauditor: score %.1f is below --min-score %.1f\n", rep.Score100, f.minScore)
		os.Exit(1)
	}
	return nil
}

func parseFlags(argv []string) (*flag.FlagSet, flags, error) {
	var f flags
	fs := flag.NewFlagSet("zauditor", flag.ContinueOnError)
	fs.BoolVar(&f.json, "json", false, "machine-readable JSON output")
	fs.BoolVar(&f.markdown, "markdown", false, "markdown report")
	fs.Float64Var(&f.minScore, "min-score", 0, "exit non-zero if the final score (0..100) falls below this")
	fs.StringVar(&f.only, "only", "", "run only these analyzers (comma-separated IDs)")
	fs.StringVar(&f.skip, "skip", "", "skip these analyzers (comma-separated IDs)")
	fs.StringVar(&f.configPath, "config", "", "path to a JSON config overriding weights and thresholds")
	fs.StringVar(&f.pluginPath, "plugin", "", "reserved: path to an external analyzer plugin")
	fs.IntVar(&f.maxFindings, "max-findings", 25, "cap the printed findings (0 = all)")
	fs.BoolVar(&f.verbose, "verbose", false, "include info-level findings and analyzer notes")
	fs.BoolVar(&f.noColor, "no-color", false, "disable ANSI colour")
	fs.BoolVar(&f.list, "list", false, "list registered analyzers and exit")
	fs.BoolVar(&f.showVersion, "version", false, "print version and exit")
	fs.Usage = func() { usage(fs) }
	if err := fs.Parse(argv); err != nil {
		return nil, f, err
	}
	return fs, f, nil
}

func listAnalyzers(w *os.File) {
	fmt.Fprintf(w, "Registered analyzers:\n\n")
	for _, a := range core.All() {
		fmt.Fprintf(w, "  %-12s w=%.1f  %s\n", a.ID(), a.Weight(), a.Name())
		if d, ok := a.(core.Describer); ok {
			fmt.Fprintf(w, "  %-12s        %s\n", "", d.Description())
		}
	}
	fmt.Fprintln(w)
}

func analyzerIDs() []string {
	var ids []string
	for _, a := range core.All() {
		ids = append(ids, a.ID())
	}
	return ids
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func usage(fs *flag.FlagSet) {
	fmt.Fprintf(os.Stderr, `zauditor %s — how expensive is this repo to work in?

Usage:
  zauditor [flags] <path>

Flags:
`, version)
	fs.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Exit codes:
  0  audit completed (and score >= --min-score, if set)
  1  score below --min-score
  2  usage or I/O error
`)
}
