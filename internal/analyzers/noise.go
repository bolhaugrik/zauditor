package analyzers

import (
	"fmt"
	"regexp"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&noiseAnalyzer{}) }

// noiseAnalyzer is a SKELETON. Only the cheapest marker scan is implemented;
// the entropy metrics that give this dimension its value are TODO.
type noiseAnalyzer struct{}

func (a *noiseAnalyzer) ID() string      { return "noise" }
func (a *noiseAnalyzer) Name() string    { return "Signal-to-noise" }
func (a *noiseAnalyzer) Weight() float64 { return 1.0 }

func (a *noiseAnalyzer) Description() string {
	return "How much of what gets loaded into context is not load-bearing: commented-out code, debug prints, unresolved markers, escape-hatch types."
}

var (
	markerRE  = regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|XXX)\b`)
	debugRE   = regexp.MustCompile(`(^|\s)(console\.(log|debug|dir)|print)\s*\(`)
	anyTypeRE = regexp.MustCompile(`:\s*any\b|<any>|as\s+any\b`)
)

func (a *noiseAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		return core.Skip("no source files found")
	}

	res := core.DimensionResult{}
	var totalLines, markers, debugs, anys int
	perFile := map[string]int{}

	for _, f := range src {
		lines := ctx.LinesOf(f)
		totalLines += len(lines)
		for i, line := range lines {
			if markerRE.MatchString(line) {
				markers++
				perFile[f.Path]++
			}
			// TODO: this matches inside string literals and comments too. A
			// minimal lexer (or tree-sitter, once the detect layer grows one)
			// is needed before this can be trusted at line granularity.
			if debugRE.MatchString(line) && !isCommentLine(line, f.Lang) {
				debugs++
				if debugs <= 20 {
					res.Findings = append(res.Findings, core.Finding{
						Severity: core.SeverityInfo,
						Path:     f.Path,
						Line:     i + 1,
						Message:  "Debug statement left in source",
						Fix:      "Replace with a real logger call at an appropriate level, or delete it. Debug prints are context an agent must read and then discard.",
					})
				}
			}
			if (f.Lang == detect.LangTypeScript || f.Lang == detect.LangTSX) && anyTypeRE.MatchString(line) {
				anys++
			}
		}
	}

	// TODO: commented-out code detection — the single most valuable metric of
	// this dimension. Heuristic sketch: a comment line whose body parses as
	// code-shaped (ends in ; { } ) or : , contains an assignment or a call)
	// and that appears in a run of >=2 such lines. Must be language-aware to
	// avoid flagging prose and doctest blocks.
	// TODO: duplicated block detection (normalised-line hashing over a sliding
	// window) — copy-paste is what makes a repo expensive to change safely.
	// TODO: dead-file detection: source files nothing imports.
	// TODO: weight `any` by whether strict mode is on (see the tooling
	// dimension) — `any` in a non-strict project is a symptom, not the disease.

	markerDensity := 1000 * float64(markers) / float64(max(totalLines, 1))
	debugDensity := 1000 * float64(debugs) / float64(max(totalLines, 1))

	tsLines := ctx.LangStats[detect.LangTypeScript].Lines + ctx.LangStats[detect.LangTSX].Lines
	anyDensity := 0.0
	if tsLines > 0 {
		anyDensity = 1000 * float64(anys) / float64(tsLines)
	}

	// Densities are per 1000 lines; the divisors below are the point at which
	// each signal is considered fully saturated.
	pMarker := core.Clamp01(markerDensity / 15)
	pDebug := core.Clamp01(debugDensity / 5)
	pAny := core.Clamp01(anyDensity / 10)
	res.Score = core.Clamp01(1 - (0.4*pMarker + 0.3*pDebug + 0.3*pAny))

	if markers > 0 {
		worst, worstN := "", 0
		for p, n := range perFile {
			if n > worstN || (n == worstN && p < worst) {
				worst, worstN = p, n
			}
		}
		sev := core.SeverityInfo
		if pMarker > 0.5 {
			sev = core.SeverityWarn
		}
		res.Findings = append(res.Findings, core.Finding{
			Severity: sev,
			Path:     worst,
			Message:  fmt.Sprintf("%d TODO/FIXME/HACK markers repo-wide (%.1f per 1000 lines); most in this file (%d)", markers, markerDensity, worstN),
			Fix:      "Convert the ones that matter into tracked issues and delete the rest. An unowned TODO is a decision deferred forever, and every reader pays to re-evaluate it.",
		})
	}
	if anys > 0 {
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityInfo,
			Path:     ".",
			Message:  fmt.Sprintf("%d uses of `any` in TypeScript (%.1f per 1000 TS lines)", anys, anyDensity),
			Fix:      "Replace with `unknown` plus a narrowing check, or model the real shape. Each `any` is a hole the type checker cannot see through — exactly the feedback an agent relies on.",
		})
	}

	res.Notes = append(res.Notes, "skeleton analyzer: marker/debug/any scan only; commented-out-code and duplication detection are TODO (see noise.go)")
	return res
}

// isCommentLine is a rough guard so commented-out debug lines are not counted
// twice. TODO: replace with the real lexer this dimension needs.
func isCommentLine(line string, l detect.Language) bool {
	t := strings.TrimSpace(line)
	switch l {
	case detect.LangPython:
		return strings.HasPrefix(t, "#")
	default:
		return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
	}
}
