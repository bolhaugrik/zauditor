package analyzers

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

func init() { core.Register(&consistencyAnalyzer{}) }

// consistencyAnalyzer is a SKELETON. The .js/.ts mixing check is implemented;
// naming conventions and error-handling patterns are TODO.
type consistencyAnalyzer struct{}

func (a *consistencyAnalyzer) ID() string      { return "consistency" }
func (a *consistencyAnalyzer) Name() string    { return "Internal consistency" }
func (a *consistencyAnalyzer) Weight() float64 { return 1.0 }

func (a *consistencyAnalyzer) Description() string {
	return "Whether the repo teaches one way of doing things. Every parallel convention doubles the context needed before writing a single line."
}

var (
	snakeRE = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)+$`)
	camelRE = regexp.MustCompile(`^[a-z0-9]+([A-Z][a-z0-9]*)+$`)
	// *.config.js / *.config.ts and friends: build tooling, not application code.
	// A prefix is required: vite.config.ts is tooling, src/config.ts is code.
	toolConfigRE = regexp.MustCompile(`\.config\.(js|cjs|mjs|ts|mts|cts)$`)
)

// isToolConfigFile reports whether a file is build/tool configuration rather
// than application code. These files follow the conventions of the tool that
// reads them, not of the codebase, so counting them as part of a layer
// produces false drift signals.
func isToolConfigFile(p string) bool {
	base := path.Base(p)
	if strings.HasPrefix(base, ".") {
		return true // .eslintrc.js, .prettierrc.js, …
	}
	if toolConfigRE.MatchString(base) {
		return true
	}
	switch base {
	case "gulpfile.js", "Gruntfile.js", "webpack.mix.js", "karma.conf.js", "protractor.conf.js":
		return true
	}
	return false
}

func (a *consistencyAnalyzer) Analyze(ctx *core.RepoContext) core.DimensionResult {
	src := ctx.SourceFiles()
	if len(src) == 0 {
		return core.Skip("no source files found")
	}

	res := core.DimensionResult{}
	var penalties []float64

	// 1. Mixed .js/.ts in the same directory: an agent must check per file
	//    which language rules apply.
	mixedDirs := 0
	tsDirs := 0
	for _, dir := range ctx.Dirs() {
		var ts, js int
		for _, f := range ctx.DirFiles(dir) {
			// Build tooling is expected to be .js even in a strict TS project
			// (vite.config.ts next to tailwind.config.js is normal, not drift).
			if isToolConfigFile(f.Path) {
				continue
			}
			switch f.Lang {
			case detect.LangTypeScript, detect.LangTSX:
				ts++
			case detect.LangJavaScript, detect.LangJSX:
				js++
			}
		}
		if ts > 0 {
			tsDirs++
		}
		if ts > 0 && js > 0 {
			mixedDirs++
			label := dir
			if label == "." {
				label = "(repo root)"
			}
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityWarn,
				Path:     dir,
				Message:  fmt.Sprintf("Mixed JS and TS in one layer: %d TS and %d JS files in %s", ts, js, label),
				Fix:      "Finish the migration directory by directory and add `allowJs: false` once a layer is clean. A half-typed layer gives the weakest guarantee of both worlds.",
			})
		}
	}
	if tsDirs > 0 {
		penalties = append(penalties, core.Clamp01(float64(mixedDirs)/float64(tsDirs)))
	}

	// 2. Filename convention drift within a language.
	for _, lang := range []detect.Language{detect.LangPython, detect.LangTypeScript, detect.LangTSX} {
		files := ctx.ByLang(lang)
		if len(files) < 5 {
			continue
		}
		var snake, camel int
		for _, f := range files {
			stem := strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
			switch {
			case snakeRE.MatchString(stem):
				snake++
			case camelRE.MatchString(stem):
				camel++
			}
		}
		total := snake + camel
		if total < 5 {
			continue
		}
		minority := min(snake, camel)
		share := float64(minority) / float64(total)
		if share > 0.15 {
			penalties = append(penalties, core.Clamp01(share*2))
			res.Findings = append(res.Findings, core.Finding{
				Severity: core.SeverityInfo,
				Path:     ".",
				Message:  fmt.Sprintf("Mixed filename conventions in %s: %d snake_case vs %d camelCase", lang, snake, camel),
				Fix:      "Pick one convention, write it down in the agent instructions file, and rename the minority in a single mechanical commit.",
			})
		}
	}

	// TODO: identifier-level naming consistency (function/variable casing per
	// language convention), which needs at least a lexer to avoid matching
	// strings and comments.
	// TODO: parallel error-handling patterns — e.g. Python code that mixes
	// bare `except:`, custom exception hierarchies and returned error tuples;
	// TS code that mixes thrown errors, Result-like returns and rejected
	// promises. Detecting >1 dominant pattern per layer is the goal.
	// TODO: import-style drift (relative vs absolute/aliased imports).
	// TODO: API-shape drift across route handlers / service classes.
	// TODO: multiple state-management or data-fetching approaches in one React
	// app (Redux + Zustand + raw fetch is three ways to answer one question).

	if len(penalties) == 0 {
		res.Score = 1
	} else {
		var sum float64
		for _, p := range penalties {
			sum += p
		}
		res.Score = core.Clamp01(1 - sum/float64(len(penalties)))
	}

	res.Notes = append(res.Notes, "skeleton analyzer: JS/TS mixing and filename casing only; error-handling and import-style checks are TODO (see consistency.go)")
	return res
}
