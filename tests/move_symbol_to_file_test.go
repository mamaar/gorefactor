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

func moveToFile(t *testing.T, sess *mcptest.Session, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	return callTool(t, sess, "move_symbol", args)
}

// fileTextOrEmpty is the text of dir/rel, empty when the file was deleted.
func fileTextOrEmpty(t *testing.T, dir, rel string) string {
	t.Helper()
	if !fileExists(dir, rel) {
		return ""
	}
	return fileText(t, dir, rel)
}

func fileExists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

func TestMoveSymbol_ToFile_MovesIntoTheTestFileOfItsOwnPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":      "package p\n\nimport \"strings\"\n\nfunc Live() int { return 1 }\n\nfunc ContainsAll(s string, subs ...string) bool {\n\tfor _, x := range subs {\n\t\tif !strings.Contains(s, x) {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n}\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif !ContainsAll(\"ab\", \"a\") || Live() != 1 {\n\t\tt.Fatal()\n\t}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{"symbol": "ContainsAll", "from_package": "p", "to_package": "p", "to_file": "p/p_test.go"}
	args["dry_run"] = true
	if res := moveToFile(t, sess, args); res.IsError {
		t.Fatalf("dry run: %s", errText(res))
	}
	delete(args, "dry_run")
	if res := moveToFile(t, sess, args); res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "p/p_test.go", "func ContainsAll")
	assertFileContains(t, dir, "p/p_test.go", `"strings"`)
	got := fileText(t, dir, "p/p.go")
	if strings.Contains(got, "ContainsAll") || strings.Contains(got, `"strings"`) {
		t.Errorf("p.go kept the symbol or its import:\n%s", got)
	}
	goVets(t, dir, "")
}

func TestMoveSymbol_ToFile_SamePackageWithoutToFileStaysAConflict(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/p.go": "package p\n\nfunc Foo() int { return 1 }\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "p", "to_package": "p"})
	if !res.IsError || !strings.Contains(errText(res), "already exists") {
		t.Errorf("want the name conflict, got error=%v %s", res.IsError, errText(res))
	}
}

func TestMoveSymbol_ToFile_MovesBetweenFilesOfOnePackageKeepingReferences(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/a.go":     "package p\n\nfunc helper() int { return 1 }\n\nfunc Foo() int { return helper() }\n\nfunc Bar() int { return Foo() }\n",
		"use/use.go": "package use\n\nimport \"example.com/threadmcp/p\"\n\nfunc Use() int { return p.Foo() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "p", "to_package": "p", "to_file": "p/b.go"}); res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "p/b.go", "package p")
	assertFileContains(t, dir, "p/b.go", "func Foo")
	if got := fileText(t, dir, "p/a.go"); strings.Contains(got, "func Foo") || !strings.Contains(got, "Foo()") {
		t.Errorf("a.go:\n%s", got)
	}
	assertFileContains(t, dir, "use/use.go", "p.Foo()")
	goVets(t, dir, "")
}

func TestMoveSymbol_ToFile_CreatesMissingFileAndUsesExistingTestFileOfAnotherPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go":      "package src\n\nfunc Foo() int { return 1 }\n\nfunc Bar() int { return 2 }\n",
		"dst/dst.go":      "package dst\n",
		"dst/dst_test.go": "package dst\n\nimport \"testing\"\n\nfunc TestD(t *testing.T) { _ = Bar() }\n",
		"use/use.go":      "package use\n\nimport \"example.com/threadmcp/src\"\n\nfunc Use() int { return src.Foo() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	if res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst", "to_file": "dst/extra.go"}); res.IsError {
		t.Fatalf("move Foo: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/extra.go", "package dst")
	assertFileContains(t, dir, "dst/extra.go", "func Foo")
	assertFileContains(t, dir, "use/use.go", "dst.Foo()")
	if fileExists(dir, "dst/dst.go") && strings.Contains(fileText(t, dir, "dst/dst.go"), "Foo") {
		t.Error("Foo went to the wrong file")
	}

	if res := moveToFile(t, sess, map[string]any{"symbol": "Bar", "from_package": "src", "to_package": "dst", "to_file": "dst/dst_test.go"}); res.IsError {
		t.Fatalf("move Bar: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/dst_test.go", "func Bar")
}

func TestMoveSymbol_ToFile_RefusesAFileOutsideTheTargetPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"src/src.go": "package src\n\nfunc Foo() int { return 1 }\n",
		"dst/dst.go": "package dst\n",
		"other/o.go": "package other\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, to := range []string{"other/o.go", "other/new.go", "dst/sub/new.go"} {
		res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst", "to_file": to})
		if !res.IsError || !strings.Contains(errText(res), "to_file") {
			t.Errorf("%s: want a to_file refusal, got error=%v %s", to, res.IsError, errText(res))
		}
	}
	if fileExists(dir, "other/new.go") || fileExists(dir, "dst/sub/new.go") {
		t.Error("a refused move created a file")
	}
}

func TestMoveSymbol_ToFile_RefusesTestFileWhenANonTestFileUsesTheSymbol(t *testing.T) {
	files := map[string]string{
		"p/p.go":      "package p\n\nfunc Helper() int { return 1 }\n\nfunc Live() int { return Helper() }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = Helper() }\n",
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "Helper", "from_package": "p", "to_package": "p", "to_file": "p/p_test.go"})
	if !res.IsError || !strings.Contains(errText(res), "p.go") {
		t.Errorf("want a refusal naming p.go, got error=%v %s", res.IsError, errText(res))
	}
	if fileText(t, dir, "p/p.go") != files["p/p.go"] || fileText(t, dir, "p/p_test.go") != files["p/p_test.go"] {
		t.Error("a refused move changed files")
	}
}

func TestMoveSymbol_DeletesTheSourceFileItEmptied(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		deleted   bool
	}{
		{"imports only remain", "package src\n\nimport \"strings\"\n\nfunc Foo() string { return strings.ToUpper(\"x\") }\n", true},
		{"build constraint", "//go:build !tagx\n\npackage src\n\nfunc Foo() int { return 1 }\n", false},
		{"package doc", "// Package src is documented here.\npackage src\n\nfunc Foo() int { return 1 }\n", false},
		{"another declaration", "package src\n\nfunc Foo() int { return 1 }\n\nfunc Other() int { return 2 }\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"src/foo.go":  tc.src,
				"src/keep.go": "package src\n\nfunc Keep() int { return 0 }\n",
				"dst/dst.go":  "package dst\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			dry := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst", "dry_run": true})
			if dry.IsError {
				t.Fatalf("dry run: %s", errText(dry))
			}
			if got := strings.Contains(errText(dry), `"action": "delete"`) || strings.Contains(errText(dry), `"action":"delete"`); got != tc.deleted {
				t.Errorf("dry run delete action = %v, want %v:\n%s", got, tc.deleted, errText(dry))
			}
			if res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst"}); res.IsError {
				t.Fatalf("move_symbol: %s", errText(res))
			}
			if fileExists(dir, "src/foo.go") == tc.deleted {
				t.Errorf("src/foo.go exists = %v, want %v", !tc.deleted, !tc.deleted)
			}
			goVets(t, dir, "", "tagx")
		})
	}
}

func TestSafeDelete_DeletesTheFileItEmptied(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":            "package p\n\nfunc Live() int { return 1 }\n",
		"p/dead.go":         "package p\n\nimport \"strings\"\n\nfunc dead() string { return strings.ToUpper(\"x\") }\n",
		"p/example_test.go": "package p\n\nimport \"fmt\"\n\nfunc ExampleLive() { fmt.Println(Live()) }\n",
		"p/tagged.go":       "//go:build !tagx\n\npackage p\n\nfunc tagged() int { return 1 }\n",
		"p/documented.go":   "// Package p is documented here.\npackage p\n\nfunc documented() int { return 1 }\n",
		"p/p_test.go":       "package p\n\nimport \"testing\"\n\nfunc TestLive(t *testing.T) { _ = Live() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, tc := range []struct {
		symbol, file string
		deleted      bool
	}{
		{"dead", "p/dead.go", true},
		{"ExampleLive", "p/example_test.go", true},
		{"tagged", "p/tagged.go", false},
		{"documented", "p/documented.go", false},
	} {
		callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": tc.file})
		if fileExists(dir, tc.file) == tc.deleted {
			t.Errorf("%s exists = %v, want %v", tc.file, !tc.deleted, !tc.deleted)
		}
	}
	goVets(t, dir, "", "tagx")
}

func TestBatchOperations_MoveSymbolCarriesToFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":      "package p\n\nfunc Helper() int { return 1 }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = Helper() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "batch_operations", batchArgs(false,
		`{"type":"move_symbol","symbol":"Helper","from_package":"p","to_package":"p","to_file":"p/p_test.go"}`))
	if res.IsError {
		t.Fatalf("batch_operations: %s", errText(res))
	}
	assertFileContains(t, dir, "p/p_test.go", "func Helper")
	if fileExists(dir, "p/p.go") {
		t.Errorf("p.go emptied but kept:\n%s", fileText(t, dir, "p/p.go"))
	}
}
