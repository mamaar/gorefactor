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
}

// SourceTree records every .go file under a root, parsed into one FileSet.
type SourceTree struct {
	fset *token.FileSet

	mu     sync.RWMutex
	byDir  map[string][]*SourceFile // sorted by name
	byPath map[string]*SourceFile
}

func NewSourceTree(fset *token.FileSet) *SourceTree {
	return &SourceTree{
		fset:   fset,
		byDir:  make(map[string][]*SourceFile),
		byPath: make(map[string]*SourceFile),
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
		return err
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
	if _, ok := t.byDir[dir]; ok {
		return nil
	}
	t.byDir[dir] = files
	for _, f := range files {
		t.byPath[f.Path] = f
	}
	return nil
}

func (t *SourceTree) readFile(dir, name string) *SourceFile {
	f := &SourceFile{Path: filepath.Join(dir, name), Dir: dir, Name: name}
	f.Content, f.Err = os.ReadFile(f.Path)
	if f.Err != nil {
		return f
	}
	f.AST, f.Err = parser.ParseFile(t.fset, f.Path, f.Content, parser.ParseComments)
	return f
}

// Load walks root, skipping dot-directories and vendor, and loads every
// directory it finds in parallel.
func (t *SourceTree) Load(root string) error {
	w := &treeWalker{}
	if err := filepath.WalkDir(root, w.visit); err != nil {
		return err
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

type treeWalker struct{ dirs []string }

func (w *treeWalker) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if !d.IsDir() {
		return nil
	}
	if name := d.Name(); strings.HasPrefix(name, ".") || name == "vendor" {
		return filepath.SkipDir
	}
	w.dirs = append(w.dirs, path)
	return nil
}

type treeLoader struct {
	tree *SourceTree
	dirs chan string

	mu  sync.Mutex
	err error
}

func (l *treeLoader) work() {
	for dir := range l.dirs {
		if err := l.tree.AddDir(dir); err != nil {
			l.mu.Lock()
			if l.err == nil {
				l.err = fmt.Errorf("load %s: %w", dir, err)
			}
			l.mu.Unlock()
		}
	}
}

func absDir(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

func workerCount(jobs int) int {
	if n := runtime.NumCPU(); n < jobs {
		return n
	}
	return jobs
}
