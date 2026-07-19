package walker

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultIgnores are always skipped, regardless of .gitignore. These are
// directories whose contents would drown out the signal in every metric.
var DefaultIgnores = []string{
	".git", ".hg", ".svn",
	"node_modules", "vendor", "bower_components",
	"dist", "build", "out", ".next", ".nuxt", ".output",
	"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox",
	".venv", "venv", "env", ".env.d", "site-packages", ".eggs",
	".idea", ".vscode-test", "coverage", ".coverage", "htmlcov",
	// Fixture trees are deliberately unrepresentative — often deliberately
	// bad — and would poison every metric if counted as application code.
	"testdata", "__snapshots__", "__fixtures__",
	".terraform", ".gradle", "target",
}

// DefaultIgnorePatterns are gitignore-style rules that cannot be expressed as
// a single directory name. Agent tooling likes to keep a full second copy of
// the repo under a dotdir; counted as source it doubles every metric.
var DefaultIgnorePatterns = []string{
	".claude/worktrees/",
	".worktrees/",
	".git/worktrees/",
}

type pattern struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// Matcher evaluates gitignore-style rules plus the built-in default list.
type Matcher struct {
	defaults map[string]bool
	patterns []pattern
}

// NewMatcher builds a matcher seeded with the default ignore list.
func NewMatcher() *Matcher {
	m := &Matcher{defaults: make(map[string]bool, len(DefaultIgnores))}
	for _, d := range DefaultIgnores {
		m.defaults[d] = true
	}
	for _, p := range DefaultIgnorePatterns {
		m.AddPattern(p)
	}
	return m
}

// LoadGitignore appends the rules of a .gitignore file, if it exists. Missing
// files are not an error: most repos we audit will only have some of them.
func (m *Matcher) LoadGitignore(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m.AddPattern(sc.Text())
	}
	return sc.Err()
}

// AddPattern registers a single gitignore-style line.
func (m *Matcher) AddPattern(line string) {
	line = strings.TrimRight(line, " \t\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}

	p := pattern{}
	if strings.HasPrefix(line, "!") {
		p.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return
	}

	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	// A pattern containing a slash anywhere is relative to the repo root;
	// otherwise it matches at any depth.
	if strings.Contains(line, "/") {
		anchored = true
	}

	re, err := compile(line, anchored)
	if err != nil {
		return // silently skip patterns we cannot express
	}
	p.re = re
	m.patterns = append(m.patterns, p)
}

// compile turns a gitignore glob into an anchored regexp over a slash-separated
// relative path.
func compile(glob string, anchored bool) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				// "**/" consumes any number of leading dirs, "**" any suffix.
				if i+2 < len(glob) && glob[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '[':
			// Character classes pass through mostly untouched.
			end := strings.IndexByte(glob[i:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(glob[i : i+end+1])
			i += end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	// Ignoring a directory implicitly ignores everything under it.
	b.WriteString("(?:/.*)?$")
	return regexp.Compile(b.String())
}

// Match reports whether a repo-relative, slash-separated path is ignored.
func (m *Matcher) Match(rel string, isDir bool) bool {
	for _, seg := range strings.Split(rel, "/") {
		if m.defaults[seg] {
			return true
		}
	}

	ignored := false
	for _, p := range m.patterns {
		if p.dirOnly && !isDir && !strings.Contains(rel, "/") {
			continue
		}
		if p.re.MatchString(rel) {
			ignored = !p.negate
		}
	}
	return ignored
}

// RelSlash converts an absolute path into a repo-relative, slash-separated one.
func RelSlash(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
