package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	dependentsA = "package a\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc unrelated() int { return 1 }\n"
	dependentsB = "package b\n\nimport \"example.com/threadmcp/a\"\n\nfunc Use() { a.T{}.M() }\n"
)

func editOnDisk(t *testing.T, dir, rel, old, replacement string) {
	t.Helper()

	path := filepath.Join(dir, rel)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, replacement, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenameMethod_FindsDependentAfterUnrelatedEditInDeclaringPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": dependentsA, "b/b.go": dependentsB})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{"type_name": "T", "method_name": "M", "new_method_name": "N", "package_path": "example.com/threadmcp/a", "dry_run": true}
	callJSON(t, sess, "rename_method", args)

	editOnDisk(t, dir, "a/a.go", "return 1", "return 2")

	args["dry_run"] = false
	callJSON(t, sess, "rename_method", args)

	assertFileContains(t, dir, "a/a.go", "return 2")
	assertFileContains(t, dir, "b/b.go", "a.T{}.N()")
}

func TestAddContextParameter_ThreadReachesDependentAfterUnrelatedEditInDeclaringPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": dependentsA, "b/b.go": dependentsB})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{"function_name": "T.M", "source_file": filepath.Join(dir, "a/a.go"), "thread": true, "dry_run": true}
	callJSON(t, sess, "add_context_parameter", args)

	editOnDisk(t, dir, "a/a.go", "return 1", "return 2")

	args["dry_run"] = false
	callJSON(t, sess, "add_context_parameter", args)

	assertFileContains(t, dir, "b/b.go", "func Use(ctx context.Context)")
	assertFileContains(t, dir, "b/b.go", "a.T{}.M(ctx)")
	goVets(t, dir, "")
}
