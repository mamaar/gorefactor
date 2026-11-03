package analysis

import (
	"errors"
	"fmt"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mamaar/gorefactor/pkg/types"
)

// View is one build configuration's projection of a workspace set's sources.
type View struct {
	Config    BuildConfig
	Workspace *types.Workspace
	Parser    *GoParser
	Resolver  *SymbolResolver
	Analyzer  *DependencyAnalyzer

	refMu    sync.RWMutex
	refIndex *ReferenceIndex
}

// ReferenceIndex returns the view's reference index, building and caching it
// on first use.
func (v *View) ReferenceIndex() (*ReferenceIndex, error) {
	v.refMu.RLock()
	idx := v.refIndex
	v.refMu.RUnlock()
	if idx != nil {
		return idx, nil
	}

	v.refMu.Lock()
	defer v.refMu.Unlock()
	if v.refIndex == nil {
		idx := v.Resolver.BuildLazyReferenceIndex()
		if idx == nil {
			return nil, fmt.Errorf("failed to build reference index")
		}
		v.refIndex = idx
	}
	return v.refIndex, nil
}

// InvalidateReferenceIndex drops the cached reference index.
func (v *View) InvalidateReferenceIndex() {
	v.refMu.Lock()
	defer v.refMu.Unlock()
	v.refIndex = nil
}

// WarmExternals loads the view's third-party packages and type-checks those
// its files import, without type-checking any workspace package.
func (v *View) WarmExternals() { v.Parser.importer.warm() }

// DropTypeInfo frees the type information of every package and the cached
// reference index, and keeps what the checks recorded about test files, so
// UncheckedFiles answers as before. The packages are not type-checked again
// until ReleaseTypeInfo.
func (v *View) DropTypeInfo() {
	v.Parser.dropTypeInfo(v.Workspace)
	v.InvalidateReferenceIndex()
}

// ReleaseTypeInfo drops the type-check results of every package and the cached
// reference index. The packages are type-checked again on next use; the
// third-party packages stay loaded.
func (v *View) ReleaseTypeInfo() {
	v.Parser.releaseTypeChecks(v.Workspace)
	v.InvalidateReferenceIndex()
}

// Approximate reports whether the view type-checks against the primary view's
// third-party packages because loading them under its own configuration
// failed, and that failure. It is known once a type-check has needed them.
func (v *View) Approximate() (bool, error) { return v.Parser.externalFallback() }

// ApproximateView is a built view whose third-party packages came from the
// primary view, and why.
type ApproximateView struct {
	Config BuildConfig
	Err    error
}

// WorkspaceSet holds one source tree and a view of it per build configuration.
// The primary view is built at load, the others on first request.
type WorkspaceSet struct {
	root    string
	module  *types.Module
	logger  *slog.Logger
	tree    *SourceTree
	primary *View

	// mu guards views and covering. It is taken after the caller's workspace
	// lock (MCPServer.mu) and before any View, GoParser or SourceTree lock;
	// type-checking never takes it.
	mu       sync.Mutex
	views    map[string]*View // by BuildConfig.Key
	covering *Covering
}

// LoadWorkspaceSet reads and parses every .go file under root once and builds
// the primary view.
func LoadWorkspaceSet(root string, primary BuildConfig, logger *slog.Logger) (*WorkspaceSet, error) {
	absRoot, err := absWorkspaceRoot(root)
	if err != nil {
		logger.Error("failed to get absolute path", "path", root, "err", err)
		return nil, err
	}
	logger.Info("parsing workspace", "path", absRoot)
	tree := NewSourceTree(token.NewFileSet())
	if err := loadSources(tree, absRoot); err != nil {
		logger.Error("workspace discovery failed", "path", root, "err", err)
		return nil, err
	}

	s := &WorkspaceSet{
		root:   absRoot,
		module: readModule(absRoot),
		logger: logger,
		tree:   tree,
		views:  make(map[string]*View),
	}
	s.primary, err = s.buildView(primary)
	if err != nil {
		return nil, err
	}
	s.views[primary.Key()] = s.primary
	return s, nil
}

// buildView projects the source tree under cfg and indexes the result. It
// reads and parses nothing.
func (s *WorkspaceSet) buildView(cfg BuildConfig) (*View, error) {
	parser := newViewParser(s.logger, s.tree, cfg)
	ws := parser.projectWorkspace(s.root, s.module)
	if s.primary != nil {
		parser.importer.fallback = s.primary.Parser.importer
	}

	resolver := NewSymbolResolver(ws, s.logger)
	resolver.inspectors = s.tree.inspectors
	for _, pkg := range ws.Packages {
		_, err := resolver.BuildSymbolTable(pkg)
		if err != nil {
			s.logger.Error("symbol table build failed", "package", pkg.Path, "err", err)
			return nil, fmt.Errorf("failed to build symbol table for package %s: %w", pkg.Path, err)
		}
	}

	analyzer := NewDependencyAnalyzer(ws, s.logger)
	_ = analyzer.BuildDependencyGraph()
	return &View{Config: cfg, Workspace: ws, Parser: parser, Resolver: resolver, Analyzer: analyzer}, nil
}

// Primary returns the view of the configuration the set was loaded under.
func (s *WorkspaceSet) Primary() *View { return s.primary }

// Sources returns the config-independent record of every loaded file.
func (s *WorkspaceSet) Sources() *SourceTree { return s.tree }

// Covering returns the build configurations that together include every
// recorded file, computing them on first use after the source tree changed.
func (s *WorkspaceSet) Covering() Covering {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coveringLocked()
}

func (s *WorkspaceSet) coveringLocked() Covering {
	if s.covering == nil {
		c := ComputeCovering(s.tree, s.primary.Config)
		s.covering = &c
	}
	return *s.covering
}

// Views returns one view per configuration of the covering, primary first,
// building those not built yet.
func (s *WorkspaceSet) Views() ([]*View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	configs := s.coveringLocked().Configs
	s.pruneLocked(configs)
	views := make([]*View, 0, len(configs))
	for _, cfg := range configs {
		v, err := s.viewLocked(cfg)
		if err != nil {
			return nil, fmt.Errorf("build view %s: %w", cfg.Key(), err)
		}
		views = append(views, v)
	}
	return views, nil
}

func (s *WorkspaceSet) viewLocked(cfg BuildConfig) (*View, error) {
	key := cfg.Key()
	v := s.views[key]
	if v != nil {
		return v, nil
	}
	v, err := s.buildView(cfg)
	if err != nil {
		return nil, fmt.Errorf("view locked: %w", err)
	}
	s.views[key] = v
	return v, nil
}

// pruneLocked drops the built views whose configuration is not in configs.
func (s *WorkspaceSet) pruneLocked(configs []BuildConfig) {
	keep := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		keep[cfg.Key()] = true
	}
	for key := range s.views {
		if !keep[key] {
			delete(s.views, key)
		}
	}
}

// ApproximateViews lists the built views that are approximate. A view is known
// to be approximate only once a type-check has needed its third-party packages.
func (s *WorkspaceSet) ApproximateViews() []ApproximateView {
	var out []ApproximateView
	for _, v := range s.BuiltViews() {
		approx, err := v.Approximate()
		if approx {
			out = append(out, ApproximateView{Config: v.Config, Err: err})
		}
	}
	return out
}

// BuiltViews returns the views built so far that the covering still holds,
// primary first, and drops the rest.
func (s *WorkspaceSet) BuiltViews() []*View {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := []*View{s.primary}
	if len(s.views) == 1 {
		return views
	}
	configs := s.coveringLocked().Configs
	s.pruneLocked(configs)
	for _, cfg := range configs[1:] {
		v := s.views[cfg.Key()]
		if v != nil {
			views = append(views, v)
		}
	}
	return views
}

// ViewOf returns the built view that owns ws, or nil.
func (s *WorkspaceSet) ViewOf(ws *types.Workspace) *View {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.views {
		if v.Workspace == ws {
			return v
		}
	}
	return nil
}

// ErrSkippedSource reports that SyncSource does not track the path: it is not a
// .go file or lies in a skipped directory.
var ErrSkippedSource = errors.New("source file is not tracked")

// ErrSourceRemoved reports that SyncSource found the path missing on disk and
// forgot it.
var ErrSourceRemoved = errors.New("source file was removed")

// SyncSource brings the shared source tree in line with path on disk: a missing
// file is forgotten (ErrSourceRemoved), a recorded one is re-read, a new one is
// recorded. Untracked paths yield ErrSkippedSource. Views are not touched;
// ProjectFile applies the result to each.
func (s *WorkspaceSet) SyncSource(path string) (*SourceFile, error) {
	if !strings.HasSuffix(path, ".go") {
		return nil, ErrSkippedSource
	}
	defer s.invalidateCovering()
	path = absDir(path)
	if InSkippedDir(s.root, filepath.Dir(path)) {
		return nil, ErrSkippedSource
	}
	_, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		s.tree.Remove(path)
		return nil, ErrSourceRemoved
	}

	dir := filepath.Dir(path)
	if !s.tree.loaded(dir) {
		err := s.tree.AddDir(dir)
		if err != nil {
			return nil, err
		}
		return s.tree.File(path), nil
	}
	if s.tree.File(path) != nil {
		return s.tree.Replace(path)
	}
	return s.tree.Add(path)
}

func (s *WorkspaceSet) invalidateCovering() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.covering = nil
}

// InvalidateReferenceIndexes drops the cached reference index of every built view.
func (s *WorkspaceSet) InvalidateReferenceIndexes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.views {
		v.InvalidateReferenceIndex()
	}
}
