package analyzers

import (
	"fmt"
	"time"

	"zauditor/internal/core"
)

func init() { core.Register(&docsAnalyzer{}) }

// docsAnalyzer is a SKELETON. The presence checks below are real; the
// freshness heuristic is deliberately minimal and marked with TODOs.
type docsAnalyzer struct{}

func (a *docsAnalyzer) ID() string      { return "docs" }
func (a *docsAnalyzer) Name() string    { return "Docs & agent instructions" }
func (a *docsAnalyzer) Weight() float64 { return 1.5 }

func (a *docsAnalyzer) Description() string {
	return "Whether the repo explains itself: what it is, how it is structured, and what an agent working here must know."
}

// docSignal mirrors tooling's signal shape. TODO: once a third analyzer needs
// it, lift this into a shared internal/analyzers/signal.go helper.
type docSignal struct {
	title    string
	weight   float64
	severity core.Severity
	paths    []string
	fix      string
}

func (a *docsAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	if len(ctx.SourceFiles()) == 0 {
		return core.Skip("no source files found")
	}

	sigs := []docSignal{
		{
			title: "README", weight: 2, severity: core.SeverityCritical,
			paths: []string{"README.md", "README.rst", "README.txt", "readme.md", "docs/README.md"},
			fix:   "Add a README that answers three questions in the first screen: what this is, how to run it, how to test it.",
		},
		{
			title: "architecture overview", weight: 1.5, severity: core.SeverityWarn,
			paths: []string{"ARCHITECTURE.md", "docs/architecture.md", "docs/ARCHITECTURE.md", "docs/design.md"},
			fix:   "Add ARCHITECTURE.md naming the top-level modules and how a request flows through them. This is the single highest-leverage document for anyone loading the repo into context.",
		},
		{
			title: "contribution guide", weight: 0.5, severity: core.SeverityInfo,
			paths: []string{"CONTRIBUTING.md", "docs/CONTRIBUTING.md", ".github/CONTRIBUTING.md"},
			fix:   "Add CONTRIBUTING.md with the local setup and the definition of done.",
		},
		{
			title: "agent instructions", weight: 1.5, severity: core.SeverityWarn,
			paths: []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", ".cursor/rules", ".github/copilot-instructions.md", "GEMINI.md"},
			fix:   "Add CLAUDE.md/AGENTS.md with the conventions an agent cannot infer: which commands to run, which directories are generated, what must never be edited by hand.",
		},
	}

	res := core.DimensionResult{}
	var total, got float64
	var newestDoc time.Time
	var newestDocPath string

	for _, s := range sigs {
		total += s.weight
		path, ok := ctx.HasAny(s.paths...)
		if !ok {
			res.Findings = append(res.Findings, core.Finding{
				Severity: s.severity,
				Path:     ".",
				Message:  "Missing: " + s.title,
				Fix:      s.fix,
			})
			continue
		}
		got += s.weight
		if f, _ := ctx.Lookup(path); f != nil && f.ModTime.After(newestDoc) {
			newestDoc, newestDocPath = f.ModTime, f.Path
		}
	}

	// Freshness heuristic (minimal version): compare the newest documentation
	// mtime with the newest source mtime.
	//
	// TODO: mtime is unreliable on fresh clones — every file gets the checkout
	// time. Prefer `git log -1 --format=%ct <path>` when a .git directory is
	// present, and fall back to mtime only outside a repo.
	// TODO: compare per-area (docs/api.md vs src/api/) instead of repo-wide, so
	// one refreshed README does not mask a stale architecture doc.
	// TODO: detect README sections that reference paths which no longer exist —
	// the strongest available signal that docs have drifted.
	stalePenalty := 0.0
	if !newestDoc.IsZero() && !ctx.NewestCodeMod.IsZero() {
		lag := ctx.NewestCodeMod.Sub(newestDoc)
		limit := time.Duration(ctx.Config.DocsStaleDays) * 24 * time.Hour
		if lag > limit {
			days := int(lag.Hours() / 24)
			stalePenalty = 0.25
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     newestDocPath,
				Message:  fmt.Sprintf("Documentation lags the code by ~%d days", days),
				Fix:      "Re-read the docs against the current code and update them in the same commit as the next feature. Stale docs are worse than none: they are confidently wrong context.",
			})
		}
	}

	if total > 0 {
		res.Score = core.Clamp01(got/total - stalePenalty)
	} else {
		res.Score = 1
	}

	// TODO: weigh docs coverage against repo size — a 200-file repo needs more
	// than a README, a 5-file utility does not.
	res.Notes = append(res.Notes, "skeleton analyzer: presence checks are live, freshness heuristic is minimal (see TODOs in docs.go)")
	if newestDocPath != "" {
		res.Notes = append(res.Notes, "newest documentation file: "+newestDocPath)
	}
	return res
}
