package analyzers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&toolingAnalyzer{}) }

type toolingAnalyzer struct{}

func (a *toolingAnalyzer) ID() string      { return "tooling" }
func (a *toolingAnalyzer) Name() string    { return "Feedback loop (tooling)" }
func (a *toolingAnalyzer) Weight() float64 { return 2.5 }

func (a *toolingAnalyzer) Description() string {
	return "Whether a change can be checked without a human: type checker, linter, formatter, test runner, CI. Without these an AI developer works blind and compensates by guessing."
}

// signal is one checkable element of the feedback loop. Scoring is simply the
// weighted share of satisfied signals, which keeps the dimension explainable:
// every lost point maps to exactly one missing tool.
type signal struct {
	id       string
	title    string
	weight   float64
	severity core.Severity
	ok       bool
	evidence string // path or key that satisfied it
	fix      string
}

func (a *toolingAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	if len(ctx.SourceFiles()) == 0 {
		return core.Skip("no Python/TS/JS/React/HTML source files found")
	}

	var sigs []signal
	sigs = append(sigs, projectSignals(ctx)...)
	if ctx.HasLang(detect.LangPython) {
		sigs = append(sigs, pythonSignals(ctx)...)
	}
	if ctx.HasAnyLang(detect.LangTypeScript, detect.LangTSX, detect.LangJavaScript, detect.LangJSX) {
		sigs = append(sigs, webSignals(ctx)...)
	}

	res := core.DimensionResult{}
	var total, got float64
	for _, s := range sigs {
		total += s.weight
		if s.ok {
			got += s.weight
			continue
		}
		res.Findings = append(res.Findings, core.Finding{
			Severity: s.severity,
			Path:     ".",
			Message:  "Missing: " + s.title,
			Fix:      s.fix,
		})
	}
	if total == 0 {
		return core.Skip("no applicable tooling signals for this repo")
	}
	res.Score = core.Clamp01(got / total)

	var have []string
	for _, s := range sigs {
		if s.ok {
			have = append(have, s.id)
		}
	}
	res.Notes = append(res.Notes, fmt.Sprintf("%d/%d feedback signals present: %s",
		len(have), len(sigs), joinOrNone(have)))
	return res
}

// --- signal groups -----------------------------------------------------------

func projectSignals(ctx *core.RepoContext) []signal {
	ciPath, hasCI := firstGlob(ctx,
		".github/workflows/*.yml", ".github/workflows/*.yaml",
		".gitlab-ci.yml", ".circleci/config.yml", "azure-pipelines.yml", "Jenkinsfile",
		".github/workflows/**/*.yml",
	)
	taskPath, hasTask := ctx.HasAny("Makefile", "makefile", "Justfile", "justfile", "Taskfile.yml", "Taskfile.yaml", "noxfile.py", "tox.ini")
	if !hasTask {
		if _, ok := packageScripts(ctx); ok {
			taskPath, hasTask = "package.json scripts", true
		}
	}
	precommitPath, hasPrecommit := ctx.HasAny(".pre-commit-config.yaml", ".pre-commit-config.yml", "lefthook.yml")
	if !hasPrecommit {
		precommitPath, hasPrecommit = firstGlob(ctx, ".husky/*")
	}

	return []signal{
		{
			id: "ci", title: "CI configuration", weight: 1.5, severity: core.SeverityWarn,
			ok: hasCI, evidence: ciPath,
			fix: "Add a CI workflow that runs lint + type check + tests on every push. CI is the only feedback loop that cannot be skipped by a hurried developer — human or model.",
		},
		{
			id: "taskrunner", title: "single entry point for build/lint/test", weight: 2, severity: core.SeverityCritical,
			ok: hasTask, evidence: taskPath,
			fix: "Add a Makefile (or package.json scripts) with `make lint`, `make test`, `make fmt`. One documented command per action means an agent does not have to reverse-engineer how to verify its own work.",
		},
		{
			id: "precommit", title: "pre-commit hooks", weight: 0.5, severity: core.SeverityInfo,
			ok: hasPrecommit, evidence: precommitPath,
			fix: "Add .pre-commit-config.yaml so formatting and linting run before the change ever reaches review.",
		},
	}
}

func pythonSignals(ctx *core.RepoContext) []signal {
	pyproject := string(ctx.ContentOf("pyproject.toml"))
	setupCfg := string(ctx.ContentOf("setup.cfg"))

	projPath, hasProj := ctx.HasAny("pyproject.toml", "setup.cfg", "setup.py")

	lintPath, hasLint := ctx.HasAny(".ruff.toml", "ruff.toml", ".flake8", ".pylintrc", "pylintrc")
	if !hasLint {
		if sec, ok := hasTOMLSection(pyproject, "tool.ruff", "tool.black", "tool.flake8", "tool.pylint", "tool.isort"); ok {
			lintPath, hasLint = "pyproject.toml ["+sec+"]", true
		} else if strings.Contains(setupCfg, "[flake8]") {
			lintPath, hasLint = "setup.cfg [flake8]", true
		}
	}

	typePath, hasType := ctx.HasAny("mypy.ini", ".mypy.ini", "pyrightconfig.json")
	if !hasType {
		if sec, ok := hasTOMLSection(pyproject, "tool.mypy", "tool.pyright", "tool.pyre"); ok {
			typePath, hasType = "pyproject.toml ["+sec+"]", true
		}
	}

	testPath, hasTest := ctx.HasAny("pytest.ini", "tox.ini", "conftest.py")
	if !hasTest {
		if sec, ok := hasTOMLSection(pyproject, "tool.pytest.ini_options", "tool.pytest"); ok {
			testPath, hasTest = "pyproject.toml ["+sec+"]", true
		}
	}
	if !hasTest {
		if len(ctx.Glob("**conftest.py")) > 0 {
			testPath, hasTest = "conftest.py", true
		}
	}

	depPath, hasDep := ctx.HasAny("poetry.lock", "uv.lock", "Pipfile.lock", "pdm.lock", "requirements.txt", "requirements-dev.txt", "requirements/base.txt")

	return []signal{
		{
			id: "py-project", title: "Python project metadata (pyproject.toml)", weight: 1, severity: core.SeverityWarn,
			ok: hasProj, evidence: projPath,
			fix: "Add pyproject.toml declaring the package and its tool configuration. It is the one file every Python tool now reads.",
		},
		{
			id: "py-lint", title: "Python linter/formatter config (ruff, black, flake8)", weight: 2, severity: core.SeverityCritical,
			ok: hasLint, evidence: lintPath,
			fix: "Add [tool.ruff] to pyproject.toml and wire `ruff check` + `ruff format` into the task runner. This is the cheapest feedback loop that exists.",
		},
		{
			id: "py-types", title: "Python type checker config (mypy/pyright)", weight: 1.5, severity: core.SeverityWarn,
			ok: hasType, evidence: typePath,
			fix: "Add [tool.mypy] to pyproject.toml, start with the strictest settings the codebase tolerates and tighten over time. Types are machine-readable documentation.",
		},
		{
			id: "py-testrunner", title: "Python test runner config (pytest)", weight: 1.5, severity: core.SeverityWarn,
			ok: hasTest, evidence: testPath,
			fix: "Add [tool.pytest.ini_options] with testpaths, so `pytest` runs the right thing from anywhere in the repo.",
		},
		{
			id: "py-deps", title: "pinned Python dependencies", weight: 1, severity: core.SeverityWarn,
			ok: hasDep, evidence: depPath,
			fix: "Commit a lockfile (uv.lock/poetry.lock) or a pinned requirements.txt so the environment is reproducible.",
		},
	}
}

func webSignals(ctx *core.RepoContext) []signal {
	pkgPath, hasPkg := ctx.HasAny("package.json")
	scripts, _ := packageScripts(ctx)

	hasTS := ctx.HasAnyLang(detect.LangTypeScript, detect.LangTSX)
	tsconfigPath, hasTSConfig := ctx.HasAny("tsconfig.json", "tsconfig.base.json", "jsconfig.json")

	eslintPath, hasESLint := ctx.HasAny(
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts",
		".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml",
		"biome.json", "biome.jsonc",
	)
	if !hasESLint {
		if _, ok := packageKey(ctx, "eslintConfig"); ok {
			eslintPath, hasESLint = "package.json eslintConfig", true
		}
	}

	fmtPath, hasFmt := ctx.HasAny(".prettierrc", ".prettierrc.json", ".prettierrc.js", ".prettierrc.cjs", ".prettierrc.yml", ".prettierrc.yaml", "prettier.config.js", "prettier.config.mjs", "biome.json")
	lockPath, hasLock := ctx.HasAny("package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "bun.lock")

	sigs := []signal{
		{
			id: "js-package", title: "package.json", weight: 1, severity: core.SeverityCritical,
			ok: hasPkg, evidence: pkgPath,
			fix: "Add package.json. Without it there is no declared way to install, build or test the frontend.",
		},
		{
			id: "js-script-test", title: "`test` script in package.json", weight: 1.5, severity: core.SeverityWarn,
			ok: hasScript(scripts, "test"), evidence: "scripts.test",
			fix: "Add a `test` script (vitest/jest). A test command that must be guessed will not be run.",
		},
		{
			id: "js-script-lint", title: "`lint` script in package.json", weight: 1.5, severity: core.SeverityWarn,
			ok: hasScript(scripts, "lint"), evidence: "scripts.lint",
			fix: "Add a `lint` script so a change can be checked with one command.",
		},
		{
			id: "js-script-build", title: "`build` script in package.json", weight: 0.5, severity: core.SeverityInfo,
			ok: hasScript(scripts, "build"), evidence: "scripts.build",
			fix: "Add a `build` script; a failing build is the cheapest end-to-end smoke test there is.",
		},
		{
			id: "js-lint", title: "ESLint/Biome configuration", weight: 1.5, severity: core.SeverityWarn,
			ok: hasESLint, evidence: eslintPath,
			fix: "Add eslint.config.js (flat config) with the recommended + react-hooks rulesets.",
		},
		{
			id: "js-format", title: "formatter configuration (Prettier/Biome)", weight: 0.5, severity: core.SeverityInfo,
			ok: hasFmt, evidence: fmtPath,
			fix: "Add a Prettier or Biome config so formatting stops being a review topic and diffs stay small.",
		},
		{
			id: "js-lock", title: "committed lockfile", weight: 0.5, severity: core.SeverityInfo,
			ok: hasLock, evidence: lockPath,
			fix: "Commit the lockfile so installs are reproducible.",
		},
	}

	if hasTS {
		strict, strictEvidence := tsStrict(ctx, tsconfigPath)
		sigs = append(sigs,
			signal{
				id: "ts-config", title: "tsconfig.json", weight: 1.5, severity: core.SeverityCritical,
				ok: hasTSConfig, evidence: tsconfigPath,
				fix: "Add tsconfig.json. TypeScript without a config is JavaScript with extra syntax.",
			},
			signal{
				id: "ts-strict", title: "TypeScript strict mode", weight: 1.5, severity: core.SeverityWarn,
				ok: strict, evidence: strictEvidence,
				fix: "Set \"strict\": true in tsconfig.json. Non-strict TS silently accepts exactly the mistakes a type checker exists to catch.",
			},
		)
	}
	return sigs
}

// --- helpers -----------------------------------------------------------------

type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
}

func packageScripts(ctx *core.RepoContext) (map[string]string, bool) {
	data := ctx.ContentOf("package.json")
	if len(data) == 0 {
		return nil, false
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, false
	}
	return pkg.Scripts, len(pkg.Scripts) > 0
}

func packageKey(ctx *core.RepoContext, key string) (json.RawMessage, bool) {
	data := ctx.ContentOf("package.json")
	if len(data) == 0 {
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	v, ok := raw[key]
	return v, ok
}

func hasScript(scripts map[string]string, name string) bool {
	v, ok := scripts[name]
	return ok && strings.TrimSpace(v) != ""
}

var (
	jsoncLineComment  = regexp.MustCompile(`(?m)^\s*//.*$`)
	jsoncBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	tsStrictRE        = regexp.MustCompile(`"strict"\s*:\s*true`)
	tsExtendsRE       = regexp.MustCompile(`"extends"\s*:`)
)

// tsStrict answers "will the type checker actually complain?". tsconfig is
// JSONC and may inherit from a base config we cannot resolve without a
// resolver; an explicit "extends" is accepted as a maybe rather than a no,
// because false alarms here would train users to ignore the dimension.
func tsStrict(ctx *core.RepoContext, tsconfigPath string) (bool, string) {
	if tsconfigPath == "" {
		return false, ""
	}
	raw := string(ctx.ContentOf(tsconfigPath))
	if raw == "" {
		return false, ""
	}
	clean := jsoncLineComment.ReplaceAllString(raw, "")
	clean = jsoncBlockComment.ReplaceAllString(clean, "")
	if tsStrictRE.MatchString(clean) {
		return true, tsconfigPath + ` "strict": true`
	}
	if tsExtendsRE.MatchString(clean) {
		return true, tsconfigPath + " (inherited via extends — verify manually)"
	}
	return false, ""
}

// hasTOMLSection looks for a [section] header in raw TOML text. The MVP does
// not parse TOML; a header match is precise enough for presence detection and
// keeps the binary dependency-free.
func hasTOMLSection(toml string, sections ...string) (string, bool) {
	for _, s := range sections {
		if strings.Contains(toml, "["+s+"]") {
			return s, true
		}
	}
	return "", false
}

func firstGlob(ctx *core.RepoContext, patterns ...string) (string, bool) {
	for _, p := range patterns {
		if m := ctx.Glob(p); len(m) > 0 {
			return m[0].Path, true
		}
	}
	return "", false
}

func joinOrNone(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ", ")
}
