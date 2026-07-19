// Package walker traverses a repository, honouring .gitignore and a built-in
// default ignore list, and produces the flat file list the RepoContext is built
// from. File stats are gathered in parallel but the result is always sorted, so
// two runs over the same tree yield byte-identical output.
package walker

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"zauditor/internal/core"
	"zauditor/internal/detect"
)

// MaxReadSize caps how much of a file we are willing to read for line
// counting. Anything larger is recorded but not measured.
const MaxReadSize = 4 << 20 // 4 MiB

// Options tune a single walk.
type Options struct {
	// FollowSymlinks is off by default: symlinked trees are a classic way to
	// double-count a repo into itself.
	FollowSymlinks bool
	// Concurrency defaults to GOMAXPROCS when zero.
	Concurrency int
}

// Walk traverses root and returns the non-ignored files, sorted by path.
func Walk(root string, opts Options) ([]core.FileInfo, error) {
	root = filepath.Clean(root)

	m := NewMatcher()
	// Root-level .gitignore only: nested ones are rare enough in the repos we
	// audit that supporting them is not worth the precedence complexity yet.
	if err := m.LoadGitignore(filepath.Join(root, ".gitignore")); err != nil {
		return nil, err
	}

	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directories are skipped, not fatal: audits run on
			// trees we do not control.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, relErr := RelSlash(root, p)
		if relErr != nil {
			return nil
		}
		if m.Match(rel, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			if d.Type()&os.ModeSymlink == 0 || !opts.FollowSymlinks {
				return nil
			}
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(paths)
	return stat(root, paths, opts), nil
}

// stat fills in size, line count and language for every path, in parallel.
// Results are written to a pre-sized slice by index, so ordering is preserved.
func stat(root string, paths []string, opts Options) []core.FileInfo {
	n := opts.Concurrency
	if n <= 0 {
		n = runtime.GOMAXPROCS(0)
	}
	if n > len(paths) {
		n = len(paths)
	}
	if n < 1 {
		return nil
	}

	out := make([]core.FileInfo, len(paths))
	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				out[i] = statOne(root, paths[i])
			}
		}()
	}
	for i := range paths {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

func statOne(root, rel string) core.FileInfo {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	fi := core.FileInfo{Path: rel, Abs: abs, Lang: detect.FromPath(rel)}

	st, err := os.Stat(abs)
	if err != nil {
		return fi
	}
	fi.Size = st.Size()
	fi.ModTime = st.ModTime()

	if fi.Size > MaxReadSize {
		fi.TooLarge = true
		return fi
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return fi
	}
	if isBinary(data) {
		fi.Binary = true
		return fi
	}
	fi.Lines = countLines(data)

	// Extension-less files get a second chance via the shebang.
	if fi.Lang == detect.LangOther && filepath.Ext(rel) == "" {
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			fi.Lang = detect.FromShebang(strings.TrimSpace(string(data[:nl])))
		}
	}
	return fi
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++ // trailing line without newline
	}
	return n
}

// isBinary uses the same heuristic as git: a NUL byte in the first 8000 bytes.
func isBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	return bytes.IndexByte(data, 0) >= 0
}
