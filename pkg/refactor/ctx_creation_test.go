package refactor

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// writeTemp writes src to a temp file and returns its path.
func writeTemp(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.go")

	err := os.WriteFile(path, []byte(src), 0o644)
	if err != nil {
		t.Fatalf("writeTemp: %v", err)
	}

	return path
}

// buildWorkspace creates a minimal Workspace with a single file parsed from src.
func buildWorkspace(t *testing.T, path, src string) *pkgtypes.Workspace {
	t.Helper()

	fset := token.NewFileSet()

	astFile, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	file := &pkgtypes.File{
		Path:            path,
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	ws := &pkgtypes.Workspace{
		FileSet: fset,
		Packages: map[string]*pkgtypes.Package{
			"main": {
				Path: "main",
				Files: map[string]*pkgtypes.File{
					filepath.Base(path): file,
				},
				TestFiles: map[string]*pkgtypes.File{},
			},
		},
	}

	return ws
}

// TestRemoveInternalCtxCreation_Background verifies that a "ctx := context.Background()"
// statement inside a function body is detected and removed — Bug 2 regression test.
func TestRemoveInternalCtxCreation_Background(t *testing.T) {
	src := `package main

import (
	"context"
	"fmt"
)

func getSecret(project string, secretName string, version string) (secret []byte, err error) {
	ctx := context.Background()
	_ = ctx
	return secret, fmt.Errorf("not implemented")
}
`
	path := writeTemp(t, src)
	ws := buildWorkspace(t, path, src)

	changes := RemoveInternalCtxCreation(ws, path, "getSecret")

	if len(changes) != 1 {
		t.Fatalf("expected 1 change (remove ctx := context.Background()), got %d: %v", len(changes), changes)
	}

	c := changes[0]
	if c.NewText != "" {
		t.Errorf("expected NewText to be empty (deletion), got %q", c.NewText)
	}

	if !strings.Contains(c.OldText, "context.Background") {
		t.Errorf("expected OldText to contain 'context.Background', got %q", c.OldText)
	}

	if !strings.Contains(c.Description, "ctx is now a parameter") {
		t.Errorf("expected description to mention 'ctx is now a parameter', got %q", c.Description)
	}
}

// TestRemoveInternalCtxCreation_TODO verifies that context.TODO() is also removed.
func TestRemoveInternalCtxCreation_TODO(t *testing.T) {
	src := `package main

import "context"

func listVersions(project string) ([]string, error) {
	ctx := context.TODO()
	_ = ctx
	return nil, nil
}
`
	path := writeTemp(t, src)
	ws := buildWorkspace(t, path, src)

	changes := RemoveInternalCtxCreation(ws, path, "listVersions")

	if len(changes) != 1 {
		t.Fatalf("expected 1 change (remove ctx := context.TODO()), got %d: %v", len(changes), changes)
	}

	if !strings.Contains(changes[0].OldText, "context.TODO") {
		t.Errorf("expected OldText to contain 'context.TODO', got %q", changes[0].OldText)
	}
}

// TestRemoveInternalCtxCreation_NoInternalCtx verifies no changes are emitted when
// the function does not create a root context internally.
func TestRemoveInternalCtxCreation_NoInternalCtx(t *testing.T) {
	src := `package main

func doWork(project string) error {
	return nil
}
`
	path := writeTemp(t, src)
	ws := buildWorkspace(t, path, src)

	changes := RemoveInternalCtxCreation(ws, path, "doWork")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes, got %d: %v", len(changes), changes)
	}
}

// TestRemoveInternalCtxCreation_NestedClosureNotRemoved verifies that a ctx assignment
// inside a nested closure is NOT removed (it is not in the top-level statement list).
func TestRemoveInternalCtxCreation_NestedClosureNotRemoved(t *testing.T) {
	src := `package main

import "context"

func doWork(project string) error {
	fn := func() {
		ctx := context.Background()
		_ = ctx
	}
	fn()
	return nil
}
`
	path := writeTemp(t, src)
	ws := buildWorkspace(t, path, src)

	changes := RemoveInternalCtxCreation(ws, path, "doWork")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes (nested closure should be untouched), got %d", len(changes))
	}
}

// TestRemoveInternalCtxCreation_NamedReturnsIntegration checks that a function with
// named returns AND an internal ctx creation produces exactly one change (the ctx removal)
// and that change targets context.Background — combining both bug scenarios.
func TestRemoveInternalCtxCreation_NamedReturnsIntegration(t *testing.T) {
	src := `package main

import (
	"context"
	"fmt"
)

func getSecret(project string, secretName string, version string) (secret []byte, err error) {
	ctx := context.Background()
	_ = ctx
	return secret, fmt.Errorf("not found")
}
`
	path := writeTemp(t, src)
	ws := buildWorkspace(t, path, src)

	changes := RemoveInternalCtxCreation(ws, path, "getSecret")

	if len(changes) != 1 {
		t.Fatalf("expected exactly 1 change, got %d", len(changes))
	}

	if changes[0].NewText != "" {
		t.Errorf("removal change must have empty NewText, got %q", changes[0].NewText)
	}
}
