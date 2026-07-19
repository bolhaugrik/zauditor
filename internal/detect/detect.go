// Package detect maps files to languages. It is intentionally small and
// side-effect free so that a future AST/tree-sitter layer can reuse the same
// Language vocabulary without touching the analyzers.
package detect

import (
	"path"
	"strings"
)

// Language is a stable identifier for a source language. Analyzers and config
// files reference these strings, so they must not change casually.
type Language string

const (
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangTSX        Language = "tsx"
	LangJavaScript Language = "javascript"
	LangJSX        Language = "jsx"
	LangHTML       Language = "html"
	LangCSS        Language = "css"
	LangJSON       Language = "json"
	LangYAML       Language = "yaml"
	LangMarkdown   Language = "markdown"
	LangTOML       Language = "toml"
	LangShell      Language = "shell"
	LangGo         Language = "go"
	LangOther      Language = "other"
)

// SourceLanguages are the languages zauditor treats as "application code".
// Everything else counts as docs, config or noise.
var SourceLanguages = []Language{
	LangPython, LangTypeScript, LangTSX, LangJavaScript, LangJSX, LangHTML,
}

var byExt = map[string]Language{
	".py":    LangPython,
	".pyi":   LangPython,
	".ts":    LangTypeScript,
	".mts":   LangTypeScript,
	".cts":   LangTypeScript,
	".tsx":   LangTSX,
	".js":    LangJavaScript,
	".mjs":   LangJavaScript,
	".cjs":   LangJavaScript,
	".jsx":   LangJSX,
	".html":  LangHTML,
	".htm":   LangHTML,
	".css":   LangCSS,
	".scss":  LangCSS,
	".less":  LangCSS,
	".json":  LangJSON,
	".jsonc": LangJSON,
	".yaml":  LangYAML,
	".yml":   LangYAML,
	".md":    LangMarkdown,
	".mdx":   LangMarkdown,
	".toml":  LangTOML,
	".sh":    LangShell,
	".bash":  LangShell,
	".go":    LangGo,
}

var byShebang = map[string]Language{
	"python":  LangPython,
	"python3": LangPython,
	"node":    LangJavaScript,
	"sh":      LangShell,
	"bash":    LangShell,
	"zsh":     LangShell,
}

// FromPath resolves a language from the file extension alone.
func FromPath(p string) Language {
	if l, ok := byExt[strings.ToLower(path.Ext(p))]; ok {
		return l
	}
	return LangOther
}

// FromShebang resolves a language from the first line of a file. It is only
// consulted for extension-less files, so that we never pay the read cost for
// files we can already classify.
func FromShebang(firstLine string) Language {
	if !strings.HasPrefix(firstLine, "#!") {
		return LangOther
	}
	fields := strings.Fields(strings.TrimPrefix(firstLine, "#!"))
	for i := len(fields) - 1; i >= 0; i-- {
		base := path.Base(strings.TrimSpace(fields[i]))
		if l, ok := byShebang[base]; ok {
			return l
		}
	}
	return LangOther
}

// IsSource reports whether the language is application code we score.
func IsSource(l Language) bool {
	for _, s := range SourceLanguages {
		if s == l {
			return true
		}
	}
	return false
}

// IsFrontendComponent reports whether the language is typically used for
// React-style component files (relevant for size thresholds).
func IsFrontendComponent(l Language) bool {
	return l == LangTSX || l == LangJSX
}
