// Package score aggregates per-dimension results into one number.
package score

import (
	"sort"

	"zauditor/internal/core"
)

// Dimension is one analyzer's contribution to the report.
type Dimension struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Weight     float64        `json:"weight"`
	Score      float64        `json:"score"`
	Skipped    bool           `json:"skipped"`
	SkipReason string         `json:"skip_reason,omitempty"`
	Notes      []string       `json:"notes,omitempty"`
	Findings   []core.Finding `json:"findings"`
	Counts     SeverityCounts `json:"counts"`
}

// SeverityCounts summarises findings without re-walking them.
type SeverityCounts struct {
	Critical int `json:"critical"`
	Warn     int `json:"warn"`
	Info     int `json:"info"`
}

// Report is the full audit result.
type Report struct {
	Root       string         `json:"root"`
	Score      float64        `json:"score"`     // 0..1
	Score100   float64        `json:"score_100"` // 0..100, rounded to 1 decimal
	Grade      string         `json:"grade"`
	Dimensions []Dimension    `json:"dimensions"`
	Counts     SeverityCounts `json:"counts"`
	Files      int            `json:"files"`
	Lines      int            `json:"lines"`
}

// Compute runs the selected analyzers and folds their results together.
// Skipped dimensions are excluded from the weighted average: a repo with no
// TypeScript should not be punished for having no tsconfig.
func Compute(ctx *core.RepoContext, analyzers []core.Analyzer) Report {
	rep := Report{Root: ctx.Root}
	var sumW, sumWS float64

	for _, a := range analyzers {
		res := a.Analyze(ctx)
		w := ctx.Config.WeightFor(a.ID(), a.Weight())

		d := Dimension{
			ID:         a.ID(),
			Name:       a.Name(),
			Weight:     w,
			Score:      core.Clamp01(res.Score),
			Skipped:    res.Skipped,
			SkipReason: res.SkipReason,
			Notes:      res.Notes,
			Findings:   sortFindings(res.Findings),
		}
		d.Counts = count(d.Findings)
		rep.Counts.Critical += d.Counts.Critical
		rep.Counts.Warn += d.Counts.Warn
		rep.Counts.Info += d.Counts.Info

		if !d.Skipped && w > 0 {
			sumW += w
			sumWS += w * d.Score
		}
		rep.Dimensions = append(rep.Dimensions, d)
	}

	for _, f := range ctx.Files {
		rep.Files++
		rep.Lines += f.Lines
	}

	if sumW > 0 {
		rep.Score = core.Clamp01(sumWS / sumW)
	} else {
		rep.Score = 1
	}
	rep.Score100 = float64(int(rep.Score*1000+0.5)) / 10
	rep.Grade = Grade(rep.Score)
	return rep
}

// Grade maps a score to a coarse letter, for people who read the headline only.
func Grade(s float64) string {
	switch {
	case s >= 0.90:
		return "A"
	case s >= 0.75:
		return "B"
	case s >= 0.60:
		return "C"
	case s >= 0.45:
		return "D"
	default:
		return "F"
	}
}

// sortFindings orders by severity (critical first), then path, then line, so
// two runs over the same repo always print in the same order.
func sortFindings(fs []core.Finding) []core.Finding {
	out := make([]core.Finding, len(fs))
	copy(out, fs)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func count(fs []core.Finding) SeverityCounts {
	var c SeverityCounts
	for _, f := range fs {
		switch f.Severity {
		case core.SeverityCritical:
			c.Critical++
		case core.SeverityWarn:
			c.Warn++
		default:
			c.Info++
		}
	}
	return c
}

// TopFindings returns the n most important findings across all dimensions.
func (r Report) TopFindings(n int) []core.Finding {
	var all []core.Finding
	for _, d := range r.Dimensions {
		all = append(all, d.Findings...)
	}
	all = sortFindings(all)
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}
