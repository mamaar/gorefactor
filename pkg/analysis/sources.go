package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// SourceFile is one .go file as found on disk, independent of any build config.
type SourceFile struct {
	Path    string
	Dir     string
	Name    string
	Content []byte
	AST     *ast.File // may be nil, or partial when Err is set
	Err     error     // read or parse error

	// Generated is set when AST carries a Go generated-code header.
	Generated bool
}

// SourceTree records every .go file under a root, parsed into one FileSet.
type SourceTree struct {
	fset *token.FileSet

	mu     sync.RWMutex
	byDir  map[string][]*SourceFile // sorted by name
	byPath map[string]*SourceFile

	inspectors *inspectorCache
}

func NewSourceTree(fset *token.FileSet) *SourceTree {
	return &SourceTree{
		fset:       fset,
		byDir:      make(map[string][]*SourceFile),
		byPath:     make(map[string]*SourceFile),
		inspectors: newInspectorCache(),
	}
}

// Dir returns the files recorded for dir, sorted by name.
func (t *SourceTree) Dir(dir string) []*SourceFile {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.byDir[absDir(dir)]
}

// File returns the file recorded for path, or nil.
func (t *SourceTree) File(path string) *SourceFile {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.byPath[absDir(path)]
}

// Dirs returns every loaded directory, sorted.
func (t *SourceTree) Dirs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	dirs := make([]string, 0, len(t.byDir))
	for dir := range t.byDir {
		dirs = append(dirs, dir)
	}

	slices.Sort(dirs)

	return dirs
}

func (t *SourceTree) loaded(dir string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	_, ok := t.byDir[dir]

	return ok
}

// AddDir reads and parses the .go files directly in dir. A directory that is
// already loaded is left untouched.
func (t *SourceTree) AddDir(dir string) error {
	dir = absDir(dir)
	if t.loaded(dir) {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("add dir: %w", err)
	}

	var files []*SourceFile

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		files = append(files, t.readFile(dir, entry.Name()))
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	_, ok := t.byDir[dir]
	if ok {
		return nil
	}

	t.byDir[dir] = files
	for _, f := range files {
		t.byPath[f.Path] = f
	}

	return nil
}

// Replace re-reads a recorded file from disk and returns the new record.
func (t *SourceTree) Replace(path string) (*SourceFile, error) {
	path = absDir(path)
	dir, name := filepath.Split(path)
	fresh := t.readFile(filepath.Clean(dir), name)

	t.mu.Lock()
	defer t.mu.Unlock()

	files := slices.Clone(t.byDir[fresh.Dir])

	i, ok := slices.BinarySearchFunc(files, name, compareFileName)
	if !ok {
		return nil, fmt.Errorf("source file not recorded: %s", path)
	}

	files[i] = fresh
	t.byDir[fresh.Dir] = files
	t.byPath[path] = fresh

	return fresh, nil
}

// Add reads a file that is not yet recorded, in a directory that is, and
// inserts it in name order.
func (t *SourceTree) Add(path string) (*SourceFile, error) {
	path = absDir(path)
	dir, name := filepath.Split(path)
	fresh := t.readFile(filepath.Clean(dir), name)

	t.mu.Lock()
	defer t.mu.Unlock()

	files, loaded := t.byDir[fresh.Dir]
	if !loaded {
		return nil, fmt.Errorf("directory not loaded: %s", fresh.Dir)
	}

	i, found := slices.BinarySearchFunc(files, name, compareFileName)
	if found {
		return nil, fmt.Errorf("source file already recorded: %s", path)
	}

	t.byDir[fresh.Dir] = slices.Insert(slices.Clone(files), i, fresh)
	t.byPath[path] = fresh

	return fresh, nil
}

// Remove forgets a file. The directory stays loaded.
func (t *SourceTree) Remove(path string) {
	path = absDir(path)

	t.mu.Lock()
	defer t.mu.Unlock()

	f := t.byPath[path]
	if f == nil {
		return
	}

	delete(t.byPath, path)
	files := slices.Clone(t.byDir[f.Dir])

	i, ok := slices.BinarySearchFunc(files, f.Name, compareFileName)
	if ok {
		files = slices.Delete(files, i, i+1)
	}

	t.byDir[f.Dir] = files
}

func compareFileName(f *SourceFile, name string) int { return strings.Compare(f.Name, name) }

func (t *SourceTree) readFile(dir, name string) *SourceFile {
	f := &SourceFile{Path: filepath.Join(dir, name), Dir: dir, Name: name}

	f.Content, f.Err = os.ReadFile(f.Path)
	if f.Err != nil {
		return f
	}

	f.AST, f.Err = parser.ParseFile(t.fset, f.Path, f.Content, parser.ParseComments)
	f.Generated = f.AST != nil && ast.IsGenerated(f.AST)

	return f
}

// Load walks root, skipping directories the go tool ignores (see InSkippedDir), and loads every
// directory it finds in parallel.
func (t *SourceTree) Load(root string) error {
	w := &treeWalker{root: root}

	err := filepath.WalkDir(root, w.visit)
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}

	l := &treeLoader{tree: t, dirs: make(chan string, len(w.dirs))}
	for _, dir := range w.dirs {
		l.dirs <- dir
	}

	close(l.dirs)

	var wg sync.WaitGroup
	for range workerCount(len(w.dirs)) {
		wg.Go(l.work)
	}

	wg.Wait()

	return l.err
}

type treeWalker struct {
	root string
	dirs []string
}

func (w *treeWalker) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}

	if !d.IsDir() {
		return nil
	}

	if InSkippedDir(w.root, path) {
		return filepath.SkipDir
	}

	w.dirs = append(w.dirs, path)

	return nil
}

// InSkippedDir reports whether dir, below root, is or sits inside a directory
// the go tool ignores: vendor, testdata, or a name starting with "." or "_".
// The root itself is never skipped.
func InSkippedDir(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return false
	}

	for name := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if name == ".." {
			return false
		}

		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor" || name == "testdata" {
			return true
		}
	}

	return false
}

type treeLoader struct {
	tree *SourceTree
	dirs chan string

	mu  sync.Mutex
	err error
}

func (l *treeLoader) work() {
	for dir := range l.dirs {
		err := l.tree.AddDir(dir)
		if err == nil {
			continue
		}

		l.mu.Lock()
		if l.err == nil {
			l.err = fmt.Errorf("load %s: %w", dir, err)
		}
		l.mu.Unlock()
	}
}

func absDir(path string) string {
	abs, err := filepath.Abs(path)
	if err == nil {
		return abs
	}

	return filepath.Clean(path)
}

func workerCount(jobs int) int {
	n := runtime.NumCPU()
	if n < jobs {
		return n
	}

	return jobs
}
