package analyzers

import (
	"path/filepath"
	"strings"
	"testing"

	"zauditor/config"
	"zauditor/internal/core"
	"zauditor/internal/walker"
)

// load builds a RepoContext from a fixture repo, going through the real walker
// so the tests exercise the same path the CLI does.
func load(t *testing.T, fixture string) *core.RepoContext {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	files, err := walker.Walk(root, walker.Options{})
	if err != nil {
		t.Fatalf("walk %s: %v", fixture, err)
	}
	if len(files) == 0 {
		t.Fatalf("fixture %s is empty — did testdata move?", fixture)
	}
	return core.NewRepoContext(root, files, config.Default())
}

func findingFor(res core.DimensionResult, pathSuffix string) (core.Finding, bool) {
	for _, f := range res.Findings {
		if strings.HasSuffix(f.Path, pathSuffix) {
			return f, true
		}
	}
	return core.Finding{}, false
}

func messageContains(res core.DimensionResult, substr string) bool {
	for _, f := range res.Findings {
		if strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

// --- size --------------------------------------------------------------------

func TestSizeFlagsGodFiles(t *testing.T) {
	res := (&sizeAnalyzer{}).Analyze(load(t, "messy"))

	god, ok := findingFor(res, "app/god_service.py")
	if !ok {
		t.Fatal("expected a finding for the 1300-line Python file")
	}
	if god.Severity != core.SeverityCritical {
		t.Errorf("god file severity = %v, want critical", god.Severity)
	}
	if god.Fix == "" {
		t.Error("every finding must carry an actionable fix")
	}

	if _, ok := findingFor(res, "web/Dashboard.tsx"); !ok {
		t.Error("expected a finding for the oversized React component (TSX warn threshold is 300)")
	}
	if !messageContains(res, "Catch-all module") {
		t.Error("expected utils.py to be flagged as a catch-all module")
	}
	if res.Score > 0.5 {
		t.Errorf("score = %.2f; a repo where most lines live in god files must score badly", res.Score)
	}
}

func TestSizeAcceptsSmallFiles(t *testing.T) {
	res := (&sizeAnalyzer{}).Analyze(load(t, "clean"))
	if len(res.Findings) != 0 {
		t.Fatalf("expected no size findings in the clean fixture, got %+v", res.Findings)
	}
	if res.Score != 1 {
		t.Errorf("score = %v, want 1", res.Score)
	}
}

func TestSizeThresholdsAreConfigurable(t *testing.T) {
	ctx := load(t, "clean")
	th := ctx.Config.Threshold("python")
	th.Warn, th.Critical = 1, 2
	ctx.Config.SizeThresholds["python"] = th

	res := (&sizeAnalyzer{}).Analyze(ctx)
	if _, ok := findingFor(res, "src/orders.py"); !ok {
		t.Fatal("lowering the threshold must produce findings — config is not being honoured")
	}
}

func TestSizeSkipsRepoWithoutSource(t *testing.T) {
	ctx := core.NewRepoContext("/empty", nil, nil)
	res := (&sizeAnalyzer{}).Analyze(ctx)
	if !res.Skipped {
		t.Fatal("a repo with no source files must be skipped, not scored 0")
	}
}

func TestIsCatchAll(t *testing.T) {
	names := config.Default().CatchAllNames
	for _, p := range []string{"app/utils.py", "src/helpers.ts", "lib/common.js", "src/utils/index.ts"} {
		if !isCatchAll(p, names) {
			t.Errorf("isCatchAll(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"app/orders.py", "src/Widget.tsx", "src/api/index.ts"} {
		if isCatchAll(p, names) {
			t.Errorf("isCatchAll(%q) = true, want false", p)
		}
	}
}

// --- tooling -----------------------------------------------------------------

func TestToolingRecognisesAFullFeedbackLoop(t *testing.T) {
	res := (&toolingAnalyzer{}).Analyze(load(t, "clean"))
	if res.Score != 1 {
		t.Fatalf("score = %.2f, want 1. Unsatisfied signals: %+v", res.Score, res.Findings)
	}
}

func TestToolingFlagsMissingFeedbackLoop(t *testing.T) {
	res := (&toolingAnalyzer{}).Analyze(load(t, "messy"))
	if res.Score > 0.1 {
		t.Fatalf("score = %.2f; a repo with no config files at all must score near 0", res.Score)
	}

	want := []string{
		"single entry point for build/lint/test",
		"Python linter/formatter config",
		"tsconfig.json",
		"CI configuration",
	}
	for _, w := range want {
		if !messageContains(res, w) {
			t.Errorf("expected a finding mentioning %q", w)
		}
	}

	var criticals int
	for _, f := range res.Findings {
		if f.Severity == core.SeverityCritical {
			criticals++
		}
		if f.Fix == "" {
			t.Errorf("finding %q has no fix", f.Message)
		}
	}
	if criticals == 0 {
		t.Error("a missing task runner and missing linter must be critical, not advisory")
	}
}

// TestToolingFindsConfigInMonorepoSubdirectories guards against the mistake
// this analyzer originally made: only looking at the repo root, and therefore
// reporting a monorepo with backend/pyproject.toml and frontend/package.json
// as having no feedback loop at all.
func TestToolingFindsConfigInMonorepoSubdirectories(t *testing.T) {
	res := (&toolingAnalyzer{}).Analyze(load(t, "monorepo"))
	if res.Score != 1 {
		t.Fatalf("score = %.2f, want 1. Falsely missing: %+v", res.Score, res.Findings)
	}
	var nested string
	for _, n := range res.Notes {
		if strings.HasPrefix(n, "found outside the repo root:") {
			nested = n
		}
	}
	for _, want := range []string{"js-package", "ts-config", "py-lint"} {
		if !strings.Contains(nested, want) {
			t.Errorf("note should record where %s was found, got %q", want, nested)
		}
	}
}

func TestFindConfigPrefersShallowestPath(t *testing.T) {
	ctx := load(t, "monorepo")
	if p, ok := ctx.FindConfig("package.json"); !ok || p != "frontend/package.json" {
		t.Fatalf("FindConfig(package.json) = %q, %v", p, ok)
	}
	// Nothing beyond MaxConfigDepth, and unknown names stay unfound.
	if p, ok := ctx.FindConfig("nonexistent.toml"); ok {
		t.Errorf("FindConfig matched a file that does not exist: %q", p)
	}
	// Caller order is the preference order.
	if p, _ := ctx.FindConfig("Makefile", "package.json"); p != "Makefile" {
		t.Errorf("FindConfig should honour caller order, got %q", p)
	}
}

func TestToolingSkipsPythonSignalsForAJSOnlyRepo(t *testing.T) {
	// The clean fixture has both languages; strip Python to prove the signal
	// set is language-conditional rather than a fixed checklist.
	ctx := load(t, "clean")
	var kept []core.FileInfo
	for _, f := range ctx.Files {
		if f.Lang != "python" {
			kept = append(kept, f)
		}
	}
	ctx = core.NewRepoContext(ctx.Root, kept, config.Default())

	res := (&toolingAnalyzer{}).Analyze(ctx)
	if messageContains(res, "Python") {
		t.Errorf("Python signals must not apply to a repo with no Python files: %+v", res.Findings)
	}
}

func TestTSStrictDetection(t *testing.T) {
	ctx := load(t, "clean")
	ok, evidence := tsStrict(ctx, "tsconfig.json")
	if !ok {
		t.Fatal("strict:true behind a // comment must still be detected (tsconfig is JSONC)")
	}
	if !strings.Contains(evidence, "tsconfig.json") {
		t.Errorf("evidence = %q, want it to name the file", evidence)
	}
	if ok, _ := tsStrict(ctx, "does-not-exist.json"); ok {
		t.Error("a missing tsconfig must not count as strict")
	}
}

// TestTSStrictFollowsProjectReferences covers the second false positive this
// analyzer produced in the field: a solution-style tsconfig that only lists
// references (the default Vite template) was read as "strict is off", even
// though every referenced config had it on.
func TestTSStrictFollowsProjectReferences(t *testing.T) {
	ctx := load(t, "monorepo")

	ok, evidence := tsStrict(ctx, "frontend/tsconfig.json")
	if !ok {
		t.Fatal("strict declared in referenced configs must count as strict")
	}
	if !strings.Contains(evidence, "tsconfig.app.json") {
		t.Errorf("evidence should name the config that actually declares strict, got %q", evidence)
	}
}

func TestTSStrictRequiresEveryReferencedConfig(t *testing.T) {
	ctx := load(t, "monorepo")
	// Simulate one referenced config lacking strict by pointing at a config
	// whose references cannot all be satisfied.
	if ok, _ := tsStrict(ctx, "frontend/tsconfig.node.json"); !ok {
		t.Fatal("a leaf config with strict:true must be reported strict")
	}
	if ok, _ := tsStrict(ctx, "frontend/package.json"); ok {
		t.Error("a file with neither strict, references nor extends must not count as strict")
	}
}

func TestHasTOMLSection(t *testing.T) {
	toml := "[project]\nname = \"x\"\n\n[tool.ruff]\nline-length = 100\n"
	if sec, ok := hasTOMLSection(toml, "tool.mypy", "tool.ruff"); !ok || sec != "tool.ruff" {
		t.Fatalf("hasTOMLSection = %q, %v", sec, ok)
	}
	if _, ok := hasTOMLSection(toml, "tool.black"); ok {
		t.Error("hasTOMLSection matched a section that is not present")
	}
}

// --- docs --------------------------------------------------------------------

// TestDocsRecognisesNonCanonicalDocumentation covers the fourth field false
// positive: a repo with 211 documents and a full .agents rule set scored 11%,
// because none of the files happened to be named ARCHITECTURE.md at the root.
func TestDocsRecognisesNonCanonicalDocumentation(t *testing.T) {
	res := (&docsAnalyzer{}).Analyze(load(t, "documented"))

	for _, missing := range []string{"README", "architecture overview", "agent instructions"} {
		if messageContains(res, "Missing: "+missing) {
			t.Errorf("%q is present in the fixture but was reported missing", missing)
		}
	}
	// Readme.md (not README.md), docs/LOGIC_FLOW.md (not ARCHITECTURE.md) and
	// .agents/**/*.md (not CLAUDE.md) must all count.
	if res.Score < 0.7 {
		t.Errorf("score = %.2f; a documented repo must not score like an undocumented one: %+v", res.Score, res.Findings)
	}
}

// TestDocsFlagsUnindexedCorpus checks the opposite failure mode: a pile of
// documents with no entry point is a search problem, not context.
func TestDocsFlagsUnindexedCorpus(t *testing.T) {
	res := (&docsAnalyzer{}).Analyze(load(t, "documented"))
	if !messageContains(res, "no index or entry point") {
		t.Errorf("31 docs with no docs/README.md must be flagged, got %+v", res.Findings)
	}
	if res2 := (&docsAnalyzer{}).Analyze(load(t, "clean")); messageContains(res2, "no index or entry point") {
		t.Error("a repo with a handful of docs must not be asked for an index")
	}
}

func TestFindArchitectureDocAcceptsAliases(t *testing.T) {
	ctx := load(t, "documented")
	p, ok := findArchitectureDoc(ctx)
	if !ok || p != "docs/LOGIC_FLOW.md" {
		t.Fatalf("findArchitectureDoc = %q, %v; want docs/LOGIC_FLOW.md", p, ok)
	}
}

func TestFindAgentInstructionsAcceptsDirectories(t *testing.T) {
	ctx := load(t, "documented")
	p, ok := findAgentInstructions(ctx)
	if !ok || !strings.HasPrefix(p, ".agents/") {
		t.Fatalf("findAgentInstructions = %q, %v; want the .agents directory", p, ok)
	}
	if _, ok := findAgentInstructions(load(t, "messy")); ok {
		t.Error("a repo with no agent instructions must not report any")
	}
}

// TestWorktreeCopyIsIgnored guards the fifth field problem: agent tooling keeps
// a full second copy of the repo under .claude/worktrees, which counted as
// source doubled every metric.
func TestWorktreeCopyIsIgnored(t *testing.T) {
	ctx := load(t, "documented")
	for _, f := range ctx.Files {
		if strings.Contains(f.Path, "worktrees") {
			t.Fatalf("%s should have been ignored: a worktree copy is not source", f.Path)
		}
	}
	// src/app.py and services/billing.py — the worktree's copy of src/app.py
	// must not appear as a third.
	if n := len(ctx.SourceFiles()); n != 2 {
		t.Errorf("source files = %d, want 2 (the worktree copy must not be counted)", n)
	}
}

// --- semantics ---------------------------------------------------------------

func TestSemanticsDetectsDanglingReferences(t *testing.T) {
	res := (&semanticsAnalyzer{}).Analyze(load(t, "documented"))
	if !messageContains(res, "no longer exist") {
		t.Fatalf("a documented path that was removed must be reported: %+v", res.Findings)
	}
	// src/app.py exists and src/helpers/legacy.py does not: one of two.
	if !messageContains(res, "1 of 2 documented paths") {
		t.Errorf("integrity should be counted over resolvable references only, got %+v", res.Notes)
	}
}

// TestSemanticsExcludesForwardLookingDocs guards a deliberate design choice:
// a plan legitimately names files that do not exist yet, and counting those as
// drift would punish planning rather than measure reliability.
func TestSemanticsExcludesForwardLookingDocs(t *testing.T) {
	res := (&semanticsAnalyzer{}).Analyze(load(t, "documented"))
	for _, f := range res.Findings {
		if strings.Contains(f.Path, "TERV") || strings.Contains(f.Message, "future/") {
			t.Errorf("references in a plan document must not count as drift: %+v", f)
		}
	}
	var noted bool
	for _, n := range res.Notes {
		if strings.Contains(n, "forward-looking") {
			noted = true
		}
	}
	if !noted {
		t.Error("excluded forward-looking references must still be reported in the notes")
	}
}

func TestSemanticsReportsUndocumentedAreas(t *testing.T) {
	res := (&semanticsAnalyzer{}).Analyze(load(t, "documented"))
	if !messageContains(res, "No documentation references services") {
		t.Errorf("a code area no document mentions must be reported: %+v", res.Findings)
	}
	if messageContains(res, "No documentation references src") {
		t.Error("src is referenced by docs/INDEXED.md and must count as covered")
	}
}

func TestSemanticsSkipsWithoutDocs(t *testing.T) {
	res := (&semanticsAnalyzer{}).Analyze(load(t, "messy"))
	if !res.Skipped {
		t.Fatal("absence of documentation is the docs dimension's finding, not this one's")
	}
}

func TestSemanticsIntegrityThresholdIsConfigurable(t *testing.T) {
	ctx := load(t, "documented")
	ctx.Config.DocRefIntegrityMin = 0.4 // 50% integrity now passes

	res := (&semanticsAnalyzer{}).Analyze(ctx)
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "no longer exist") && f.Severity != core.SeverityInfo {
			t.Errorf("below the configured threshold the drift finding should soften to info, got %v", f.Severity)
		}
	}
}

// --- agentfiles --------------------------------------------------------------

func TestAgentFilesFlagsBrokenCommands(t *testing.T) {
	res := (&agentFilesAnalyzer{}).Analyze(load(t, "agentrepo"))

	// `make test` and `npm run lint` exist; `make verify` and
	// `npm run typecheck` do not.
	if !messageContains(res, "no target \"verify\"") {
		t.Errorf("a make target that does not exist must be flagged: %+v", res.Findings)
	}
	if !messageContains(res, "no script \"typecheck\"") {
		t.Error("an npm script that does not exist must be flagged")
	}
	for _, ok := range []string{"make test", "npm run lint"} {
		for _, f := range res.Findings {
			if strings.Contains(f.Message, "`"+ok+"`") {
				t.Errorf("%q exists and must not be flagged: %s", ok, f.Message)
			}
		}
	}
	// A broken command is critical: the agent will actually run it.
	var crit bool
	for _, f := range res.Findings {
		if f.Severity == core.SeverityCritical {
			crit = true
		}
	}
	if !crit {
		t.Error("a command the agent is told to run but which does not exist must be critical")
	}
}

func TestAgentFilesFlagsBrokenPaths(t *testing.T) {
	res := (&agentFilesAnalyzer{}).Analyze(load(t, "agentrepo"))
	if !messageContains(res, "src/helpers/legacy.py") {
		t.Errorf("a referenced path that does not exist must be flagged: %+v", res.Findings)
	}
	if messageContains(res, "`src/app.py`, which does not exist") {
		t.Error("src/app.py exists and must not be flagged")
	}
}

func TestAgentFilesFlagsStubsAndEmptyDescription(t *testing.T) {
	res := (&agentFilesAnalyzer{}).Analyze(load(t, "agentrepo"))
	if !messageContains(res, "Stub instruction file") {
		t.Errorf("an instruction file with no body must be flagged: %+v", res.Findings)
	}
	if !messageContains(res, "empty `description`") {
		t.Error("frontmatter with an empty description must be flagged")
	}
	// Field false positive: a two-line rule can be a complete instruction.
	// Stub-ness is about how much is said, not how many lines it took.
	for _, f := range res.Findings {
		if strings.Contains(f.Path, "short-but-real") {
			t.Errorf("a short but complete rule must not be called a stub: %s", f.Message)
		}
	}
}

func TestAgentFilesFlagsCompetingRootFiles(t *testing.T) {
	res := (&agentFilesAnalyzer{}).Analyze(load(t, "agentrepo"))
	if !messageContains(res, "Multiple root instruction files") {
		t.Error("CLAUDE.md and AGENTS.md side by side must be reported")
	}
}

func TestAgentFilesSkipsRepoWithoutInstructions(t *testing.T) {
	res := (&agentFilesAnalyzer{}).Analyze(load(t, "messy"))
	if !res.Skipped {
		t.Fatal("absence is the docs dimension's finding; this one must skip to avoid double-counting")
	}
}

func TestAgentFilesRespectsContextBudget(t *testing.T) {
	ctx := load(t, "agentrepo")
	ctx.Config.AgentContext.Warn = 1
	ctx.Config.AgentContext.Critical = 2

	res := (&agentFilesAnalyzer{}).Analyze(ctx)
	if !messageContains(res, "Always-loaded instructions total") {
		t.Errorf("lowering the budget must produce a finding: %+v", res.Findings)
	}
}

// --- consistency -------------------------------------------------------------

// TestConsistencyIgnoresBuildTooling covers the third field false positive:
// vite.config.ts sitting next to tailwind.config.js was reported as a
// half-finished TS migration, when it is the normal shape of a Vite project.
func TestConsistencyIgnoresBuildTooling(t *testing.T) {
	res := (&consistencyAnalyzer{}).Analyze(load(t, "monorepo"))
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "Mixed JS and TS") {
			t.Errorf("build config files must not count as an application layer: %s", f.Message)
		}
	}
}

func TestIsToolConfigFile(t *testing.T) {
	for _, p := range []string{
		"frontend/vite.config.ts", "frontend/tailwind.config.js",
		"frontend/postcss.config.js", "eslint.config.mjs", "app/.eslintrc.js",
	} {
		if !isToolConfigFile(p) {
			t.Errorf("isToolConfigFile(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"src/config.ts", "src/App.tsx", "app/settings.js"} {
		if isToolConfigFile(p) {
			t.Errorf("isToolConfigFile(%q) = true, want false", p)
		}
	}
}

// --- registration ------------------------------------------------------------

func TestAllAnalyzersAreRegistered(t *testing.T) {
	want := []string{"consistency", "docs", "noise", "size", "tests", "tooling"}
	got := map[string]bool{}
	for _, a := range core.All() {
		got[a.ID()] = true
		if a.Name() == "" || a.Weight() <= 0 {
			t.Errorf("analyzer %q has an empty name or non-positive weight", a.ID())
		}
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("analyzer %q did not self-register", id)
		}
	}
}

// TestEveryAnalyzerIsDeterministic guards the core promise of the tool: the
// same tree must always produce the same report.
func TestEveryAnalyzerIsDeterministic(t *testing.T) {
	for _, fixture := range []string{"messy", "clean"} {
		for _, a := range core.All() {
			first := a.Analyze(load(t, fixture))
			second := a.Analyze(load(t, fixture))
			if first.Score != second.Score || len(first.Findings) != len(second.Findings) {
				t.Errorf("%s/%s is not deterministic: %v vs %v", fixture, a.ID(), first.Score, second.Score)
			}
			for i := range first.Findings {
				if first.Findings[i] != second.Findings[i] {
					t.Errorf("%s/%s finding %d differs between runs", fixture, a.ID(), i)
					break
				}
			}
		}
	}
}
