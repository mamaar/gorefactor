package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestMoveSymbolToNonExistentPackage is a regression test for a panic in
// move_symbol when the target package directory does not exist on disk at
// all (no directory, no .go file) at the time of the call.
//
// Root cause: MoveSymbolOperation.getOrCreateTargetFile
// (pkg/refactor/operations.go) fabricated a placeholder *types.File for the
// newly-created target file with a nil AST, and inserted it into
// targetPackage.Files keyed by the file's *full path* instead of its base
// filename (the convention used everywhere else pkg.Files is populated).
//
// After the plan was executed, internal/mcp.executePlan re-syncs the
// workspace from disk (SyncWorkspaceChanges -> WorkspaceUpdater), which
// re-parses the same file and inserts a correctly-parsed entry keyed by
// base filename. Because the key didn't match, the old nil-AST entry was
// never replaced and lingered in the map. The very next call to
// WorkspaceUpdater.recollectImports (pkg/watch/updater.go) then iterated
// over both entries and dereferenced the nil AST on the stale one,
// panicking with "invalid memory address or nil pointer dereference".
//
// Separately: only move_symbol had its own ad-hoc recover(); every other
// tool handler had none, so a panic like this one (or any future one) in
// any of ~14 other tool files would unwind past the SDK's per-request
// goroutine and kill the whole server process, disconnecting every client
// session. See internal/mcp/recovery.go for the fix (a single
// AddReceivingMiddleware wired in RegisterAllTools covering every tool).
func TestMoveSymbolToNonExistentPackage(t *testing.T) {
	tmpDir := t.TempDir()
	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(tmpDir, "common", "memcache.go"), `package common

type MemCache struct {
	data map[string][]byte
}

func NewMemCache() *MemCache {
	return &MemCache{data: make(map[string][]byte)}
}
`)

	// Sanity check: the target package directory must not exist yet - that's
	// exactly the condition that triggered the panic.
	_, err := os.Stat(filepath.Join(tmpDir, "pkg", "memcache"))
	if !os.IsNotExist(err) {
		t.Fatalf("test setup invariant violated: pkg/memcache must not exist yet, stat err=%v", err)
	}

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "MemCache",
			"from_package": "common",
			"to_package":   "pkg/memcache",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error (server likely crashed/disconnected): %v", err)
	}

	if result.IsError {
		t.Fatalf("move_symbol returned an error result: %+v", result.Content)
	}

	targetPath := filepath.Join(tmpDir, "pkg", "memcache", "memcache.go")

	targetContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("expected %s to have been created: %v", targetPath, err)
	}

	if !strings.Contains(string(targetContent), "type MemCache struct") {
		t.Errorf("expected MemCache type in newly-created package, got:\n%s", targetContent)
	}

	commonPath := filepath.Join(tmpDir, "common", "memcache.go")

	commonContent, err := os.ReadFile(commonPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading %s: %v", commonPath, err)
	}

	if strings.Contains(string(commonContent), "type MemCache struct") {
		t.Errorf("expected MemCache type to have been removed from source package, got:\n%s", commonContent)
	}
}

// TestMoveSymbolToNewPackageByImportPath covers the destination form that
// getOrCreateTargetFile's original ad-hoc fix did not handle. That fix
// anchored a non-absolute target at the workspace root, which is right for
// "pkg/memcache" but wrong for the module import path "reprotest/pkg/memcache":
// joining that onto the root creates a directory named after the module,
// inside the module. It now resolves through types.ResolveNewPackagePath,
// which strips the module prefix first.
func TestMoveSymbolToNewPackageByImportPath(t *testing.T) {
	tmpDir := t.TempDir()
	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(tmpDir, "common", "memcache.go"), `package common

type MemCache struct {
	data map[string][]byte
}
`)

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "MemCache",
			"from_package": "common",
			"to_package":   "reprotest/pkg/memcache",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}

	if result.IsError {
		t.Fatalf("move_symbol returned an error result: %+v", result.Content)
	}

	targetPath := filepath.Join(tmpDir, "pkg", "memcache", "memcache.go")

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("expected %s to have been created: %v", targetPath, err)
	}

	if !strings.Contains(string(got), "type MemCache struct") {
		t.Errorf("expected MemCache in the new package, got:\n%s", got)
	}

	// The failure mode this guards against: a directory named after the module.
	if _, err := os.Stat(filepath.Join(tmpDir, "reprotest")); !os.IsNotExist(err) {
		t.Errorf("a directory named after the module was created inside the module, stat err=%v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	err = os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
