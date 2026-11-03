package analysis

import (
	"go/ast"
	"slices"
	"sync"
	"weak"

	"golang.org/x/tools/go/ast/inspector"
)

// inspectorCache shares the inspector of a package's file list between the
// views that index it. It holds inspectors weakly: one lives as long as a
// reference index uses it.
type inspectorCache struct {
	mu    sync.Mutex
	byDir map[string][]*cachedInspector
}

type cachedInspector struct {
	files []*ast.File

	mu      sync.Mutex // held while the inspector is built
	inspect weak.Pointer[inspector.Inspector]
}

func newInspectorCache() *inspectorCache {
	return &inspectorCache{byDir: make(map[string][]*cachedInspector)}
}

// get returns the inspector over exactly files, building it unless one is
// alive. A nil cache builds a fresh one.
func (c *inspectorCache) get(dir string, files []*ast.File) *inspector.Inspector {
	if c == nil {
		return inspector.New(files)
	}

	e := c.entry(dir, files)
	e.mu.Lock()
	defer e.mu.Unlock()

	ins := e.inspect.Value()
	if ins != nil {
		return ins
	}

	ins = inspector.New(files)
	e.inspect = weak.Make(ins)

	return ins
}

// entry returns dir's entry for files, adding it if needed, and forgets dir's
// other entries whose inspector is gone.
func (c *inspectorCache) entry(dir string, files []*ast.File) *cachedInspector {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found *cachedInspector

	kept := c.byDir[dir][:0]
	for _, e := range c.byDir[dir] {
		switch {
		case slices.Equal(e.files, files):
			found = e
		case e.dead():
			continue
		}

		kept = append(kept, e)
	}

	if found == nil {
		found = &cachedInspector{files: files}
		kept = append(kept, found)
	}

	c.byDir[dir] = kept

	return found
}

// dead reports whether e's inspector is gone and nobody is building it.
func (e *cachedInspector) dead() bool {
	if !e.mu.TryLock() {
		return false
	}
	defer e.mu.Unlock()

	return e.inspect.Value() == nil
}
