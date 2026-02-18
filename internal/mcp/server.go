package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/pkg/watch"
)

// MCPServer holds the shared state for the MCP tool handlers:
// a loaded workspace, its refactoring engine, and an optional
// filesystem watcher that incrementally updates the workspace.
type MCPServer struct {
	mu        sync.RWMutex
	engine    *refactor.DefaultEngine
	workspace *types.Workspace
	resolver  interface{} // *analysis.SymbolResolver (from WatchContext)
	watcher   *watch.Watcher
	updater   *watch.WorkspaceUpdater
	cancel    context.CancelFunc // stops watcher goroutine
	logger    *slog.Logger

	// Cached reference index for performance (invalidated on workspace changes)
	refIndexMu    sync.RWMutex
	refIndex      interface{} // *analysis.ReferenceIndex
	refIndexValid bool
}

// NewMCPServer creates a new MCPServer with the given logger.
func NewMCPServer(logger *slog.Logger) *MCPServer {
	eng := refactor.CreateEngineWithConfig(&refactor.EngineConfig{
		SkipCompilation: true,
		AllowBreaking:   true,
	}, logger)
	return &MCPServer{
		engine: eng.(*refactor.DefaultEngine),
		logger: logger,
	}
}

// LoadWorkspace loads (or reloads) a workspace at the given path.
// It builds the reference index upfront and starts a background watcher for incremental updates.
// Returns (indexBuilt, error) where indexBuilt indicates if the reference index was successfully built.
func (s *MCPServer) LoadWorkspace(ctx context.Context, path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop any existing watcher.
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.watcher != nil {
		_ = s.watcher.Close()
		s.watcher = nil
	}

	s.logger.Info("loading workspace", "path", path)
	wctx, err := s.engine.LoadWorkspaceForWatch(path)
	if err != nil {
		return false, fmt.Errorf("load workspace: %w", err)
	}
	s.workspace = wctx.Workspace
	s.resolver = wctx.Resolver

	// Invalidate cached reference index since workspace changed
	s.InvalidateReferenceIndex()

	// Build reference index upfront (this may take a moment for large workspaces)
	s.logger.Info("building reference index", "packages", len(s.workspace.Packages))
	indexBuilt := s.buildReferenceIndexLocked()
	if indexBuilt {
		s.logger.Info("reference index built successfully")
	} else {
		s.logger.Warn("failed to build reference index")
	}

	// Start watcher.
	w, err := watch.NewWatcher(path, 200*time.Millisecond, s.logger)
	if err != nil {
		s.logger.Warn("watcher unavailable, workspace will not auto-update", "err", err)
		return indexBuilt, nil
	}
	s.watcher = w
	s.updater = watch.NewUpdater(wctx.Workspace, wctx.Parser, wctx.Resolver, wctx.Analyzer, s.logger)

	watchCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	ch := make(chan []watch.ChangeEvent, 4)
	go func() {
		if err := w.Run(watchCtx, ch); err != nil && watchCtx.Err() == nil {
			s.logger.Error("watcher error", "err", err)
		}
	}()
	go func() {
		for events := range ch {
			s.mu.Lock()
			s.updater.HandleChanges(events)
			s.mu.Unlock()
		}
	}()

	return indexBuilt, nil
}

// GetWorkspace returns the loaded workspace or an error if none is loaded.
func (s *MCPServer) GetWorkspace() (*types.Workspace, error) {
	if s.workspace == nil {
		return nil, fmt.Errorf("no workspace loaded — call load_workspace first")
	}
	return s.workspace, nil
}

// GetEngine returns the refactoring engine.
func (s *MCPServer) GetEngine() *refactor.DefaultEngine {
	return s.engine
}

// EnsureReferenceIndex returns a cached reference index, building one if necessary.
// Uses double-checked locking for thread safety.
func (s *MCPServer) EnsureReferenceIndex(ws *types.Workspace) (*analysis.ReferenceIndex, error) {
	// Fast path: check if valid cached index exists
	s.refIndexMu.RLock()
	if s.refIndexValid && s.refIndex != nil {
		idx := s.refIndex.(*analysis.ReferenceIndex)
		s.refIndexMu.RUnlock()
		return idx, nil
	}
	s.refIndexMu.RUnlock()

	// Slow path: build and cache
	s.refIndexMu.Lock()
	defer s.refIndexMu.Unlock()

	// Double-check after acquiring write lock
	if s.refIndexValid && s.refIndex != nil {
		return s.refIndex.(*analysis.ReferenceIndex), nil
	}

	// Build the index using the resolver if available, otherwise create a new one
	var resolver *analysis.SymbolResolver
	if s.resolver != nil {
		var ok bool
		resolver, ok = s.resolver.(*analysis.SymbolResolver)
		if !ok {
			resolver = analysis.NewSymbolResolver(ws, s.logger)
		}
	} else {
		resolver = analysis.NewSymbolResolver(ws, s.logger)
	}

	s.logger.Info("building reference index for workspace")
	idx := resolver.BuildReferenceIndex()
	if idx == nil {
		return nil, fmt.Errorf("failed to build reference index")
	}
	s.refIndex = idx
	s.refIndexValid = true
	return idx, nil
}

// InvalidateReferenceIndex marks the cached reference index as stale.
func (s *MCPServer) InvalidateReferenceIndex() {
	s.refIndexMu.Lock()
	defer s.refIndexMu.Unlock()
	s.refIndexValid = false
	s.refIndex = nil
}

// buildReferenceIndexLocked builds the reference index (must be called with s.mu held).
// Returns true if successful, false otherwise.
func (s *MCPServer) buildReferenceIndexLocked() bool {
	if s.workspace == nil || s.resolver == nil {
		s.logger.Warn("workspace or resolver not available")
		return false
	}

	// Cast resolver and build index
	resolver, ok := s.resolver.(*analysis.SymbolResolver)
	if !ok {
		s.logger.Error("resolver type assertion failed")
		return false
	}

	s.logger.Debug("building reference index...")
	idx := resolver.BuildReferenceIndex()
	if idx != nil {
		s.refIndexMu.Lock()
		s.refIndex = idx
		s.refIndexValid = true
		s.refIndexMu.Unlock()
		return true
	}
	s.logger.Warn("failed to build reference index")
	return false
}

// SyncWorkspaceChanges forces an immediate workspace update for the given files.
// This is called after MCP operations write files to ensure workspace state is current.
func (s *MCPServer) SyncWorkspaceChanges(files []string) error {
	if s.updater == nil || len(files) == 0 {
		return nil
	}

	// Create synthetic change events for modified files
	events := make([]watch.ChangeEvent, len(files))
	for i, file := range files {
		events[i] = watch.ChangeEvent{
			Path: file,
			Op:   fsnotify.Write,
		}
	}

	// Acquire write lock and update synchronously
	s.mu.Lock()
	defer s.mu.Unlock()

	s.updater.HandleChanges(events)

	// Invalidate cached reference index since files changed
	s.InvalidateReferenceIndex()

	return nil
}

// RLock acquires a read lock on the server state.
func (s *MCPServer) RLock() { s.mu.RLock() }

// RUnlock releases the read lock.
func (s *MCPServer) RUnlock() { s.mu.RUnlock() }

// Close stops the watcher and releases resources.
func (s *MCPServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.watcher != nil {
		_ = s.watcher.Close()
		s.watcher = nil
	}
}
