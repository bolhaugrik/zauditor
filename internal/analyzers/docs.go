package analyzers

import (
	"fmt"
	"path"
	"strings"
	"time"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&docsAnalyzer{}) }

// docsAnalyzer asks whether a newcomer — human or agent — can orient itself.
//
// The presence checks are deliberately generous about *where* and *how* a
// document is named, because the original version was a filename quiz: it
// scored a repo with 211 documents and a full agent rule set at 11%, purely
// because none of the files were spelled ARCHITECTURE.md at the root. A
// dimension that punishes a well-documented repo teaches users to ignore it.
type docsAnalyzer struct{}

func (a *docsAnalyzer) ID() string      { return "docs" }
func (a *docsAnalyzer) Name() string    { return "Docs & agent instructions" }
func (a *docsAnalyzer) Weight() float64 { return 1.5 }

func (a *docsAnalyzer) Description() string {
	return "Whether the repo explains itself: what it is, how it is structured, and what an agent working here must know."
}

// docSignal is one documentation artefact we look for. found returns the path
// that satisfied it, so the report can say where it was located.
type docSignal struct {
	id       string
	title    string
	weight   float64
	severity core.Severity
	find     func(*core.RepoContext) (string, bool)
	fix      string
}

func (a *docsAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	if len(ctx.SourceFiles()) == 0 {
		return core.Skip("no source files found")
	}

	sigs := []docSignal{
		{
			id: "readme", title: "README", weight: 2, severity: core.SeverityCritical,
			find: func(c *core.RepoContext) (string, bool) {
				return c.FindFold("README.md", "README.rst", "README.txt", "README")
			},
			fix: "Add a README that answers three questions in the first screen: what this is, how to run it, how to test it.",
		},
		{
			id: "architecture", title: "architecture overview", weight: 1.5, severity: core.SeverityWarn,
			find: findArchitectureDoc,
			fix:  "Add ARCHITECTURE.md naming the top-level modules and how a request flows through them. This is the single highest-leverage document for anyone loading the repo into context.",
		},
		{
			id: "contributing", title: "contribution guide", weight: 0.5, severity: core.SeverityInfo,
			find: func(c *core.RepoContext) (string, bool) {
				return c.FindFold("CONTRIBUTING.md", "DEVELOPING.md", "DEVELOPMENT.md")
			},
			fix: "Add CONTRIBUTING.md with the local setup and the definition of done.",
		},
		{
			id: "agents", title: "agent instructions", weight: 1.5, severity: core.SeverityWarn,
			find: findAgentInstructions,
			fix:  "Add CLAUDE.md/AGENTS.md with the conventions an agent cannot infer: which commands to run, which directories are generated, what must never be edited by hand.",
		},
	}

	res := core.DimensionResult{}
	var total, got float64
	var evidence []string

	for _, s := range sigs {
		total += s.weight
		p, ok := s.find(ctx)
		if !ok {
			res.Findings = append(res.Findings, core.Finding{
				Severity: s.severity,
				Path:     ".",
				Message:  "Missing: " + s.title,
				Fix:      s.fix,
			})
			continue
		}
		got += s.weight
		evidence = append(evidence, s.id+" → "+p)
	}

	score := got / total
	res.Findings = append(res.Findings, corpusFindings(ctx)...)
	score -= stalenessPenalty(ctx, &res)
	res.Score = core.Clamp01(score)

	res.Notes = append(res.Notes, "documentation found: "+joinOrNone(evidence))
	if n := len(docCorpus(ctx)); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d markdown documents in the repo", n))
	}
	return res
}

// findArchitectureDoc accepts the canonical filename anywhere shallow, and
// otherwise any document under a docs directory whose name reads like a
// system overview. Projects rarely use the exact word "architecture".
func findArchitectureDoc(c *core.RepoContext) (string, bool) {
	if p, ok := c.FindFold("ARCHITECTURE.md", "ARCHITECTURE.rst", "DESIGN.md", "OVERVIEW.md"); ok {
		return p, true
	}
	for _, f := range docCorpus(c) {
		name := strings.ToLower(path.Base(f.Path))
		if archNameHints.matches(name) {
			return f.Path, true
		}
	}
	return "", false
}

// findAgentInstructions recognises both the single-file and the directory
// conventions. Agent tooling has not converged on one shape, and a repo with
// .agents/rules/*.md plus .agents/workflows/*.md is more thoroughly instructed
// than one with a stub CLAUDE.md.
func findAgentInstructions(c *core.RepoContext) (string, bool) {
	if p, ok := c.FindFold("CLAUDE.md", "AGENTS.md", "GEMINI.md", ".cursorrules", ".windsurfrules", "copilot-instructions.md"); ok {
		return p, true
	}
	for _, dir := range []string{".agents", ".claude", ".cursor/rules", ".github/instructions", "guardian-rules"} {
		if m := c.Glob(dir + "/**"); len(m) > 0 {
			for _, f := range m {
				// settings.json alone is tool configuration, not instruction.
				if f.Lang == detect.LangMarkdown {
					return dir + "/ (" + fmt.Sprint(len(m)) + " files)", true
				}
			}
		}
	}
	return "", false
}

// nameHints is a small substring matcher, kept explicit so the heuristic is
// readable and easy to extend.
type nameHints []string

func (h nameHints) matches(name string) bool {
	for _, hint := range h {
		if strings.Contains(name, hint) {
			return true
		}
	}
	return false
}

var archNameHints = nameHints{
	"architect", "logic_flow", "logic-flow", "system_design", "system-design",
	"overview", "struktura", "structure", "inventory", "data_model", "data-model",
}

// docCorpus returns the markdown documents that are actual documentation:
// anything at the root or under a docs directory, excluding generated and
// vendored trees (already filtered by the walker).
func docCorpus(c *core.RepoContext) []*core.FileInfo {
	var out []*core.FileInfo
	for _, f := range c.ByLang(detect.LangMarkdown) {
		dir := path.Dir(f.Path)
		if dir == "." || strings.HasPrefix(dir, "docs") || strings.HasPrefix(dir, "doc/") {
			out = append(out, f)
		}
	}
	return out
}

// CorpusIndexThreshold is the number of documents above which an unindexed
// docs directory becomes a problem of its own.
const CorpusIndexThreshold = 25

// corpusFindings reports the opposite failure mode from "no documentation":
// so many documents that an agent cannot tell which one to read. A pile of
// 200 undated reports is not context, it is a search problem.
func corpusFindings(c *core.RepoContext) []core.Finding {
	corpus := docCorpus(c)
	if len(corpus) < CorpusIndexThreshold {
		return nil
	}
	if _, ok := c.FindFold("mkdocs.yml", "mkdocs.yaml"); ok {
		return nil
	}
	// An index is only an index if it sits in the docs directory itself.
	for _, f := range c.FindAllFold("README.md", "INDEX.md", "SUMMARY.md", "_sidebar.md", "TOC.md") {
		if dir := path.Dir(f.Path); dir == "docs" || dir == "doc" {
			return nil
		}
	}
	return []core.Finding{{
		Severity: core.SeverityWarn,
		Path:     "docs",
		Message:  fmt.Sprintf("%d markdown documents with no index or entry point", len(corpus)),
		Fix:      "Add docs/README.md listing which document answers which question, and archive the superseded ones. Without an index an agent either reads everything or guesses — both are expensive.",
	}}
}

// stalenessPenalty compares the newest documentation against the newest code.
//
// TODO: mtime is unreliable on fresh clones and on copied directories — every
// file gets the checkout time. Prefer `git log -1 --format=%ct <path>` when a
// .git directory is present, and fall back to mtime only outside a repo.
// TODO: compare per-area (docs/api.md vs src/api/) instead of repo-wide, so one
// refreshed README does not mask a stale architecture doc.
// TODO: detect documents referencing paths that no longer exist — the strongest
// available signal that docs have drifted from the code.
func stalenessPenalty(c *core.RepoContext, res *core.DimensionResult) float64 {
	var newest time.Time
	var newestPath string
	for _, f := range docCorpus(c) {
		if f.ModTime.After(newest) {
			newest, newestPath = f.ModTime, f.Path
		}
	}
	if newest.IsZero() || c.NewestCodeMod.IsZero() {
		return 0
	}
	lag := c.NewestCodeMod.Sub(newest)
	limit := time.Duration(c.Config.DocsStaleDays) * 24 * time.Hour
	if lag <= limit {
		return 0
	}
	res.Findings = append(res.Findings, core.Finding{
		Severity: core.SeverityWarn,
		Path:     newestPath,
		Message:  fmt.Sprintf("Newest documentation is ~%d days older than the newest code", int(lag.Hours()/24)),
		Fix:      "Re-read the docs against the current code and update them in the same commit as the next feature. Stale docs are worse than none: they are confidently wrong context.",
	})
	return 0.25
}
