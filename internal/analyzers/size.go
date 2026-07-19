package analyzers

import (
	"fmt"
	"path"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

// Registration happens here and nowhere else. The core has no knowledge of
// this file's existence.
func init() { core.Register(&sizeAnalyzer{}) }

type sizeAnalyzer struct{}

func (a *sizeAnalyzer) ID() string      { return "size" }
func (a *sizeAnalyzer) Name() string    { return "File & module size" }
func (a *sizeAnalyzer) Weight() float64 { return 1.5 }

func (a *sizeAnalyzer) Description() string {
	return "How much of the codebase lives in files too large to load, reason about and edit in one pass."
}

func (a *sizeAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		return core.Skip("no Python/TS/JS/React/HTML source files found")
	}

	res := core.DimensionResult{}
	cfg := ctx.Config

	var totalLines, warnLines, critLines, catchAllLines int
	for _, f := range src {
		totalLines += f.Lines
		th := cfg.Threshold(f.Lang)

		switch {
		case f.Lines > th.Critical:
			critLines += f.Lines
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityCritical,
				Path:     f.Path,
				Message:  fmt.Sprintf("God file: %d lines (%s limit is %d)", f.Lines, f.Lang, th.Critical),
				Fix:      splitAdvice(f.Lang, f.Path),
			})
		case f.Lines > th.Warn:
			warnLines += f.Lines
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     f.Path,
				Message:  fmt.Sprintf("Oversized file: %d lines (%s warn threshold is %d)", f.Lines, f.Lang, th.Warn),
				Fix:      splitAdvice(f.Lang, f.Path),
			})
		}

		if isCatchAll(f.Path, cfg.CatchAllNames) && f.Lines >= cfg.CatchAllMinLines {
			catchAllLines += f.Lines
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     f.Path,
				Message:  fmt.Sprintf("Catch-all module: %d lines in a generically named file", f.Lines),
				Fix:      "Split by responsibility and name the parts after what they do (dates.go-style: date_format.py, retry.py). A file named after nothing attracts everything.",
			})
		}
	}

	// Directory width: a folder with dozens of siblings forces a model to read
	// filenames instead of structure.
	var overflow int
	for _, dir := range ctx.Dirs() {
		files := ctx.DirFiles(dir)
		if len(files) <= cfg.DirWidthWarn {
			continue
		}
		overflow += len(files) - cfg.DirWidthWarn
		label := dir
		if label == "." {
			label = "(repo root)"
		}
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityWarn,
			Path:     dir,
			Message:  fmt.Sprintf("Wide directory: %d files directly in %s (threshold %d)", len(files), label, cfg.DirWidthWarn),
			Fix:      "Group the files into sub-packages by feature. Directory names are free documentation; a flat list of 40 files is none.",
		})
	}

	// Score: three independent penalties, each expressed as a share of the
	// codebase rather than a raw count, so small and large repos compare.
	pSize := core.Clamp01(float64(warnLines+2*critLines) / float64(max(totalLines, 1)))
	pCatchAll := core.Clamp01(2 * float64(catchAllLines) / float64(max(totalLines, 1)))
	pDir := core.Clamp01(float64(overflow) / float64(max(len(ctx.Files), 1)))
	res.Score = core.Clamp01(1 - (0.55*pSize + 0.25*pCatchAll + 0.20*pDir))

	res.Notes = append(res.Notes, fmt.Sprintf("%d source files, %d lines; %d lines (%.0f%%) sit in oversized files",
		len(src), totalLines, warnLines+critLines, 100*float64(warnLines+critLines)/float64(max(totalLines, 1))))
	return res
}

// splitAdvice tailors the fix hint to the language, because "split this up"
// means different things in a Django module and a React component.
func splitAdvice(l detect.Language, p string) string {
	switch {
	case detect.IsFrontendComponent(l):
		return "Extract sub-components and move data fetching/state into hooks. A component that does not fit on one screen cannot be reviewed on one screen."
	case l == detect.LangPython:
		return "Split into a package: one module per responsibility, with the public surface re-exported from __init__.py. Start with the class or function group that has the fewest inbound references."
	case l == detect.LangHTML:
		return "Break the page into partials/templates and move inline scripts and styles into their own files."
	default:
		return "Split into modules along the seams that already exist (exports used by only one caller are the first candidates to move)."
	}
}

func isCatchAll(p string, names []string) bool {
	base := strings.ToLower(path.Base(p))
	if i := strings.Index(base, "."); i > 0 {
		base = base[:i]
	}
	// index.ts inside a utils/ folder counts too: the folder is the dump.
	dir := strings.ToLower(path.Base(path.Dir(p)))
	for _, n := range names {
		if base == n {
			return true
		}
		if dir == n && (base == "index" || base == "__init__") {
			return true
		}
	}
	return false
}
