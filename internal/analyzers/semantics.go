package analyzers

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&semanticsAnalyzer{}) }

// semanticsAnalyzer measures whether the documentation corpus is a usable
// semantic index of *this* codebase.
//
// It deliberately does not attempt to judge whether a document is true or
// well written — that needs a reader, and the core of this tool is static and
// offline. What it can check is whether the map still matches the territory:
//
//   - referential integrity — do the paths the docs name still exist?
//   - coverage — is every code area described anywhere at all?
//
// The distinction matters more than it first appears. A methodology that
// front-loads documentation only pays off while the documentation is
// reliable; from a stale index a model does not fail safe, it fails
// confidently. Referential integrity is the cheapest available proxy for that
// reliability, and unlike prose quality it is decidable.
type semanticsAnalyzer struct{}

func (a *semanticsAnalyzer) ID() string      { return "semantics" }
func (a *semanticsAnalyzer) Name() string    { return "Semantic index (doc↔code)" }
func (a *semanticsAnalyzer) Weight() float64 { return 1.5 }

func (a *semanticsAnalyzer) Description() string {
	return "Whether the documentation still describes the code that exists: referential integrity of the paths it names, and coverage of the areas it should explain."
}

func (a *semanticsAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		return core.Skip("no source files found")
	}
	docs := ctx.ByLang(detect.LangMarkdown)
	if len(docs) == 0 {
		return core.Skip("no documentation to index (the docs dimension reports its absence)")
	}

	res := core.DimensionResult{}
	areas := sourceAreas(src)
	if len(areas) == 0 {
		return core.Skip("no top-level code areas to describe")
	}

	live, dangling, planned, perDoc, perArea := scanRefs(ctx, docs, areas)
	total := live + dangling

	// 1. Referential integrity.
	if total > 0 {
		integrity := float64(live) / float64(total)
		if dangling > 0 {
			worst := worstDoc(perDoc)
			sev := core.SeverityInfo
			if integrity < ctx.Config.DocRefIntegrityMin {
				sev = core.SeverityWarn
			}
			res.Findings = append(res.Findings, core.Finding{
				Severity: sev,
				Path:     worst,
				Message: fmt.Sprintf("%d of %d documented paths no longer exist (%.0f%% integrity); most in this file (%d)",
					dangling, total, 100*integrity, perDoc[worst]),
				Fix: "Update or delete the dead references. A documented path that moved is not a small error: the model does not fall back to reading the code, it goes to the wrong place confidently.",
			})
		}
		// The score is the integrity ratio itself, not a pass/fail against the
		// threshold: 91% accurate documentation is not the same as perfect,
		// and a dimension that rounds it up to perfect stops being useful for
		// tracking drift over time. The threshold only decides severity.
		res.Score = integrity
		res.Notes = append(res.Notes, fmt.Sprintf("%d path references across %d documents, %.0f%% still resolve", total, len(docs), 100*integrity))
	} else {
		// Documentation that never names a file is prose, not an index. It may
		// still be valuable, but it cannot be verified from here.
		res.Score = 0.5
		res.Notes = append(res.Notes, "documentation names no paths in this repo; nothing to verify structurally")
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityInfo,
			Path:     "docs",
			Message:  "Documentation references no files or directories of this repo",
			Fix:      "Name the modules and files you describe. A reference is what turns prose into an index an agent can navigate — and what makes drift detectable at all.",
		})
	}

	// 2. Coverage: an area nothing documents is an area a model must
	//    reconstruct from source, which is the expensive path this whole tool
	//    exists to measure.
	uncovered := 0
	for _, area := range sortedAreas(areas) {
		if perArea[area] > 0 {
			continue
		}
		uncovered++
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityWarn,
			Path:     area,
			Message:  fmt.Sprintf("No documentation references %s (%d source files)", area, areas[area]),
			Fix:      "Write one document that names this area's modules and says what each is for. Undocumented areas are where an agent reads the most code to learn the least.",
		})
	}
	if uncovered > 0 {
		res.Score = core.Clamp01(res.Score - float64(uncovered)/float64(len(areas)))
	}

	if planned > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d references excluded as forward-looking (plan/roadmap documents)", planned))
	}
	res.Notes = append(res.Notes, fmt.Sprintf("%d code areas, %d undocumented", len(areas), uncovered))
	return res
}

// --- reference scanning ------------------------------------------------------

var (
	// Only backticked spans are examined, for the same reason as in the
	// agentfiles dimension: prose mentions things loosely, a code span makes
	// a claim.
	docSpanRE = regexp.MustCompile("`([^`\n]{2,120})`")
	docPathRE = regexp.MustCompile(`^[A-Za-z0-9_.@\[\]-]+(?:/[A-Za-z0-9_.@\[\]-]+)+/?$`)
	// Documents that describe a future state legitimately name files that do
	// not exist yet. Counting those as drift would punish planning.
	planDocRE = regexp.MustCompile(`(?i)(plan|terv|roadmap|phase|proposal|javaslat|vision|koncepc|backlog|draft|rfc)`)
)

// sourceAreas returns the top-level directories holding application code,
// mapped to their source file count. Root-level files are not an area.
func sourceAreas(src []*core.FileInfo) map[string]int {
	areas := map[string]int{}
	for _, f := range src {
		if i := strings.Index(f.Path, "/"); i > 0 {
			areas[f.Path[:i]]++
		}
	}
	return areas
}

func sortedAreas(areas map[string]int) []string {
	out := make([]string, 0, len(areas))
	for a := range areas {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// scanRefs walks the documentation and resolves every path-shaped code span
// whose first segment names a real area — the evidence that the text is about
// this repository rather than an illustration.
func scanRefs(ctx *core.RepoContext, docs []*core.FileInfo, areas map[string]int) (live, dangling, planned int, perDoc, perArea map[string]int) {
	perDoc, perArea = map[string]int{}, map[string]int{}

	for _, doc := range docs {
		forwardLooking := planDocRE.MatchString(doc.Path)
		seen := map[string]bool{}
		for _, m := range docSpanRE.FindAllStringSubmatch(string(ctx.Content(doc)), -1) {
			ref := strings.TrimSuffix(strings.TrimSpace(m[1]), "/")
			if seen[ref] || !docPathRE.MatchString(ref) || strings.HasPrefix(ref, "http") {
				continue
			}
			seen[ref] = true
			first := strings.Split(ref, "/")[0]
			if areas[first] == 0 {
				continue
			}
			perArea[first]++

			if ctx.Has(ref) || len(ctx.DirFiles(ref)) > 0 {
				live++
				continue
			}
			if forwardLooking {
				planned++
				continue
			}
			dangling++
			perDoc[doc.Path]++
		}
	}
	return live, dangling, planned, perDoc, perArea
}

func worstDoc(perDoc map[string]int) string {
	worst, worstN := "", 0
	for p, n := range perDoc {
		if n > worstN || (n == worstN && p < worst) {
			worst, worstN = p, n
		}
	}
	return path.Clean(worst)
}
