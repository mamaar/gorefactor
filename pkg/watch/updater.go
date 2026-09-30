package watch

import (
	"fmt"
	"log/slog"
	"path/filepath"
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
		u.handleChange(dir, ev.Path, actionName(ev.Op))
	}
}

func actionName(op fsnotify.Op) string {
	switch {
	case op&(fsnotify.Remove|fsnotify.Rename) != 0:
		return "delete"
	case op&fsnotify.Create != 0:
		return "create"
	default:
		return "modify"
	}
}

// handleChange re-reads path into the source tree and re-evaluates whether it
// belongs to its package under the build config. A package left without
// non-test files is removed from the workspace.
func (u *WorkspaceUpdater) handleChange(dir, path, action string) {
	start := time.Now()

	src, err := u.parser.SyncSource(path)
	if err != nil {
		u.logger.Error(action+": source sync failed", "file", path, "err", err)
		return
	}

	pkg := u.findPackageByDir(dir)
	if pkg == nil {
		if src != nil {
			u.createPackage(dir, start)
		}
		return
	}

	if !u.parser.ProjectFile(pkg, path) {
		return
	}
	u.resolver.InvalidateCacheForFile(path)

	if len(pkg.Files) == 0 {
		u.removePackage(pkg, start)
		return
	}
	u.rebuildPackage(pkg, action, path, start)
}

func (u *WorkspaceUpdater) removePackage(pkg *types.Package, start time.Time) {
	delete(u.workspace.Packages, pkg.Path)
	if pkg.ImportPath != "" {
		delete(u.workspace.ImportToPath, pkg.ImportPath)
	}
	u.resolver.InvalidateCacheForPackage(pkg.Path)
	u.logger.Info("removed empty package",
		"dir", pkg.Dir,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// rebuildPackage invalidates caches, rebuilds the symbol table and dependency graph,
// and logs the result.
func (u *WorkspaceUpdater) rebuildPackage(pkg *types.Package, action, path string, start time.Time) {
	// Re-parsing replaces AST node identities. go/types.Info is keyed by those
	// identities, so it must never survive an incremental package rebuild.
	u.parser.InvalidateTypeCheck(pkg)
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

// createPackage builds the package in dir from the source tree and registers
// it. A directory with no usable non-test files yields no package.
func (u *WorkspaceUpdater) createPackage(dir string, start time.Time) {
	pkg, err := u.parser.ParsePackage(dir)
	if err != nil {
		u.logger.Error("create: package parse failed", "dir", dir, "err", err)
		return
	}
	if pkg == nil {
		return
	}

	pkg.ImportPath = analysis.ComputeImportPath(u.workspace, dir)
	u.workspace.Packages[pkg.Path] = pkg
	if pkg.ImportPath != "" {
		u.workspace.ImportToPath[pkg.ImportPath] = pkg.Path
	}

	u.resolver.InvalidateCacheForPackage(pkg.Path)
	if _, err := u.resolver.BuildSymbolTable(pkg); err != nil {
		u.logger.Error("create: symbol table build failed", "dir", dir, "err", err)
	}
	if _, err := u.analyzer.BuildDependencyGraph(); err != nil {
		u.logger.Error("create: dependency graph build failed", "dir", dir, "err", err)
	}
	u.logger.Info("create: new package",
		"dir", dir,
		"importPath", pkg.ImportPath,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
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

// Sources returns the source tree the updater keeps in sync with disk.
func (u *WorkspaceUpdater) Sources() *analysis.SourceTree { return u.parser.Sources() }

// Workspace returns the workspace for testing.
func (u *WorkspaceUpdater) Workspace() *types.Workspace {
	return u.workspace
}

// String implements fmt.Stringer for logging convenience.
func (u *WorkspaceUpdater) String() string {
	return fmt.Sprintf("WorkspaceUpdater{packages=%d}", len(u.workspace.Packages))
}
