// Package core defines the analyzer contract and the shared repository model.
//
// core never imports internal/analyzers. The dependency only flows the other
// way, which is what keeps the registration model open: adding an analyzer is a
// new file plus a Register call, never an edit here.
package core

// Severity ranks a finding. Ordering matters: reports sort descending.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarn
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityCritical:
		return "critical"
	case SeverityWarn:
		return "warn"
	default:
		return "info"
	}
}

// Finding is a single actionable observation. Fix is not optional in spirit:
// a finding without a concrete next step is noise, and this tool exists to
// reduce noise.
type Finding struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Line     int      `json:"line,omitempty"` // 0 = no specific line
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
}

// DimensionResult is what one analyzer reports back.
type DimensionResult struct {
	// Score is 0.0..1.0 where 1.0 is perfect.
	Score float64 `json:"score"`
	// Findings are ordered by the analyzer; the report layer re-sorts.
	Findings []Finding `json:"findings"`
	// Notes carry non-finding context for the report (e.g. "no Python files").
	Notes []string `json:"notes,omitempty"`
	// Skipped marks a dimension as not applicable to this repo. Skipped
	// dimensions are excluded from the weighted average rather than scored 0.
	Skipped bool `json:"skipped"`
	// SkipReason explains why, for the human report.
	SkipReason string `json:"skip_reason,omitempty"`
}

// Analyzer is the single extension point of zauditor.
type Analyzer interface {
	// ID is a stable identifier used by --only/--skip and config keys.
	ID() string
	// Name is the human-readable dimension name.
	Name() string
	// Weight is the default weight in the final score; config may override it.
	Weight() float64
	// Analyze inspects the shared context. Implementations must be pure with
	// respect to the filesystem: read through ctx, never walk on their own.
	Analyze(ctx *RepoContext) DimensionResult
}

// Describer is an optional interface. Analyzers implementing it can explain
// what the dimension means; the report layer shows it for low scores.
type Describer interface {
	Description() string
}

// Skip builds a not-applicable result.
func Skip(reason string) DimensionResult {
	return DimensionResult{Score: 1, Skipped: true, SkipReason: reason}
}

// Clamp01 constrains a score to the valid range. Analyzers should use it
// rather than trusting their own arithmetic.
func Clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
