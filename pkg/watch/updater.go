package watch

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// WorkspaceUpdater incrementally re-evaluates affected packages when files change.
type WorkspaceUpdater struct {
	workspace *types.Workspace
	parser    *analysis.GoParser
	resolver  *analysis.SymbolResolver
	analyzer  *analysis.DependencyAnalyzer
	logger    *slog.Logger
}

// NewUpdater creates a WorkspaceUpdater from the components exposed by WatchContext.
func NewUpdater(
	ws *types.Workspace,
	parser *analysis.GoParser,
	resolver *analysis.SymbolResolver,
	analyzer *analysis.DependencyAnalyzer,
	logger *slog.Logger,
) *WorkspaceUpdater {
	return &WorkspaceUpdater{
		workspace: ws,
		parser:    parser,
		resolver:  resolver,
		analyzer:  analyzer,
		logger:    logger,
	}
}

// HandleChanges processes a batch of file-change events.
// It groups them by package directory and incrementally re-evaluates each.
func (u *WorkspaceUpdater) HandleChanges(events []ChangeEvent) {
	start := time.Now()

	// Group events by directory (= package).
	byDir := make(map[string][]ChangeEvent)
	for _, ev := range events {
		dir := filepath.Dir(ev.Path)
		byDir[dir] = append(byDir[dir], ev)
	}

	for dir, dirEvents := range byDir {
		u.handlePackageChanges(dir, dirEvents)
	}

	u.logger.Info("batch complete",
		"dirs", len(byDir),
		"files", len(events),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

func (u *WorkspaceUpdater) handlePackageChanges(dir string, events []ChangeEvent) {
	for _, ev := range events {
		base := filepath.Base(ev.Path)
		isTest := strings.HasSuffix(base, "_test.go")

		switch {
		case ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0:
			u.handleDelete(dir, ev.Path, base, isTest)
		case ev.Op&fsnotify.Create != 0:
			u.handleCreate(dir, ev.Path, base, isTest)
		case ev.Op&fsnotify.Write != 0:
			u.handleModify(dir, ev.Path, base, isTest)
		}
	}
}

// handleModify re-parses a single file and updates its package's symbol table.
func (u *WorkspaceUpdater) handleModify(dir, path, base string, isTest bool) {
	start := time.Now()

	pkg := u.findPackageByDir(dir)
	if pkg == nil {
		u.logger.Warn("modify: package not found for dir, treating as create", "dir", dir, "file", base)
		u.handleCreate(dir, path, base, isTest)
		return
	}

	file, err := u.parser.ParseFile(path)
	if err != nil {
		u.logger.Error("modify: parse failed", "file", path, "err", err)
		return
	}
	file.Package = pkg

	if isTest {
		pkg.TestFiles[base] = file
	} else {
		pkg.Files[base] = file
		u.recollectImports(pkg)
	}

	u.rebuildPackage(pkg, "modify", path, start)
}

// handleCreate parses a new file and adds it to the appropriate package.
// If the package doesn't exist yet, a new package is created.
func (u *WorkspaceUpdater) handleCreate(dir, path, base string, isTest bool) {
	start := time.Now()

	// Ensure the file actually exists (create events can race with deletes).
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}

	file, err := u.parser.ParseFile(path)
	if err != nil {
		u.logger.Error("create: parse failed", "file", path, "err", err)
		return
	}

	pkg := u.findPackageByDir(dir)
	if pkg == nil {
		// New package.
		pkg = u.createPackage(dir, file, isTest)
		if pkg == nil {
			return
		}
		u.logger.Info("create: new package",
			"dir", dir,
			"importPath", pkg.ImportPath,
			"elapsed", time.Since(start).Round(time.Millisecond),
		)
		return
	}

	file.Package = pkg
	if isTest {
		pkg.TestFiles[base] = file
	} else {
		pkg.Files[base] = file
		u.recollectImports(pkg)
	}

	u.rebuildPackage(pkg, "create", path, start)
}

// handleDelete removes a file from its package. If the package becomes empty it
// is removed from the workspace.
func (u *WorkspaceUpdater) handleDelete(dir, path, base string, isTest bool) {
	start := time.Now()

	pkg := u.findPackageByDir(dir)
	if pkg == nil {
		return
	}

	if isTest {
		delete(pkg.TestFiles, base)
	} else {
		delete(pkg.Files, base)
	}

	u.resolver.InvalidateCacheForFile(path)

	if len(pkg.Files) == 0 {
		// Package is empty — remove it.
		delete(u.workspace.Packages, pkg.Path)
		if pkg.ImportPath != "" {
			delete(u.workspace.ImportToPath, pkg.ImportPath)
		}
		u.resolver.InvalidateCacheForPackage(pkg.Path)
		u.logger.Info("delete: removed empty package",
			"dir", dir,
			"elapsed", time.Since(start).Round(time.Millisecond),
		)
		return
	}

	u.recollectImports(pkg)
	u.rebuildPackage(pkg, "delete", path, start)
}

// rebuildPackage invalidates caches, rebuilds the symbol table and dependency graph,
// and logs the result.
func (u *WorkspaceUpdater) rebuildPackage(pkg *types.Package, action, path string, start time.Time) {
	u.resolver.InvalidateCacheForPackage(pkg.Path)

	st, err := u.resolver.BuildSymbolTable(pkg)
	if err != nil {
		u.logger.Error(action+": symbol table rebuild failed", "file", path, "err", err)
		return
	}

	if _, err := u.analyzer.BuildDependencyGraph(); err != nil {
		u.logger.Error(action+": dependency graph rebuild failed", "file", path, "err", err)
		return
	}

	symbols := countSymbols(st)
	u.logger.Info(action+": re-evaluated",
		"file", path,
		"package", pkg.ImportPath,
		"symbols", symbols,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// createPackage creates a brand-new Package for a directory that wasn't previously
// known to the workspace.
func (u *WorkspaceUpdater) createPackage(dir string, file *types.File, isTest bool) *types.Package {
	pkg := &types.Package{
		Dir:       dir,
		Path:      dir,
		Files:     make(map[string]*types.File),
		TestFiles: make(map[string]*types.File),
		Imports:   make([]string, 0),
	}

	base := filepath.Base(file.Path)
	file.Package = pkg

	if isTest {
		pkg.TestFiles[base] = file
		// Can't determine package name from test file alone; wait for a non-test file.
		return nil
	}

	pkg.Files[base] = file
	pkg.Name = file.AST.Name.Name

	u.recollectImports(pkg)

	// Compute import path.
	importPath := analysis.ComputeImportPath(u.workspace, dir)
	pkg.ImportPath = importPath

	// Register in workspace.
	u.workspace.Packages[pkg.Path] = pkg
	if importPath != "" {
		u.workspace.ImportToPath[importPath] = pkg.Path
	}

	// Build symbol table and dependency graph.
	u.resolver.InvalidateCacheForPackage(pkg.Path)
	if _, err := u.resolver.BuildSymbolTable(pkg); err != nil {
		u.logger.Error("create: symbol table build failed", "dir", dir, "err", err)
	}
	if _, err := u.analyzer.BuildDependencyGraph(); err != nil {
		u.logger.Error("create: dependency graph build failed", "dir", dir, "err", err)
	}

	return pkg
}

// findPackageByDir returns the workspace package whose Dir matches dir.
func (u *WorkspaceUpdater) findPackageByDir(dir string) *types.Package {
	for _, pkg := range u.workspace.Packages {
		if pkg.Dir == dir || pkg.Path == dir {
			return pkg
		}
	}
	return nil
}

// recollectImports rebuilds the import list for pkg from its file ASTs.
func (u *WorkspaceUpdater) recollectImports(pkg *types.Package) {
	seen := make(map[string]bool)
	var imports []string
	for _, f := range pkg.Files {
		for _, imp := range f.AST.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if !seen[p] {
				seen[p] = true
				imports = append(imports, p)
			}
		}
	}
	pkg.Imports = imports
}

// countSymbols returns the total number of symbols in a symbol table.
func countSymbols(st *types.SymbolTable) int {
	if st == nil {
		return 0
	}
	n := len(st.Functions) + len(st.Types) + len(st.Variables) + len(st.Constants)
	for _, ms := range st.Methods {
		n += len(ms)
	}
	return n
}

// PackageCount returns the number of packages currently in the workspace.
// Useful for test assertions.
func (u *WorkspaceUpdater) PackageCount() int {
	return len(u.workspace.Packages)
}

// FindPackage returns the package at dir, or nil.
func (u *WorkspaceUpdater) FindPackage(dir string) *types.Package {
	return u.findPackageByDir(dir)
}

// Workspace returns the workspace for testing.
func (u *WorkspaceUpdater) Workspace() *types.Workspace {
	return u.workspace
}

// String implements fmt.Stringer for logging convenience.
func (u *WorkspaceUpdater) String() string {
	return fmt.Sprintf("WorkspaceUpdater{packages=%d}", len(u.workspace.Packages))
}
