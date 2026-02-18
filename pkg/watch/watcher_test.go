package watch

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestWatcher_CreateFileTriggersEvent(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "init.go", "package p\n")

	w, err := NewWatcher(dir, 50*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan []ChangeEvent, 10)
	go func() { _ = w.Run(ctx, out) }()

	// Create a new .go file
	writeGoFile(t, dir, "new.go", "package p\nfunc New() {}\n")

	batch := waitForBatch(t, out, 2*time.Second)
	assertContainsPath(t, batch, filepath.Join(dir, "new.go"))
}

func TestWatcher_ModifyFileTriggersEvent(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "main.go", "package p\n")

	w, err := NewWatcher(dir, 50*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan []ChangeEvent, 10)
	go func() { _ = w.Run(ctx, out) }()

	// Modify the file
	writeGoFile(t, dir, "main.go", "package p\nfunc Hello() {}\n")

	batch := waitForBatch(t, out, 2*time.Second)
	assertContainsPath(t, batch, filepath.Join(dir, "main.go"))
}

func TestWatcher_DeleteFileTriggersEvent(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "del.go", "package p\n")

	w, err := NewWatcher(dir, 50*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan []ChangeEvent, 10)
	go func() { _ = w.Run(ctx, out) }()

	_ = os.Remove(filepath.Join(dir, "del.go"))

	batch := waitForBatch(t, out, 2*time.Second)
	assertContainsPath(t, batch, filepath.Join(dir, "del.go"))
}

func TestWatcher_NonGoFileIgnored(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "init.go", "package p\n")

	w, err := NewWatcher(dir, 50*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	out := make(chan []ChangeEvent, 10)
	go func() { _ = w.Run(ctx, out) }()

	// Write a non-.go file
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hello"), 0644)

	// Should timeout with no events
	select {
	case batch := <-out:
		t.Fatalf("expected no events for .md file, got %d", len(batch))
	case <-ctx.Done():
		// Good: no events received
	}
}

func TestWatcher_DebounceCoalescesEvents(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "init.go", "package p\n")

	w, err := NewWatcher(dir, 200*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan []ChangeEvent, 10)
	go func() { _ = w.Run(ctx, out) }()

	// Rapid edits to same file — should coalesce into one event
	for i := 0; i < 5; i++ {
		writeGoFile(t, dir, "rapid.go", "package p\n// v"+string(rune('0'+i))+"\n")
		time.Sleep(20 * time.Millisecond)
	}

	batch := waitForBatch(t, out, 2*time.Second)

	// Only one entry for rapid.go
	count := 0
	for _, ev := range batch {
		if filepath.Base(ev.Path) == "rapid.go" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 coalesced event for rapid.go, got %d", count)
	}
}

func TestWatcher_ContextCancellationStops(t *testing.T) {
	dir := t.TempDir()
	writeGoFile(t, dir, "init.go", "package p\n")

	w, err := NewWatcher(dir, 50*time.Millisecond, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan []ChangeEvent, 10)

	done := make(chan error, 1)
	go func() {
		done <- w.Run(ctx, out)
	}()

	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

// --- helpers ---

func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func waitForBatch(t *testing.T, ch <-chan []ChangeEvent, timeout time.Duration) []ChangeEvent {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(timeout):
		t.Fatal("timed out waiting for batch")
		return nil
	}
}

func assertContainsPath(t *testing.T, batch []ChangeEvent, path string) {
	t.Helper()
	for _, ev := range batch {
		if ev.Path == path {
			return
		}
	}
	t.Fatalf("batch does not contain %s; got %v", path, batch)
}
