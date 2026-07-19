package core

import (
	"fmt"
	"sort"
	"sync"
)

// The registry is the extensibility seam. Analyzers self-register from their
// own init(), so the core never needs to know the set of analyzers that exist:
//
//	// internal/analyzers/size.go
//	func init() { core.Register(&sizeAnalyzer{}) }
//
// Registration is guarded by a mutex so a future dynamic loader (Go plugins,
// or an out-of-process "--plugin ./mine" bridge) can register after init()
// without racing the CLI.
var (
	mu       sync.RWMutex
	registry []Analyzer
	seen     = map[string]bool{}
)

// Register adds an analyzer. Duplicate IDs panic: two analyzers answering to
// the same --only key is a programming error, and failing loudly at init time
// is cheaper than debugging a silently dropped dimension.
func Register(a Analyzer) {
	mu.Lock()
	defer mu.Unlock()
	id := a.ID()
	if id == "" {
		panic("core.Register: analyzer with empty ID")
	}
	if seen[id] {
		panic(fmt.Sprintf("core.Register: duplicate analyzer ID %q", id))
	}
	seen[id] = true
	registry = append(registry, a)
}

// All returns every registered analyzer, sorted by ID so that report ordering
// does not depend on package initialisation order.
func All() []Analyzer {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Analyzer, len(registry))
	copy(out, registry)
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Get looks up a single analyzer by ID.
func Get(id string) (Analyzer, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, a := range registry {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

// Selection describes which analyzers should run. An empty Only means "all
// registered"; Skip and Disabled subtract from that set.
type Selection struct {
	Only     []string
	Skip     []string
	Disabled map[string]bool // from config: analyzers[id].enabled = false
}

// Select resolves a Selection against the registry. Unknown IDs are returned
// separately so the CLI can fail fast on a typo rather than silently running
// fewer checks than the user asked for.
func Select(sel Selection) (chosen []Analyzer, unknown []string) {
	all := All()
	known := map[string]bool{}
	for _, a := range all {
		known[a.ID()] = true
	}
	for _, id := range append(append([]string{}, sel.Only...), sel.Skip...) {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return nil, unknown
	}

	include := map[string]bool{}
	if len(sel.Only) == 0 {
		for id := range known {
			include[id] = true
		}
	} else {
		for _, id := range sel.Only {
			include[id] = true
		}
	}
	for _, id := range sel.Skip {
		delete(include, id)
	}
	for id, off := range sel.Disabled {
		if off {
			delete(include, id)
		}
	}

	for _, a := range all {
		if include[a.ID()] {
			chosen = append(chosen, a)
		}
	}
	return chosen, nil
}

// resetRegistryForTest clears global state. Only tests use it.
func resetRegistryForTest() {
	mu.Lock()
	defer mu.Unlock()
	registry = nil
	seen = map[string]bool{}
}
