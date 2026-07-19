package walker

import "testing"

func TestDefaultIgnoresApplyAtAnyDepth(t *testing.T) {
	m := NewMatcher()
	for _, p := range []string{"node_modules/react/index.js", "src/__pycache__/a.pyc", ".git/config", "app/.venv/lib/x.py"} {
		if !m.Match(p, false) {
			t.Errorf("Match(%q) = false, want true (default ignore)", p)
		}
	}
	for _, p := range []string{"src/app.py", "web/node_modules.md"} {
		if m.Match(p, false) {
			t.Errorf("Match(%q) = true, want false", p)
		}
	}
}

func TestGitignorePatterns(t *testing.T) {
	m := NewMatcher()
	for _, line := range []string{
		"# comment",
		"*.log",
		"/secrets.txt",
		"tmp/",
		"docs/**/draft.md",
		"!keep.log",
	} {
		m.AddPattern(line)
	}

	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"deep/nested/app.log", false, true},
		{"keep.log", false, false},         // negation
		{"secrets.txt", false, true},       // anchored
		{"app/secrets.txt", false, false},  // anchored: root only
		{"tmp", true, true},                // dir-only pattern
		{"tmp/cache/x.bin", false, true},   // everything under an ignored dir
		{"docs/a/b/draft.md", false, true}, // ** spans directories
		{"docs/draft.md", false, true},     // ** matches zero directories
		{"src/main.py", false, false},
	}
	for _, tt := range tests {
		if got := m.Match(tt.path, tt.isDir); got != tt.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestCountLines(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n\n", 2},
	} {
		if got := countLines([]byte(tt.in)); got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
