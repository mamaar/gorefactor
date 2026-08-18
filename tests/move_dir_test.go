package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestMoveDirRenamesAndRewrites is the primary regression for the move_dir
// rewrite. It exercises, in one move where the leaf directory's base name
// changes (a -> b, so package a is renamed to b):
//   - a _test.go file is moved (previously left behind in pkg.TestFiles),
//   - a moved file's self-reference to a sibling moved package (a/sub) is
//     rewritten to the new path,
//   - the originals are deleted (not left as 0-byte husks) and the now-empty
//     source directory is pruned,
//   - an outside importer's import path AND qualified references are updated.
func TestMoveDirRenamesAndRewrites(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "a", "sub", "sub.go"), `package sub

func Hello() string { return "hi" }
`)
	mustWrite(t, filepath.Join(dir, "a", "foo.go"), `package a

import "scratch/a/sub"

func Foo() string { return sub.Hello() }
`)
	mustWrite(t, filepath.Join(dir, "a", "foo_test.go"), `package a

import "testing"

func TestFoo(t *testing.T) {
	if Foo() == "" {
		t.Fatal("empty")
	}
}
`)
	mustWrite(t, filepath.Join(dir, "cmd", "main.go"), `package main

import "scratch/a"

func main() { _ = a.Foo() }
`)

	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "a", TargetDir: "b"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Errorf("source dir a/ should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "foo_test.go")); err != nil {
		t.Errorf("b/foo_test.go should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "sub", "sub.go")); err != nil {
		t.Errorf("b/sub/sub.go should exist: %v", err)
	}

	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}

	foo := read("b/foo.go")
	if !strings.Contains(foo, "package b") {
		t.Errorf("b/foo.go package clause not renamed:\n%s", foo)
	}
	if !strings.Contains(foo, `"scratch/b/sub"`) || strings.Contains(foo, `"scratch/a/sub"`) {
		t.Errorf("b/foo.go self-reference import not rewritten:\n%s", foo)
	}

	if !strings.Contains(read("b/foo_test.go"), "package b") {
		t.Errorf("b/foo_test.go package clause not renamed")
	}

	main := read("cmd/main.go")
	if !strings.Contains(main, `"scratch/b"`) {
		t.Errorf("cmd/main.go import not rewritten:\n%s", main)
	}
	if !strings.Contains(main, "b.Foo()") || strings.Contains(main, "a.Foo()") {
		t.Errorf("cmd/main.go qualified reference not rewritten:\n%s", main)
	}
}

// TestMoveDirRelocationKeepsName covers the common case where the leaf base
// name is unchanged (pkg/widget -> internal/widget): import paths update while
// the package name and qualified references stay put.
func TestMoveDirRelocationKeepsName(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "pkg", "widget", "widget.go"), `package widget

func Ping() string { return "pong" }
`)
	mustWrite(t, filepath.Join(dir, "cmd", "main.go"), `package main

import "scratch/pkg/widget"

func main() { _ = widget.Ping() }
`)

	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	plan, err := eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "pkg/widget", TargetDir: "internal/widget"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "pkg", "widget", "widget.go")); !os.IsNotExist(err) {
		t.Errorf("original should be deleted, stat err=%v", err)
	}
	moved, err := os.ReadFile(filepath.Join(dir, "internal", "widget", "widget.go"))
	if err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	if !strings.Contains(string(moved), "package widget") {
		t.Errorf("package name should be unchanged:\n%s", moved)
	}
	main, _ := os.ReadFile(filepath.Join(dir, "cmd", "main.go"))
	if !strings.Contains(string(main), `"scratch/internal/widget"`) {
		t.Errorf("import not rewritten:\n%s", main)
	}
	if !strings.Contains(string(main), "widget.Ping()") {
		t.Errorf("qualified reference should be unchanged:\n%s", main)
	}
}

// TestMoveDirViaMCP drives the actual move_dir MCP handler end-to-end (the
// path that supplies UpdateImports and executes the plan), asserting the
// originals are gone, the test file moved, and imports were rewritten.
func TestMoveDirViaMCP(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "a", "foo.go"), `package a

func Foo() string { return "x" }
`)
	mustWrite(t, filepath.Join(dir, "a", "foo_test.go"), `package a

import "testing"

func TestFoo(t *testing.T) { _ = Foo() }
`)
	mustWrite(t, filepath.Join(dir, "cmd", "main.go"), `package main

import "scratch/a"

func main() { _ = a.Foo() }
`)

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "move_dir",
		Arguments: map[string]any{"source_dir": "a", "target_dir": "b"},
	})
	if err != nil {
		t.Fatalf("CallTool transport error (server likely crashed): %v", err)
	}
	if result.IsError {
		t.Fatalf("move_dir returned an error result: %+v", result.Content)
	}

	if _, err := os.Stat(filepath.Join(dir, "a", "foo.go")); !os.IsNotExist(err) {
		t.Errorf("a/foo.go should be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b", "foo_test.go")); err != nil {
		t.Errorf("b/foo_test.go should exist: %v", err)
	}
	main, err := os.ReadFile(filepath.Join(dir, "cmd", "main.go"))
	if err != nil {
		t.Fatalf("read cmd/main.go: %v", err)
	}
	if !strings.Contains(string(main), `"scratch/b"`) {
		t.Errorf("cmd/main.go import not rewritten:\n%s", main)
	}
	if !strings.Contains(string(main), "b.Foo()") {
		t.Errorf("cmd/main.go qualified reference not rewritten:\n%s", main)
	}
}

// TestMovePackageMovesTestFilesAndDeletes is the shared-infra regression for
// move_package: _test.go files (here an external test package) must move and
// the originals must be deleted, not emptied.
func TestMovePackageMovesTestFilesAndDeletes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "old", "thing.go"), `package old

func Thing() int { return 42 }
`)
	mustWrite(t, filepath.Join(dir, "old", "thing_test.go"), `package old_test

import (
	"testing"

	"scratch/old"
)

func TestThing(t *testing.T) {
	if old.Thing() != 42 {
		t.Fatal("bad")
	}
}
`)

	eng := createEngine(t)
	ws := loadWorkspace(t, eng, dir)
	// An absolute target used to be the only form that worked: every other one
	// was written relative to the process's working directory. That is fixed
	// (see TestMovePackageTargetPathForms, which covers all three forms); this
	// stays absolute so the absolute path remains covered here too.
	plan, err := eng.MovePackage(ws, types.MovePackageRequest{
		SourcePackage: "old",
		TargetPackage: filepath.Join(dir, "new"),
	})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "old", "thing.go")); !os.IsNotExist(err) {
		t.Errorf("old/thing.go should be deleted, stat err=%v", err)
	}
	tf, err := os.ReadFile(filepath.Join(dir, "new", "thing_test.go"))
	if err != nil {
		t.Fatalf("new/thing_test.go should exist: %v", err)
	}
	if !strings.Contains(string(tf), "package new_test") {
		t.Errorf("external test package clause not renamed:\n%s", tf)
	}
	if !strings.Contains(string(tf), `"scratch/new"`) {
		t.Errorf("external test import not rewritten:\n%s", tf)
	}
}
