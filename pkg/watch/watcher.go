package watch

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ChangeEvent represents a single filesystem change to a .go file.
type ChangeEvent struct {
	Path string
	Op   fsnotify.Op
}

// Watcher watches a workspace for .go file changes and emits debounced batches.
type Watcher struct {
	rootPath string
	debounce time.Duration
	logger   *slog.Logger
	fsw      *fsnotify.Watcher
}

// NewWatcher creates a Watcher that recursively watches rootPath for .go file
// changes. Hidden directories and vendor are skipped.
func NewWatcher(rootPath string, debounce time.Duration, logger *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		rootPath: rootPath,
		debounce: debounce,
		logger:   logger,
		fsw:      fsw,
	}

	if err := w.addDirs(); err != nil {
		_ = fsw.Close()
		return nil, err
	}

	return w, nil
}

// addDirs walks rootPath and adds every non-hidden, non-vendor directory.
func (w *Watcher) addDirs() error {
	return filepath.WalkDir(w.rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "vendor" {
			return filepath.SkipDir
		}
		return w.fsw.Add(path)
	})
}

// Run is the main event loop. It reads fsnotify events, filters for .go files,
// debounces rapid edits, and sends batched ChangeEvents to out.
// It blocks until ctx is cancelled or an unrecoverable fsnotify error occurs.
func (w *Watcher) Run(ctx context.Context, out chan<- []ChangeEvent) error {
	pending := make(map[string]fsnotify.Op)
	timer := time.NewTimer(w.debounce)
	timer.Stop() // don't fire until we have events

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if w.accept(ev) {
				pending[ev.Name] = ev.Op
				timer.Reset(w.debounce)
			}
			// When a new directory is created, start watching it.
			if ev.Op&fsnotify.Create != 0 {
				w.maybeAddDir(ev.Name)
			}

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("fsnotify error", "err", err)

		case <-timer.C:
			if len(pending) == 0 {
				continue
			}
			batch := make([]ChangeEvent, 0, len(pending))
			for p, op := range pending {
				batch = append(batch, ChangeEvent{Path: p, Op: op})
			}
			pending = make(map[string]fsnotify.Op)

			select {
			case out <- batch:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// Close shuts down the underlying fsnotify watcher.
func (w *Watcher) Close() error {
	return w.fsw.Close()
}

// accept returns true if the event is for a .go file and carries a relevant op.
func (w *Watcher) accept(ev fsnotify.Event) bool {
	if !strings.HasSuffix(ev.Name, ".go") {
		return false
	}
	return ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0
}

// maybeAddDir adds path to the watch set if it is a directory.
func (w *Watcher) maybeAddDir(path string) {
	// Best effort — ignore errors for non-dirs, symlinks, etc.
	if err := w.fsw.Add(path); err != nil {
		w.logger.Debug("could not add to watch", "path", path, "err", err)
	}
}
