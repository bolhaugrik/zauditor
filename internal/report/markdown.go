package report

import (
	"fmt"
	"io"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/score"
)

// Markdown writes a report suitable for a PR comment or a document pipeline.
// It contains no ANSI codes and no timestamps, so committing the output
// produces a meaningful diff between runs.
func Markdown(w io.Writer, rep score.Report, opts Options) {
	fmt.Fprintf(w, "# Repository audit\n\n")
	fmt.Fprintf(w, "**Score: %.1f/100 (%s)** — %d files, %d lines\n\n", rep.Score100, rep.Grade, rep.Files, rep.Lines)

	fmt.Fprintf(w, "| Dimension | Score | Weight | Critical | Warn | Info |\n")
	fmt.Fprintf(w, "|---|---:|---:|---:|---:|---:|\n")
	for _, d := range rep.Dimensions {
		if d.Skipped {
			fmt.Fprintf(w, "| %s | _skipped_ | %.1f | – | – | – |\n", d.Name, d.Weight)
			continue
		}
		fmt.Fprintf(w, "| %s | %.0f%% | %.1f | %d | %d | %d |\n",
			d.Name, d.Score*100, d.Weight, d.Counts.Critical, d.Counts.Warn, d.Counts.Info)
	}

	findings := rep.TopFindings(0)
	if !opts.Verbose {
		findings = filterAtLeast(findings, core.SeverityWarn)
	}
	if opts.MaxFindings > 0 && len(findings) > opts.MaxFindings {
		findings = findings[:opts.MaxFindings]
	}

	if len(findings) == 0 {
		fmt.Fprintf(w, "\nNo findings above info level.\n")
		return
	}

	fmt.Fprintf(w, "\n## Findings\n")
	var lastSev core.Severity = -1
	for _, f := range findings {
		if f.Severity != lastSev {
			fmt.Fprintf(w, "\n### %s\n\n", strings.ToUpper(f.Severity.String()))
			lastSev = f.Severity
		}
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		fmt.Fprintf(w, "- **`%s`** — %s\n  - _Fix:_ %s\n", loc, f.Message, f.Fix)
	}

	if opts.Verbose {
		fmt.Fprintf(w, "\n## Notes\n\n")
		for _, d := range rep.Dimensions {
			for _, n := range d.Notes {
				fmt.Fprintf(w, "- **%s**: %s\n", d.Name, n)
			}
		}
	}
}
