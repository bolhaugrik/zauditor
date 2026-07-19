// Package config holds the default thresholds and weights, plus the optional
// JSON override file. It depends on nothing but detect, so both core and the
// analyzers can import it freely.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"zauditor/internal/detect"
)

// SizeThreshold defines when a file of a given language is too big to hold in
// working memory — human or model.
type SizeThreshold struct {
	Warn     int `json:"warn"`
	Critical int `json:"critical"`
}

// AnalyzerConfig overrides registry defaults per analyzer.
type AnalyzerConfig struct {
	// Enabled defaults to true; a JSON file must say false explicitly.
	Enabled *bool `json:"enabled,omitempty"`
	// Weight overrides Analyzer.Weight() when non-nil.
	Weight *float64 `json:"weight,omitempty"`
}

// Config is the whole tunable surface of an audit run.
type Config struct {
	// SizeThresholds is keyed by detect.Language.
	SizeThresholds map[detect.Language]SizeThreshold `json:"size_thresholds"`
	// SizeDefault applies to source languages with no explicit entry.
	SizeDefault SizeThreshold `json:"size_default"`
	// DirWidthWarn is the number of files in a single directory above which
	// navigation starts costing real context.
	DirWidthWarn int `json:"dir_width_warn"`
	// CatchAllNames are basenames that tend to become dumping grounds.
	CatchAllNames []string `json:"catch_all_names"`
	// CatchAllMinLines is how big such a file must be before we complain.
	CatchAllMinLines int `json:"catch_all_min_lines"`
	// DocsStaleDays is how far docs may lag behind code before we flag them.
	DocsStaleDays int `json:"docs_stale_days"`
	// MinTestRatio is the test-file / source-file ratio considered healthy.
	MinTestRatio float64 `json:"min_test_ratio"`
	// Analyzers holds per-analyzer overrides, keyed by analyzer ID.
	Analyzers map[string]AnalyzerConfig `json:"analyzers"`
}

// Default returns the built-in configuration. It is a fresh value on every
// call, so callers may mutate it safely.
func Default() *Config {
	return &Config{
		SizeThresholds: map[detect.Language]SizeThreshold{
			detect.LangPython:     {Warn: 500, Critical: 1000},
			detect.LangTypeScript: {Warn: 400, Critical: 800},
			detect.LangJavaScript: {Warn: 400, Critical: 800},
			// React components carry JSX, state and effects at once; they get
			// unreviewable earlier than plain modules.
			detect.LangTSX:  {Warn: 300, Critical: 600},
			detect.LangJSX:  {Warn: 300, Critical: 600},
			detect.LangHTML: {Warn: 600, Critical: 1200},
		},
		SizeDefault:      SizeThreshold{Warn: 500, Critical: 1000},
		DirWidthWarn:     25,
		CatchAllNames:    []string{"utils", "util", "utilities", "helpers", "helper", "common", "commons", "misc", "shared", "core", "base", "stuff"},
		CatchAllMinLines: 150,
		DocsStaleDays:    90,
		MinTestRatio:     0.2,
		Analyzers:        map[string]AnalyzerConfig{},
	}
}

// Threshold resolves the size limits for a language.
func (c *Config) Threshold(l detect.Language) SizeThreshold {
	if t, ok := c.SizeThresholds[l]; ok {
		return t
	}
	return c.SizeDefault
}

// Enabled reports whether an analyzer is switched on. Unknown IDs are on:
// config is an override mechanism, not an allow-list.
func (c *Config) Enabled(id string) bool {
	if ac, ok := c.Analyzers[id]; ok && ac.Enabled != nil {
		return *ac.Enabled
	}
	return true
}

// WeightFor returns the configured weight, or def when unset.
func (c *Config) WeightFor(id string, def float64) float64 {
	if ac, ok := c.Analyzers[id]; ok && ac.Weight != nil {
		return *ac.Weight
	}
	return def
}

// Disabled returns the set of explicitly switched-off analyzers, in the shape
// core.Selection expects.
func (c *Config) Disabled() map[string]bool {
	out := map[string]bool{}
	for id, ac := range c.Analyzers {
		if ac.Enabled != nil && !*ac.Enabled {
			out[id] = true
		}
	}
	return out
}

// Load reads a JSON override file on top of the defaults. Only the keys present
// in the file are replaced, so a two-line config stays a two-line config.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Analyzers == nil {
		cfg.Analyzers = map[string]AnalyzerConfig{}
	}
	return cfg, nil
}
