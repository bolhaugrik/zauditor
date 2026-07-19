package score

import (
	"math"
	"testing"

	"zauditor/config"
	"zauditor/internal/core"
)

type stub struct {
	id  string
	w   float64
	res core.DimensionResult
}

func (s *stub) ID() string                                     { return s.id }
func (s *stub) Name() string                                   { return s.id }
func (s *stub) Weight() float64                                { return s.w }
func (s *stub) Analyze(*core.RepoContext) core.DimensionResult { return s.res }

func ctx() *core.RepoContext {
	return core.NewRepoContext("/repo", nil, nil)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeWeightedAverage(t *testing.T) {
	rep := Compute(ctx(), []core.Analyzer{
		&stub{id: "a", w: 3, res: core.DimensionResult{Score: 1}},
		&stub{id: "b", w: 1, res: core.DimensionResult{Score: 0}},
	})
	if !near(rep.Score, 0.75) {
		t.Fatalf("score = %v, want 0.75", rep.Score)
	}
	if rep.Score100 != 75 {
		t.Fatalf("score100 = %v, want 75", rep.Score100)
	}
	if rep.Grade != "B" {
		t.Fatalf("grade = %q, want B", rep.Grade)
	}
}

func TestComputeExcludesSkippedDimensions(t *testing.T) {
	// A repo with no TypeScript must not be punished for having no tsconfig:
	// a skipped dimension leaves the average untouched.
	rep := Compute(ctx(), []core.Analyzer{
		&stub{id: "a", w: 1, res: core.DimensionResult{Score: 0.5}},
		&stub{id: "b", w: 9, res: core.Skip("not applicable")},
	})
	if !near(rep.Score, 0.5) {
		t.Fatalf("score = %v, want 0.5 (skipped dimension must not count)", rep.Score)
	}
}

func TestComputeHonoursConfigWeightOverride(t *testing.T) {
	c := ctx()
	zero := 0.0
	c.Config.Analyzers["b"] = config.AnalyzerConfig{Weight: &zero}

	rep := Compute(c, []core.Analyzer{
		&stub{id: "a", w: 1, res: core.DimensionResult{Score: 1}},
		&stub{id: "b", w: 100, res: core.DimensionResult{Score: 0}},
	})
	if !near(rep.Score, 1) {
		t.Fatalf("score = %v, want 1 (config weight 0 must neutralise the dimension)", rep.Score)
	}
}

func TestFindingsSortedBySeverityThenPath(t *testing.T) {
	rep := Compute(ctx(), []core.Analyzer{
		&stub{id: "a", w: 1, res: core.DimensionResult{Score: 0.5, Findings: []core.Finding{
			{Severity: core.SeverityInfo, Path: "a.py"},
			{Severity: core.SeverityCritical, Path: "z.py"},
			{Severity: core.SeverityWarn, Path: "m.py", Line: 9},
			{Severity: core.SeverityWarn, Path: "m.py", Line: 2},
		}}},
	})
	got := rep.Dimensions[0].Findings
	want := []struct {
		sev  core.Severity
		path string
		line int
	}{
		{core.SeverityCritical, "z.py", 0},
		{core.SeverityWarn, "m.py", 2},
		{core.SeverityWarn, "m.py", 9},
		{core.SeverityInfo, "a.py", 0},
	}
	for i, w := range want {
		if got[i].Severity != w.sev || got[i].Path != w.path || got[i].Line != w.line {
			t.Fatalf("finding %d = %+v, want %v %s:%d", i, got[i], w.sev, w.path, w.line)
		}
	}
	if c := rep.Counts; c.Critical != 1 || c.Warn != 2 || c.Info != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestTopFindingsCapsAcrossDimensions(t *testing.T) {
	rep := Compute(ctx(), []core.Analyzer{
		&stub{id: "a", w: 1, res: core.DimensionResult{Findings: []core.Finding{
			{Severity: core.SeverityInfo, Path: "a"},
		}}},
		&stub{id: "b", w: 1, res: core.DimensionResult{Findings: []core.Finding{
			{Severity: core.SeverityCritical, Path: "b"},
		}}},
	})
	top := rep.TopFindings(1)
	if len(top) != 1 || top[0].Path != "b" {
		t.Fatalf("TopFindings(1) = %+v, want the critical one", top)
	}
}

func TestGrade(t *testing.T) {
	for _, tt := range []struct {
		s    float64
		want string
	}{{0.95, "A"}, {0.90, "A"}, {0.80, "B"}, {0.60, "C"}, {0.50, "D"}, {0.1, "F"}} {
		if got := Grade(tt.s); got != tt.want {
			t.Errorf("Grade(%v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}
