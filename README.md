# zauditor

**How expensive is this repository to work in?**

zauditor audits a codebase for context cost: how much a developer — human or AI —
has to load into working memory before making a correct change, and whether
there is anything to check the change against afterwards.

It is not a linter. It does not count style violations. It measures the
structural conditions that make a repo cheap or expensive to change:

| Dimension | Question it answers |
|---|---|
| **tooling** | Can a change be verified without a human? (linter, type checker, tests, CI) |
| **tests** | Is there a safety net at all? |
| **size** | How much of the code lives in files too large to hold in one pass? |
| **docs** | Does the repo explain itself — including to an agent? |
| **noise** | How much of the loaded context is not load-bearing? |
| **consistency** | Does the repo teach *one* way of doing things? |

Supported languages: Python, TypeScript, JavaScript, React (JSX/TSX), HTML.

## Design constraints

These are not negotiable, and every contribution has to hold them:

1. **Static, offline, deterministic.** No network calls, no LLM in the core, no
   timestamps in the output. The same tree always produces the same report.
   (An LLM plugin may come later; the diagnosis must never depend on it.)
2. **Zero runtime dependencies.** `go build` produces one binary you can copy
   anywhere. No external Go modules, colour included.
3. **Modularity above all.** Every check sits behind one interface and joins via
   registration. Adding a check must not require touching the core.
4. **Every finding is actionable.** A finding that says only "this is bad" is a
   bug. It must say what to do about it.

## Install & run

```sh
go build -o zauditor .
./zauditor ./path/to/repo
```

```
Usage:
  zauditor [flags] <path>

  --json                machine-readable output (CI)
  --markdown            markdown report
  --min-score <float>   exit non-zero if the final score (0..100) is lower
  --only <id,id>        run only these analyzers
  --skip <id,id>        skip these analyzers
  --config <path>       JSON file overriding weights and thresholds
  --max-findings <n>    cap printed findings (0 = all, default 25)
  --verbose             include info-level findings and analyzer notes
  --no-color            disable ANSI colour
  --list                list registered analyzers and exit
```

Exit codes: `0` success, `1` score below `--min-score`, `2` usage or I/O error.

As a CI gate:

```yaml
- run: zauditor --min-score 65 .
```

## Architecture

```
main.go                  CLI, orchestration; blank-imports internal/analyzers
internal/walker/         fs traversal, .gitignore + built-in ignore list
internal/detect/         language detection (extension, then shebang)
internal/core/
  analyzer.go            Analyzer interface, Finding, DimensionResult, Severity
  context.go             RepoContext — built once, shared by every analyzer
  registry.go            registration + selection (the extensibility seam)
internal/analyzers/      one file per analyzer, each self-registering
internal/score/          weighted aggregation, per-dimension breakdown
internal/report/         terminal (ANSI), json, markdown renderers
config/defaults.go       default weights and thresholds, JSON override loader
```

The dependency rule that makes this work: **`core` never imports `analyzers`.**
The arrow only points inward. `main` blank-imports the analyzer package so its
`init()` functions run; nothing else in the system knows which analyzers exist.

### RepoContext

The filesystem is walked exactly once. Analyzers receive a `*RepoContext` and
must never touch the disk themselves — everything goes through it, so the cost
is paid once and there is exactly one place for a future AST/tree-sitter layer
to hook into.

```go
ctx.SourceFiles()               // application code only
ctx.ByLang(detect.LangPython)   // per-language, path-ordered
ctx.Dirs() / ctx.DirFiles(dir)  // directory shape
ctx.Has("pyproject.toml")       // project metadata
ctx.HasAny("a", "b")            // first match wins
ctx.Glob(".github/workflows/*.yml")
ctx.Content(f) / ctx.LinesOf(f) // lazy, cached, BOM-stripped
```

Content is read lazily and cached: an analyzer that only needs file sizes never
causes a single file read.

## Adding an analyzer

This is the whole procedure. One new file, one `Register` call, nothing else.

Create `internal/analyzers/complexity.go`:

```go
package analyzers

import "zauditor/internal/core"

// 1. Register from init(). The core does not know this file exists.
func init() { core.Register(&complexityAnalyzer{}) }

type complexityAnalyzer struct{}

// 2. Implement the interface.
func (a *complexityAnalyzer) ID() string      { return "complexity" } // --only key
func (a *complexityAnalyzer) Name() string    { return "Cyclomatic complexity" }
func (a *complexityAnalyzer) Weight() float64 { return 1.0 }

// 3. Optional: implement core.Describer so --list explains the dimension.
func (a *complexityAnalyzer) Description() string {
	return "How much branching a reader must simulate to predict what a function does."
}

func (a *complexityAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		// Not applicable != score 0. A skipped dimension is excluded from the
		// weighted average instead of dragging it down.
		return core.Skip("no source files found")
	}

	res := core.DimensionResult{}
	for _, f := range src {
		for i, line := range ctx.LinesOf(f) {
			if isTooDeeplyNested(line) {
				res.Findings = append(res.Findings, core.Finding{
					Severity: core.SeverityWarn,
					Path:     f.Path,
					Line:     i + 1,
					Message:  "Nesting depth 5",
					// Fix is mandatory in spirit: say what to do.
					Fix: "Extract the inner block into a named function, or invert the guard clauses.",
				})
			}
		}
	}
	res.Score = core.Clamp01(1 - float64(len(res.Findings))/float64(len(src)))
	return res
}
```

That is it. `zauditor --list` now shows it, `--only complexity` runs it alone,
`--skip complexity` excludes it, the JSON report gains a dimension, and the
config file can re-weight or disable it. No file outside `internal/analyzers/`
changed.

### Rules for analyzers

- **Deterministic.** No map iteration order in output, no time, no randomness.
  `TestEveryAnalyzerIsDeterministic` runs every registered analyzer twice.
- **Read through `RepoContext`,** never through `os`.
- **Return `core.Skip(reason)`** when the dimension does not apply, rather than
  scoring 0. A Python-only repo must not lose points for having no tsconfig.
- **Score 0.0–1.0,** where 1.0 is perfect. Prefer expressing penalties as a
  *share* of the codebase so small and large repos are comparable.
- **Every `Finding` carries a `Fix`.**

## Configuration

`--config zauditor.json` overrides the built-in defaults. Only the keys present
in the file are replaced:

```json
{
  "size_thresholds": {
    "python": { "warn": 400, "critical": 900 },
    "tsx":    { "warn": 250, "critical": 500 }
  },
  "dir_width_warn": 30,
  "min_test_ratio": 0.3,
  "analyzers": {
    "noise":       { "weight": 0.5 },
    "consistency": { "enabled": false }
  }
}
```

`analyzers.<id>.enabled = false` is the config-driven on/off switch built on top
of the same registration model that `--only` and `--skip` use.

## Extending zauditor further

The registration model is designed to grow in two directions that are **not yet
implemented**, but which the architecture deliberately leaves open:

**(a) Config-driven enable/disable — done.** `config.Config.Disabled()` feeds
`core.Selection.Disabled`, which is the same mechanism `--only`/`--skip` use.
Adding per-analyzer *options* (not just weight) is a matter of extending
`config.AnalyzerConfig` and reading it from `ctx.Config` inside `Analyze`.

**(b) External plugins.** `--plugin ./mine` is parsed and currently returns an
error rather than silently auditing with fewer checks. Two viable routes, in
order of preference:

1. **Subprocess protocol.** The plugin binary is invoked with the repo path and
   returns a `DimensionResult` as JSON on stdout. A thin adapter in `core`
   implements `Analyzer` by shelling out and registering the result. Keeps
   zauditor's zero-dependency, cross-platform build; plugins can be written in
   any language.
2. **Go plugins (`plugin.Open`).** Lower overhead, but Linux/macOS only and
   requires exact toolchain matching — a poor fit for a single-binary tool.

Either route registers through the existing `core.Register`, which is why it is
mutex-guarded: registration after `init()` must be safe. No analyzer, no
renderer and no scoring code needs to change when this lands.

**(c) Deeper analysis.** The MVP is line- and regex-based. `RepoContext` and
`detect` are the two seams where a tree-sitter layer would enter: `detect`
already speaks a `Language` vocabulary, and parsed trees would be cached in
`RepoContext` next to file content. Analyzers that ask for an AST would opt in;
the ones that do not keep working unchanged.

## Status

Fully implemented: **size**, **tooling**.

Skeletons — registered, running, scoring, with the intended heuristics marked by
`TODO` at the point where they belong: **docs**, **tests**, **noise**,
**consistency**. They are deliberately visible in the report (each carries a
"skeleton analyzer" note under `--verbose`) rather than hidden behind a flag.

## Tests

```sh
go test ./...
```

`testdata/clean` and `testdata/messy` are fixture repositories: one with a
complete feedback loop and small files, one with a 1300-line god module, a
catch-all `utils.py`, an oversized React component and no tooling whatsoever.
