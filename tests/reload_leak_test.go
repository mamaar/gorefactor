package tests_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/mamaar/gorefactor/internal/mcp"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

const reloadCount = 30

// openFDs returns the number of descriptors this process holds, or skips the
// test where the platform exposes no listing.
func openFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/dev/fd", "/proc/self/fd"} {
		if n, ok := countEntries(dir); ok {
			return n
		}
	}
	t.Skip("no /dev/fd or /proc/self/fd on this platform")
	return 0
}

// countEntries counts directory entries by name; os.ReadDir fails on /dev/fd
// on darwin because it stats entries that include its own descriptor.
func countEntries(dir string) (int, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	return len(names), err == nil
}

// settledGoroutines waits for goroutines to exit and returns the final count.
func settledGoroutines(want int) int {
	n := runtime.NumGoroutine()
	for deadline := time.Now().Add(3 * time.Second); n > want && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func reloadFixture(t *testing.T) string {
	t.Helper()
	files := map[string]string{}
	for i := range 20 {
		files[fmt.Sprintf("p%d/a.go", i)] = fmt.Sprintf("package p%d\n\ntype T struct{}\n\nfunc (T) M() {}\n", i)
		files[fmt.Sprintf("p%d/a_test.go", i)] = fmt.Sprintf("package p%d\n\nimport \"testing\"\n\nfunc TestT(t *testing.T) { T{}.M() }\n", i)
	}
	return writeInlineWorkspace(t, files)
}

func assertNoGrowth(t *testing.T, fdsBefore, goroutinesBefore int) {
	t.Helper()
	if got := settledGoroutines(goroutinesBefore); got > goroutinesBefore {
		t.Errorf("goroutines grew from %d to %d across %d reloads", goroutinesBefore, got, reloadCount)
	}
	fdsAfter := openFDs(t)
	t.Logf("fds %d -> %d", fdsBefore, fdsAfter)
	if fdsAfter > fdsBefore+8 {
		t.Errorf("open fds grew from %d to %d across %d reloads", fdsBefore, fdsAfter, reloadCount)
	}
}

func TestLoadWorkspace_ReloadDoesNotLeakDescriptorsOrGoroutines(t *testing.T) {
	dir := reloadFixture(t)
	s := mcp.NewMCPServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s.LoadWorkspace(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	fdsBefore, goroutinesBefore := openFDs(t), runtime.NumGoroutine()

	for range reloadCount {
		if _, err := s.LoadWorkspace(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	assertNoGrowth(t, fdsBefore, goroutinesBefore)
}

func TestLoadWorkspace_FailedLoadLeavesNoWatcher(t *testing.T) {
	dir := reloadFixture(t)
	s := mcp.NewMCPServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	fdsBefore := openFDs(t)
	if _, err := s.LoadWorkspace(context.Background(), dir); err != nil {
		t.Fatal(err)
	}

	if _, err := s.LoadWorkspace(context.Background(), dir+"/does-not-exist"); err == nil {
		t.Fatal("expected load of a missing path to fail")
	}
	if got := openFDs(t); got > fdsBefore+8 {
		t.Errorf("fds after failed load = %d, want about the %d held before any load", got, fdsBefore)
	}
	s.Close()
}

func TestLoadWorkspaceTool_ReloadWithOperationsDoesNotLeak(t *testing.T) {
	dir := reloadFixture(t)
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcptest.InProcess(), dir)
	defer sess.Close()
	fdsBefore, goroutinesBefore := openFDs(t), runtime.NumGoroutine()

	for range reloadCount {
		callJSON(t, sess, "load_workspace", map[string]any{"path": dir})
		callJSON(t, sess, "rename_method", map[string]any{"type_name": "T", "method_name": "M", "new_method_name": "N", "dry_run": true})
	}
	assertNoGrowth(t, fdsBefore, goroutinesBefore)
}
