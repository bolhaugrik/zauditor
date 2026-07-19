package analyzers

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&agentFilesAnalyzer{}) }

// agentFilesAnalyzer audits the instruction files written *for* an LLM.
//
// These are the highest-leverage bytes in the repository: unlike ordinary
// documentation, which someone reads once, an always-on instruction file is
// loaded into every request the agent handles. That makes its failure modes
// different from a README's — a wrong command in CLAUDE.md is not an
// inconvenience, it is an instruction the agent will follow.
//
// Absence is the docs dimension's business; this dimension only runs when
// instruction files exist, and asks whether they are any good.
type agentFilesAnalyzer struct{}

func (a *agentFilesAnalyzer) ID() string      { return "agentfiles" }
func (a *agentFilesAnalyzer) Name() string    { return "Agent instruction quality" }
func (a *agentFilesAnalyzer) Weight() float64 { return 1.5 }

func (a *agentFilesAnalyzer) Description() string {
	return "Whether the files written for an LLM are worth loading: sized for a permanent context budget, pointing at commands and paths that actually exist, and not stubs."
}

// agentFile is one instruction file with its frontmatter split off.
type agentFile struct {
	info      *core.FileInfo
	front     string   // YAML frontmatter, raw
	body      []string // non-empty body lines
	bodyChars int      // body length excluding whitespace and markdown headings
	alwaysOn  bool     // loaded into every request
	rootLevel bool
}

func (a *agentFilesAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	files := collectAgentFiles(ctx)
	if len(files) == 0 {
		return core.Skip("no agent instruction files (the docs dimension reports their absence)")
	}

	res := core.DimensionResult{}
	cfg := ctx.Config
	var penalties []float64

	// 1. Stubs. An instruction file with no body is not neutral: it advertises
	//    a capability the agent will try to use.
	stubs := 0
	for _, f := range files {
		if f.bodyChars >= cfg.AgentStubChars {
			continue
		}
		stubs++
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityWarn,
			Path:     f.info.Path,
			Message:  fmt.Sprintf("Stub instruction file: %d characters of content", f.bodyChars),
			Fix:      "Write it or delete it. An empty rule file still gets listed to the agent, which then has to open it to discover it says nothing.",
		})
	}
	penalties = append(penalties, core.Clamp01(float64(stubs)/float64(len(files))))

	// 2. Frontmatter that exists but says nothing. The description is how an
	//    agent decides whether to load the file at all.
	for _, f := range files {
		if f.front == "" {
			continue
		}
		if emptyFrontmatterField(f.front, "description") {
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     f.info.Path,
				Message:  "Frontmatter has an empty `description`",
				Fix:      "Describe when this file applies. The description is the routing key: an agent uses it to decide whether to read the file, and an empty one guarantees either always-read or never-read.",
			})
			penalties = append(penalties, 0.15)
		}
	}

	// 3. The always-on context budget. Every line here is paid on every task.
	var alwaysLines int
	var alwaysPaths []string
	for _, f := range files {
		if f.alwaysOn {
			alwaysLines += f.info.Lines
			alwaysPaths = append(alwaysPaths, f.info.Path)
		}
	}
	if alwaysLines > cfg.AgentContext.Warn {
		sev := core.SeverityWarn
		if alwaysLines > cfg.AgentContext.Critical {
			sev = core.SeverityCritical
		}
		res.Findings = append(res.Findings, core.Finding{
			Severity: sev,
			Path:     alwaysPaths[0],
			Message: fmt.Sprintf("Always-loaded instructions total %d lines across %d files (budget %d)",
				alwaysLines, len(alwaysPaths), cfg.AgentContext.Warn),
			Fix: "Move task-specific guidance into files the agent loads on demand, and keep the always-on file to the conventions that apply to every change. Context spent before reading any code is context unavailable for the task.",
		})
		penalties = append(penalties, core.Clamp01(float64(alwaysLines-cfg.AgentContext.Warn)/float64(cfg.AgentContext.Critical)))
	}

	// 4. Commands the instructions tell the agent to run, that do not exist.
	//    This is the most damaging defect available: the agent will run it.
	broken := checkCommands(ctx, files, &res)
	// 5. Paths referenced that are not in the tree any more.
	broken += checkPaths(ctx, files, &res)
	if broken > 0 {
		penalties = append(penalties, core.Clamp01(float64(broken)/5))
	}

	// 6. Competing authorities at the root.
	if rivals := rootInstructionFiles(ctx); len(rivals) > 1 {
		res.Findings = append(res.Findings, core.Finding{
			Severity: core.SeverityInfo,
			Path:     rivals[0],
			Message:  "Multiple root instruction files: " + strings.Join(rivals, ", "),
			Fix:      "Keep one authoritative file and have the others reference it (or symlink them). Parallel instruction files drift, and the agent cannot tell which one wins.",
		})
		penalties = append(penalties, 0.1)
	}

	res.Score = core.Clamp01(1 - sum(penalties))
	res.Notes = append(res.Notes,
		fmt.Sprintf("%d instruction files, %d always-on (%d lines)", len(files), len(alwaysPaths), alwaysLines))
	return res
}

// --- discovery ---------------------------------------------------------------

var agentRootNames = []string{
	"CLAUDE.md", "AGENTS.md", "GEMINI.md", ".cursorrules", ".windsurfrules",
	"copilot-instructions.md", "CONVENTIONS.md",
}

var agentDirs = []string{".agents", ".claude", ".cursor/rules", ".github/instructions", ".windsurf/rules"}

func collectAgentFiles(ctx *core.RepoContext) []agentFile {
	seen := map[string]bool{}
	var out []agentFile

	add := func(f *core.FileInfo) {
		if f == nil || seen[f.Path] || f.Lang != detect.LangMarkdown && !isRuleFile(f.Path) {
			return
		}
		seen[f.Path] = true
		out = append(out, newAgentFile(ctx, f))
	}

	for _, f := range ctx.FindAllFold(agentRootNames...) {
		add(f)
	}
	for _, dir := range agentDirs {
		for _, f := range ctx.Glob(dir + "/**") {
			add(f)
		}
	}
	return out
}

// isRuleFile covers the extension-less conventions (.cursorrules) that the
// language detector reports as "other".
func isRuleFile(p string) bool {
	base := strings.ToLower(path.Base(p))
	return base == ".cursorrules" || base == ".windsurfrules" || strings.HasSuffix(base, ".mdc")
}

var frontmatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n?`)

func newAgentFile(ctx *core.RepoContext, f *core.FileInfo) agentFile {
	af := agentFile{info: f, rootLevel: !strings.Contains(f.Path, "/")}
	raw := string(ctx.Content(f))

	body := raw
	if m := frontmatterRE.FindStringSubmatch(raw); m != nil {
		af.front = m[1]
		body = raw[len(m[0]):]
	}
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		af.body = append(af.body, line)
		// A heading is a label, not instruction: "# Conventions" followed by
		// nothing is still a stub.
		if !strings.HasPrefix(t, "#") {
			af.bodyChars += len(t)
		}
	}

	// A root CLAUDE.md/AGENTS.md is always loaded by convention; inside rule
	// directories the frontmatter says so explicitly.
	af.alwaysOn = af.rootLevel ||
		strings.Contains(af.front, "always_on") ||
		strings.Contains(af.front, "alwaysApply: true") ||
		strings.Contains(af.front, "alwaysApply: True")
	return af
}

func rootInstructionFiles(ctx *core.RepoContext) []string {
	var out []string
	for _, f := range ctx.FindAllFold(agentRootNames...) {
		if !strings.Contains(f.Path, "/") {
			out = append(out, f.Path)
		}
	}
	return out
}

func emptyFrontmatterField(front, field string) bool {
	for _, line := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), field) {
			return strings.TrimSpace(v) == ""
		}
	}
	return false
}

// --- reference checking ------------------------------------------------------

// Only text inside backticks is examined. Prose mentions a "src/api" loosely;
// a code span is a claim about something that exists.
var (
	codeSpanRE = regexp.MustCompile("`([^`\n]{2,120})`")
	makeCmdRE  = regexp.MustCompile(`^make\s+([a-zA-Z0-9_.-]+)$`)
	npmCmdRE   = regexp.MustCompile(`^(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?([a-zA-Z0-9:_-]+)$`)
	makeTgtRE  = regexp.MustCompile(`(?m)^([a-zA-Z0-9_.-]+):`)
)

func codeSpans(af agentFile) []string {
	var out []string
	for _, m := range codeSpanRE.FindAllStringSubmatch(strings.Join(af.body, "\n"), -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// checkCommands verifies `make x` and `npm run x` references against the
// Makefile and package.json that actually exist in the repo. When neither is
// present the check stays silent: we cannot distinguish a wrong command from
// one provided by an environment we cannot see.
func checkCommands(ctx *core.RepoContext, files []agentFile, res *core.DimensionResult) int {
	makePath, hasMake := ctx.FindConfig("Makefile", "makefile")
	targets := map[string]bool{}
	if hasMake {
		for _, m := range makeTgtRE.FindAllStringSubmatch(string(ctx.ContentOf(makePath)), -1) {
			targets[m[1]] = true
		}
	}
	scripts, pkgPath, hasScripts := packageScripts(ctx)

	broken := 0
	for _, af := range files {
		for _, span := range codeSpans(af) {
			switch {
			case hasMake && makeCmdRE.MatchString(span):
				target := makeCmdRE.FindStringSubmatch(span)[1]
				if targets[target] {
					continue
				}
				broken++
				res.Findings = append(res.Findings, core.Finding{
					Severity: core.SeverityCritical,
					Path:     af.info.Path,
					Message:  fmt.Sprintf("Instructions reference `%s`, but %s has no target %q", span, makePath, target),
					Fix:      "Fix the command or add the target. The agent will run what it is told to run, and a failing command it was promised sends it looking for the fault in your code.",
				})
			case hasScripts && npmCmdRE.MatchString(span):
				name := npmCmdRE.FindStringSubmatch(span)[1]
				if _, ok := scripts[name]; ok || npmBuiltins[name] {
					continue
				}
				broken++
				res.Findings = append(res.Findings, core.Finding{
					Severity: core.SeverityCritical,
					Path:     af.info.Path,
					Message:  fmt.Sprintf("Instructions reference `%s`, but %s has no script %q", span, pkgPath, name),
					Fix:      "Fix the command or add the script. An agent told to run a script that does not exist will assume the project is broken.",
				})
			}
		}
	}
	return broken
}

var npmBuiltins = map[string]bool{
	"install": true, "ci": true, "test": true, "start": true, "run": true,
	"add": true, "i": true, "exec": true, "dlx": true, "create": true,
}

// pathRefRE matches a code span that looks like a repo path: at least one
// slash and a plausible file or directory name.
var pathRefRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+/?$`)

// checkPaths reports referenced paths that no longer exist. To keep precision
// high it only judges a path whose first segment is a real directory in this
// repo — that is the evidence the text is describing *this* tree rather than
// giving a generic example.
func checkPaths(ctx *core.RepoContext, files []agentFile, res *core.DimensionResult) int {
	realDirs := map[string]bool{}
	for _, d := range ctx.Dirs() {
		realDirs[strings.Split(d, "/")[0]] = true
	}

	broken := 0
	for _, af := range files {
		reported := map[string]bool{}
		for _, span := range codeSpans(af) {
			ref := strings.TrimSuffix(span, "/")
			if reported[ref] || !pathRefRE.MatchString(span) {
				continue
			}
			first := strings.Split(ref, "/")[0]
			if !realDirs[first] || ctx.Has(ref) || realDirs[ref] || isDirInRepo(ctx, ref) {
				continue
			}
			reported[ref] = true
			broken++
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     af.info.Path,
				Message:  fmt.Sprintf("Instructions reference `%s`, which does not exist", span),
				Fix:      "Update the path or remove the reference. A path that moved is worse than no path: the agent trusts it and looks in the wrong place.",
			})
		}
	}
	return broken
}

func isDirInRepo(ctx *core.RepoContext, p string) bool {
	return len(ctx.DirFiles(p)) > 0
}

func sum(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s
}
