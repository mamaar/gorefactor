package watch

import (
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// WorkspaceUpdater incrementally re-evaluates affected packages when files change.
type WorkspaceUpdater struct {
	set    *analysis.WorkspaceSet
	logger *slog.Logger
}

// NewUpdater creates a WorkspaceUpdater that keeps every built view of set current.
func NewUpdater(set *analysis.WorkspaceSet, logger *slog.Logger) *WorkspaceUpdater {
	return &WorkspaceUpdater{set: set, logger: logger}
}

// syncedChange is a file event whose content is already in the source tree.
type syncedChange struct {
	dir     string
	path    string
	action  string
	present bool
}

// HandleChanges processes a batch of file-change events: each file is synced
// into the shared source tree once, then re-projected into every built view.
func (u *WorkspaceUpdater) HandleChanges(events []ChangeEvent) {
	start := time.Now()

	changes := make([]syncedChange, 0, len(events))
	for _, ev := range events {
		action := actionName(ev.Op)
		src, err := u.set.SyncSource(ev.Path)
		absent := errors.Is(err, analysis.ErrSkippedSource) || errors.Is(err, analysis.ErrSourceRemoved)
		if err != nil && !absent {
			u.logger.Error(action+": source sync failed", "file", ev.Path, "err", err)
			continue
		}
		changes = append(changes, syncedChange{dir: filepath.Dir(ev.Path), path: ev.Path, action: action, present: src != nil})
	}

	views := u.set.BuiltViews()
	for _, v := range views {
		dirs := changedDirs(changes)
		// Importers are found before and after the change: a removed package
		// is only in the first graph, a created one only in the second.
		v.Parser.InvalidateImporters(v.Workspace, dirs)
		for _, c := range changes {
			u.handleChange(v, c)
		}
		v.Parser.InvalidateImporters(v.Workspace, dirs)
		u.invalidateImporterCaches(v, dirs)
		v.InvalidateReferenceIndex()
	}

	u.logger.Info("batch complete",
		"files", len(events),
		"views", len(views),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// changedDirs returns the distinct directories of the changed files.
func changedDirs(changes []syncedChange) []string {
	dirs := make([]string, 0, len(changes))
	for _, c := range changes {
		if !slices.Contains(dirs, c.dir) {
			dirs = append(dirs, c.dir)
		}
	}
	return dirs
}

// invalidateImporterCaches drops the resolver caches of the packages that
// import a changed directory, whose resolved references name replaced symbols.
func (u *WorkspaceUpdater) invalidateImporterCaches(v *analysis.View, dirs []string) {
	for _, path := range analysis.Importers(v.Workspace, dirs) {
		v.Resolver.InvalidateCacheForPackage(path)
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

// handleChange re-evaluates whether the changed file belongs to its package
// under v's build config. A package left without files is removed from v's
// workspace.
func (u *WorkspaceUpdater) handleChange(v *analysis.View, c syncedChange) {
	start := time.Now()

	pkg := findPackageByDir(v.Workspace, c.dir)
	if pkg == nil {
		if c.present {
			u.createPackage(v, c.dir, start)
		}
		return
	}

	if !v.Parser.ProjectFile(pkg, c.path) {
		return
	}
	v.Resolver.InvalidateCacheForFile(c.path)

	if len(pkg.Files) == 0 && len(pkg.TestFiles) == 0 {
		u.removePackage(v, pkg, start)
		return
	}
	u.rebuildPackage(v, pkg, c, start)
}

func (u *WorkspaceUpdater) removePackage(v *analysis.View, pkg *types.Package, start time.Time) {
	delete(v.Workspace.Packages, pkg.Path)
	if pkg.ImportPath != "" {
		delete(v.Workspace.ImportToPath, pkg.ImportPath)
	}
	v.Parser.InvalidateTypeCheck(pkg)
	v.Resolver.InvalidateCacheForPackage(pkg.Path)
	_ = v.Analyzer.BuildDependencyGraph()
	u.logger.Info("removed empty package",
		"dir", pkg.Dir,
		"config", v.Config.Key(),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// rebuildPackage invalidates caches, rebuilds the symbol table and dependency graph,
// and logs the result.
func (u *WorkspaceUpdater) rebuildPackage(v *analysis.View, pkg *types.Package, c syncedChange, start time.Time) {
	// Re-parsing replaces AST node identities. go/types.Info is keyed by those
	// identities, so it must never survive an incremental package rebuild.
	v.Parser.InvalidateTypeCheck(pkg)
	v.Resolver.InvalidateCacheForPackage(pkg.Path)

	st, err := v.Resolver.BuildSymbolTable(pkg)
	if err != nil {
		u.logger.Error(c.action+": symbol table rebuild failed", "file", c.path, "err", err)
		return
	}

	_ = v.Analyzer.BuildDependencyGraph()

	symbols := countSymbols(st)
	u.logger.Info(c.action+": re-evaluated",
		"file", c.path,
		"package", pkg.ImportPath,
		"config", v.Config.Key(),
		"symbols", symbols,
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// createPackage builds the package in dir from the source tree and registers
// it in v. A directory with no usable files yields no package.
func (u *WorkspaceUpdater) createPackage(v *analysis.View, dir string, start time.Time) {
	pkg, err := v.Parser.ParsePackage(dir)
	if errors.Is(err, analysis.ErrNoPackage) {
		return
	}
	if err != nil {
		u.logger.Error("create: package parse failed", "dir", dir, "err", err)
		return
	}

	pkg.ImportPath = analysis.ComputeImportPath(v.Workspace, dir)
	v.Workspace.Packages[pkg.Path] = pkg
	if pkg.ImportPath != "" {
		v.Workspace.ImportToPath[pkg.ImportPath] = pkg.Path
	}

	v.Parser.InvalidateTypeCheck(pkg)
	v.Resolver.InvalidateCacheForPackage(pkg.Path)
	_, err = v.Resolver.BuildSymbolTable(pkg)
	if err != nil {
		u.logger.Error("create: symbol table build failed", "dir", dir, "err", err)
	}
	_ = v.Analyzer.BuildDependencyGraph()
	u.logger.Info("create: new package",
		"dir", dir,
		"importPath", pkg.ImportPath,
		"config", v.Config.Key(),
		"elapsed", time.Since(start).Round(time.Millisecond),
	)
}

// findPackageByDir returns the package of ws whose Dir matches dir.
func findPackageByDir(ws *types.Workspace, dir string) *types.Package {
	for _, pkg := range ws.Packages {
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
