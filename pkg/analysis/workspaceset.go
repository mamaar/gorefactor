package analysis

import (
	"fmt"
	"go/token"
	"log/slog"
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
	if s.primary, err = s.buildView(primary); err != nil {
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

	resolver := NewSymbolResolver(ws, s.logger)
	for _, pkg := range ws.Packages {
		if _, err := resolver.BuildSymbolTable(pkg); err != nil {
			s.logger.Error("symbol table build failed", "package", pkg.Path, "err", err)
			return nil, fmt.Errorf("failed to build symbol table for package %s: %w", pkg.Path, err)
		}
	}

	analyzer := NewDependencyAnalyzer(ws, s.logger)
	if _, err := analyzer.BuildDependencyGraph(); err != nil {
		s.logger.Error("dependency graph build failed", "err", err)
		return nil, fmt.Errorf("failed to build dependency graph: %w", err)
	}
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
	if v := s.views[key]; v != nil {
		return v, nil
	}
	v, err := s.buildView(cfg)
	if err != nil {
		return nil, err
	}
	s.views[key] = v
	return v, nil
}

// BuiltViews returns the views built so far, primary first.
func (s *WorkspaceSet) BuiltViews() []*View {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := []*View{s.primary}
	if len(s.views) == 1 {
		return views
	}
	for _, cfg := range s.coveringLocked().Configs[1:] {
		if v := s.views[cfg.Key()]; v != nil {
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
