package analyzers

import (
	"fmt"
	"path"
	"strings"

	"zauditor/internal/core"
)

func init() { core.Register(&testsAnalyzer{}) }

// testsAnalyzer is a SKELETON. It answers "is there anything to check against
// at all?" — not coverage. See the TODOs for the intended depth.
type testsAnalyzer struct{}

func (a *testsAnalyzer) ID() string      { return "tests" }
func (a *testsAnalyzer) Name() string    { return "Test presence" }
func (a *testsAnalyzer) Weight() float64 { return 2.0 }

func (a *testsAnalyzer) Description() string {
	return "Whether a change can be validated. Not coverage percentage — the existence of a safety net an agent can run and read as a spec."
}

func (a *testsAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		return core.Skip("no source files found")
	}

	var testFiles, prodFiles int
	for _, f := range src {
		if isTestFile(f.Path) {
			testFiles++
		} else {
			prodFiles++
		}
	}

	res := core.DimensionResult{}
	if testFiles == 0 {
		res.Score = 0
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityCritical,
			Path:     ".",
			Message:  fmt.Sprintf("No test files found across %d source files", prodFiles),
			Fix:      "Add one test for the most-edited module and make it runnable with a single command. The first test is worth more than the next fifty: it establishes where tests live and how they run.",
		})
		res.Notes = append(res.Notes, "skeleton analyzer: see TODOs in tests.go")
		return res
	}

	ratio := float64(testFiles) / float64(max(prodFiles, 1))
	target := ctx.Config.MinTestRatio
	res.Score = core.Clamp01(ratio / max(target, 0.01))
	if ratio < target {
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityWarn,
			Path:     ".",
			Message:  fmt.Sprintf("Thin test layer: %d test files for %d source files (ratio %.2f, target %.2f)", testFiles, prodFiles, ratio, target),
			Fix:      "Add tests next to the modules that change most often. Prioritise by git churn, not by what is easiest to test.",
		})
	}

	// TODO: report untested *areas*, not just a global ratio — a top-level
	// directory with source files and no sibling tests is the actionable unit.
	// TODO: detect empty/placeholder tests (a test file with no assertions is
	// a false safety signal, which is worse than an honest gap).
	// TODO: distinguish unit / integration / e2e by directory convention and
	// report the shape of the pyramid.
	// TODO: cross-reference with the tooling dimension — tests that exist but
	// have no runner configured are not a feedback loop.
	res.Notes = append(res.Notes,
		fmt.Sprintf("%d test files / %d source files (ratio %.2f)", testFiles, prodFiles, ratio),
		"skeleton analyzer: ratio only, no per-area analysis yet (see TODOs in tests.go)")
	return res
}

// isTestFile recognises the common conventions of the supported languages.
func isTestFile(p string) bool {
	lower := strings.ToLower(p)
	for _, seg := range strings.Split(lower, "/") {
		if seg == "test" || seg == "tests" || seg == "__tests__" || seg == "spec" || seg == "e2e" {
			return true
		}
	}
	base := path.Base(lower)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return strings.HasPrefix(stem, "test_") ||
		strings.HasSuffix(stem, "_test") ||
		strings.HasSuffix(stem, ".test") ||
		strings.HasSuffix(stem, ".spec") ||
		strings.HasSuffix(stem, "conftest")
}
