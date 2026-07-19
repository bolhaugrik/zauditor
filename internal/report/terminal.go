// Package report renders an audit result. Every renderer takes the same
// score.Report, so adding an output format never touches the analyzers.
package report

import (
	"fmt"
	"io"
	"os"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/score"
)

// ANSI codes are inlined rather than pulled from a library: the binary must
// stay dependency-free, and this is the entire surface we need.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	yellow = "\033[33m"
	green  = "\033[32m"
	cyan   = "\033[36m"
	gray   = "\033[90m"
)

// Options control the terminal renderer.
type Options struct {
	Color bool
	// MaxFindings caps the finding list; 0 means no cap.
	MaxFindings int
	// Verbose also prints info-level findings and analyzer notes.
	Verbose bool
}

// ColorEnabled decides whether to emit ANSI codes: honour NO_COLOR, and only
// colourise when writing to a real terminal.
func ColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

type painter struct{ on bool }

func (p painter) c(code, s string) string {
	if !p.on {
		return s
	}
	return code + s + reset
}

// Terminal writes the human-readable report.
func Terminal(w io.Writer, rep score.Report, opts Options) {
	p := painter{on: opts.Color}

	fmt.Fprintf(w, "\n%s %s\n", p.c(bold, "zauditor"), p.c(gray, rep.Root))
	fmt.Fprintf(w, "%s\n", p.c(gray, strings.Repeat("─", 72)))

	scoreColor := green
	switch {
	case rep.Score < 0.45:
		scoreColor = red
	case rep.Score < 0.75:
		scoreColor = yellow
	}
	fmt.Fprintf(w, "\n  %s  %s   %s\n",
		p.c(bold+scoreColor, fmt.Sprintf("%.1f/100", rep.Score100)),
		p.c(bold+scoreColor, "["+rep.Grade+"]"),
		p.c(gray, fmt.Sprintf("%d files, %d lines", rep.Files, rep.Lines)))

	fmt.Fprintf(w, "\n%s\n", p.c(bold, "Dimensions"))
	for _, d := range rep.Dimensions {
		if d.Skipped {
			fmt.Fprintf(w, "  %-28s %s  %s\n", d.Name, p.c(gray, "  skipped"), p.c(gray, d.SkipReason))
			continue
		}
		fmt.Fprintf(w, "  %-28s %s %s  %s\n",
			d.Name,
			bar(p, d.Score),
			p.c(colorFor(d.Score), fmt.Sprintf("%3.0f%%", d.Score*100)),
			p.c(gray, fmt.Sprintf("w=%.1f  %s", d.Weight, countsLabel(p, d.Counts))))
		if opts.Verbose {
			for _, n := range d.Notes {
				fmt.Fprintf(w, "      %s\n", p.c(dim, n))
			}
		}
	}

	findings := rep.TopFindings(0)
	if !opts.Verbose {
		findings = filterAtLeast(findings, core.SeverityWarn)
	}
	shown := findings
	if opts.MaxFindings > 0 && len(shown) > opts.MaxFindings {
		shown = shown[:opts.MaxFindings]
	}

	if len(shown) == 0 {
		fmt.Fprintf(w, "\n%s\n\n", p.c(green, "No findings above info level. "+
			"This repo is cheap to load into context."))
		return
	}

	fmt.Fprintf(w, "\n%s\n", p.c(bold, "Findings"))
	for _, f := range shown {
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		fmt.Fprintf(w, "\n  %s %s\n", sevLabel(p, f.Severity), p.c(cyan, loc))
		fmt.Fprintf(w, "    %s\n", f.Message)
		fmt.Fprintf(w, "    %s %s\n", p.c(gray, "fix:"), p.c(dim, wrap(f.Fix, 68, "         ")))
	}

	if len(findings) > len(shown) {
		fmt.Fprintf(w, "\n  %s\n", p.c(gray, fmt.Sprintf("… %d more findings (use --max-findings 0 to see all)", len(findings)-len(shown))))
	}
	if !opts.Verbose {
		fmt.Fprintf(w, "  %s\n", p.c(gray, "info-level findings hidden (use --verbose)"))
	}
	fmt.Fprintln(w)
}

func colorFor(s float64) string {
	switch {
	case s < 0.45:
		return red
	case s < 0.75:
		return yellow
	default:
		return green
	}
}

func bar(p painter, s float64) string {
	const width = 20
	filled := int(s*width + 0.5)
	return p.c(colorFor(s), strings.Repeat("█", filled)) + p.c(gray, strings.Repeat("·", width-filled))
}

func sevLabel(p painter, s core.Severity) string {
	switch s {
	case core.SeverityCritical:
		return p.c(bold+red, "CRIT")
	case core.SeverityWarn:
		return p.c(yellow, "WARN")
	default:
		return p.c(gray, "INFO")
	}
}

func countsLabel(p painter, c score.SeverityCounts) string {
	if c.Critical == 0 && c.Warn == 0 && c.Info == 0 {
		return "clean"
	}
	return fmt.Sprintf("%dC %dW %dI", c.Critical, c.Warn, c.Info)
}

func filterAtLeast(fs []core.Finding, min core.Severity) []core.Finding {
	var out []core.Finding
	for _, f := range fs {
		if f.Severity >= min {
			out = append(out, f)
		}
	}
	return out
}

// wrap soft-wraps a fix hint so long advice stays readable in a narrow terminal.
func wrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := 0
	for i, wd := range words {
		if line > 0 && line+1+len(wd) > width {
			b.WriteString("\n" + indent)
			line = 0
		} else if i > 0 {
			b.WriteString(" ")
			line++
		}
		b.WriteString(wd)
		line += len(wd)
	}
	return b.String()
}
