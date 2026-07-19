package core

import (
	"bytes"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"zauditor/config"
	"zauditor/internal/detect"
)

// FileInfo is one file in the audited tree. It is filled once by the walker and
// shared read-only by every analyzer.
type FileInfo struct {
	Path     string          `json:"path"` // repo-relative, slash-separated
	Abs      string          `json:"-"`
	Lang     detect.Language `json:"lang"`
	Lines    int             `json:"lines"`
	Size     int64           `json:"size"`
	ModTime  time.Time       `json:"-"`
	Binary   bool            `json:"-"`
	TooLarge bool            `json:"-"`
}

// LangStat aggregates one language across the repo.
type LangStat struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// RepoContext is the single shared model of the repository. It is built once,
// before any analyzer runs, and analyzers must not touch the filesystem
// directly — everything goes through here so the cost is paid once and a future
// AST layer has exactly one place to hook into.
type RepoContext struct {
	Root      string
	Files     []FileInfo
	LangStats map[detect.Language]LangStat
	Config    *config.Config

	// NewestCodeMod is the most recent mtime among source files, used by the
	// docs freshness heuristic.
	NewestCodeMod time.Time

	byPath  map[string]*FileInfo
	byLang  map[detect.Language][]*FileInfo
	dirs    map[string][]*FileInfo
	mu      sync.Mutex
	content map[string][]byte
}

// NewRepoContext assembles the shared model from a walked file list.
func NewRepoContext(root string, files []FileInfo, cfg *config.Config) *RepoContext {
	if cfg == nil {
		cfg = config.Default()
	}
	c := &RepoContext{
		Root:      root,
		Files:     files,
		LangStats: map[detect.Language]LangStat{},
		Config:    cfg,
		byPath:    make(map[string]*FileInfo, len(files)),
		byLang:    map[detect.Language][]*FileInfo{},
		dirs:      map[string][]*FileInfo{},
		content:   map[string][]byte{},
	}
	for i := range c.Files {
		f := &c.Files[i]
		c.byPath[f.Path] = f
		c.byLang[f.Lang] = append(c.byLang[f.Lang], f)
		c.dirs[path.Dir(f.Path)] = append(c.dirs[path.Dir(f.Path)], f)

		st := c.LangStats[f.Lang]
		st.Files++
		st.Lines += f.Lines
		c.LangStats[f.Lang] = st

		if detect.IsSource(f.Lang) && f.ModTime.After(c.NewestCodeMod) {
			c.NewestCodeMod = f.ModTime
		}
	}
	return c
}

// Lookup returns a file by its repo-relative path.
func (c *RepoContext) Lookup(rel string) (*FileInfo, bool) {
	f, ok := c.byPath[rel]
	return f, ok
}

// Has reports whether a repo-relative path exists in the audited set.
func (c *RepoContext) Has(rel string) bool {
	_, ok := c.byPath[rel]
	return ok
}

// HasAny reports whether any of the given paths exist, returning the first hit.
func (c *RepoContext) HasAny(rels ...string) (string, bool) {
	for _, r := range rels {
		if c.Has(r) {
			return r, true
		}
	}
	return "", false
}

// ByLang returns every file of a language, in path order.
func (c *RepoContext) ByLang(l detect.Language) []*FileInfo {
	return c.byLang[l]
}

// SourceFiles returns every application-code file, in path order.
func (c *RepoContext) SourceFiles() []*FileInfo {
	var out []*FileInfo
	for i := range c.Files {
		if detect.IsSource(c.Files[i].Lang) {
			out = append(out, &c.Files[i])
		}
	}
	return out
}

// HasLang reports whether the repo contains any file of a language.
func (c *RepoContext) HasLang(l detect.Language) bool {
	return c.LangStats[l].Files > 0
}

// HasAnyLang reports whether any of the languages is present.
func (c *RepoContext) HasAnyLang(langs ...detect.Language) bool {
	for _, l := range langs {
		if c.HasLang(l) {
			return true
		}
	}
	return false
}

// Dirs returns directory paths (repo-relative) in sorted order.
func (c *RepoContext) Dirs() []string {
	out := make([]string, 0, len(c.dirs))
	for d := range c.dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// DirFiles returns the files directly inside a directory.
func (c *RepoContext) DirFiles(dir string) []*FileInfo {
	return c.dirs[dir]
}

// MatchPaths returns every file path matching a regexp, in path order. Used by
// analyzers that look for conventions rather than exact filenames.
func (c *RepoContext) MatchPaths(re *regexp.Regexp) []*FileInfo {
	var out []*FileInfo
	for i := range c.Files {
		if re.MatchString(c.Files[i].Path) {
			out = append(out, &c.Files[i])
		}
	}
	return out
}

// Glob matches files against a simple pattern where "*" does not cross "/" and
// "**" does. It exists so analyzers can look for ".github/workflows/*.yml"
// without each of them reimplementing path matching.
func (c *RepoContext) Glob(pattern string) []*FileInfo {
	re, err := globRE(pattern)
	if err != nil {
		return nil
	}
	return c.MatchPaths(re)
}

func globRE(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Content lazily reads and caches a file's bytes. Files the walker marked as
// binary or oversized return nil, so callers can treat "no content" uniformly.
func (c *RepoContext) Content(f *FileInfo) []byte {
	if f == nil || f.Binary || f.TooLarge {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if data, ok := c.content[f.Path]; ok {
		return data
	}
	data, err := os.ReadFile(f.Abs)
	if err != nil {
		data = nil
	}
	// Windows editors happily prepend a UTF-8 BOM to JSON and TOML; stripping
	// it here means no analyzer has to think about it.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	c.content[f.Path] = data
	return data
}

// ContentOf is Content by path.
func (c *RepoContext) ContentOf(rel string) []byte {
	f, ok := c.Lookup(rel)
	if !ok {
		return nil
	}
	return c.Content(f)
}

// LinesOf returns a file's content split into lines. The split is recomputed
// per call; analyzers that need it repeatedly should hold onto the result.
func (c *RepoContext) LinesOf(f *FileInfo) []string {
	data := c.Content(f)
	if len(data) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
