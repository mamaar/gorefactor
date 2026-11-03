package mcp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	mu      sync.RWMutex
	engine  *refactor.DefaultEngine
	set     *analysis.WorkspaceSet // nil until a workspace is loaded
	watcher *watch.Watcher
	updater *watch.WorkspaceUpdater
	cancel  context.CancelFunc // stops watcher goroutine
	logger  *slog.Logger
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
	_, err := s.engine.LoadWorkspace(path)
	if err != nil {
		return false, fmt.Errorf("load workspace: %w", err)
	}
	s.set = s.engine.WorkspaceSet()

	// Build reference index upfront (this may take a moment for large workspaces)
	s.logger.Info("building reference index", "packages", len(s.set.Primary().Workspace.Packages))
	indexBuilt := s.buildReferenceIndexLocked()
	if indexBuilt {
		s.logger.Info("reference index built successfully")
	} else {
		s.logger.Warn("failed to build reference index")
	}

	// The updater also re-parses files we write ourselves (SyncWorkspaceChanges),
	// which is what lets one operation observe the previous one's output. Build it
	// before the watcher and independently of it: if it were only constructed on
	// the watcher-available path, a failed watcher would silently turn every
	// post-apply sync into a no-op and later operations would analyse stale ASTs.
	s.updater = watch.NewUpdater(s.set, s.logger)

	// Start watcher.
	w, err := watch.NewWatcher(path, 200*time.Millisecond, s.logger)
	if err != nil {
		s.logger.Warn("watcher unavailable, workspace will not auto-update", "err", err)
		return indexBuilt, nil
	}
	s.watcher = w

	watchCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	ch := make(chan []watch.ChangeEvent, 4)
	go s.runWatcher(watchCtx, w, ch)
	go s.applyWatchedChanges(ch)

	return indexBuilt, nil
}

// runWatcher feeds w's batches into ch until ctx is cancelled, then closes ch
// so applyWatchedChanges exits.
func (s *MCPServer) runWatcher(ctx context.Context, w *watch.Watcher, ch chan<- []watch.ChangeEvent) {
	defer close(ch)
	err := w.Run(ctx, ch)
	if err != nil && ctx.Err() == nil {
		s.logger.Error("watcher error", "err", err)
	}
}

// applyWatchedChanges applies each batch received on ch to the workspace.
func (s *MCPServer) applyWatchedChanges(ch <-chan []watch.ChangeEvent) {
	for events := range ch {
		s.mu.Lock()
		s.updater.HandleChanges(events)
		s.mu.Unlock()
	}
}

// GetWorkspace returns the primary view's workspace or an error if none is loaded.
func (s *MCPServer) GetWorkspace() (*types.Workspace, error) {
	if s.set == nil {
		return nil, fmt.Errorf("no workspace loaded — call load_workspace first")
	}
	return s.set.Primary().Workspace, nil
}

// Covering returns the build configurations covering the loaded workspace.
func (s *MCPServer) Covering() analysis.Covering { return s.set.Covering() }

// GetEngine returns the refactoring engine.
func (s *MCPServer) GetEngine() *refactor.DefaultEngine {
	return s.engine
}

// InvalidateReferenceIndex marks the cached reference index of every built
// view as stale.
func (s *MCPServer) InvalidateReferenceIndex() {
	if s.set != nil {
		s.set.InvalidateReferenceIndexes()
	}
}

// buildReferenceIndexLocked builds the primary view's reference index (must be
// called with s.mu held). Returns true if successful, false otherwise.
func (s *MCPServer) buildReferenceIndexLocked() bool {
	s.logger.Debug("building reference index...")
	_, err := s.set.Primary().ReferenceIndex()
	if err != nil {
		s.logger.Warn("failed to build reference index")
		return false
	}
	return true
}

// SyncWorkspaceChanges forces an immediate workspace update for the given files.
// This is called after MCP operations write files to ensure workspace state is current.
func (s *MCPServer) SyncWorkspaceChanges(files []string) {
	if s.updater == nil || len(files) == 0 {
		return
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

	return
}

// RefreshStaleFiles re-syncs every .go file whose on-disk content differs from
// the source tree (edited, created or deleted outside the tool, or by an apply
// the file watcher has not delivered yet), including files the build config
// excludes. Tools plan from parsed byte offsets, so they must call this before
// planning; otherwise hunks land at stale offsets.
func (s *MCPServer) RefreshStaleFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil || s.updater == nil {
		return
	}

	var events []watch.ChangeEvent
	tree := s.set.Sources()
	for _, dir := range tree.Dirs() {
		events = append(events, staleEvents(tree, dir)...)
	}
	if len(events) == 0 {
		return
	}
	s.logger.Info("refreshing files changed outside the workspace", "files", len(events))
	s.updater.HandleChanges(events)
	s.InvalidateReferenceIndex()
}

// staleEvents compares the recorded files of dir with disk and returns a
// change event for each one that was removed, edited or added.
func staleEvents(tree *analysis.SourceTree, dir string) []watch.ChangeEvent {
	var events []watch.ChangeEvent
	known := make(map[string]bool)
	for _, f := range tree.Dir(dir) {
		known[f.Name] = true
		disk, err := os.ReadFile(f.Path)
		switch {
		case os.IsNotExist(err):
			events = append(events, watch.ChangeEvent{Path: f.Path, Op: fsnotify.Remove})
		case err == nil && !bytes.Equal(disk, f.Content):
			events = append(events, watch.ChangeEvent{Path: f.Path, Op: fsnotify.Write})
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return events
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || known[e.Name()] {
			continue
		}
		events = append(events, watch.ChangeEvent{Path: filepath.Join(dir, e.Name()), Op: fsnotify.Create})
	}
	return events
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
