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
	res := moveToFile(t, sess, args)
	if res.IsError {
		t.Fatalf("dry run: %s", errText(res))
	}
	delete(args, "dry_run")
	res = moveToFile(t, sess, args)
	if res.IsError {
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

	res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "p", "to_package": "p", "to_file": "p/b.go"})
	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}

	assertFileContains(t, dir, "p/b.go", "package p")
	assertFileContains(t, dir, "p/b.go", "func Foo")
	got := fileText(t, dir, "p/a.go")
	if strings.Contains(got, "func Foo") || !strings.Contains(got, "Foo()") {
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

	res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst", "to_file": "dst/extra.go"})
	if res.IsError {
		t.Fatalf("move Foo: %s", errText(res))
	}
	assertFileContains(t, dir, "dst/extra.go", "package dst")
	assertFileContains(t, dir, "dst/extra.go", "func Foo")
	assertFileContains(t, dir, "use/use.go", "dst.Foo()")
	if fileExists(dir, "dst/dst.go") && strings.Contains(fileText(t, dir, "dst/dst.go"), "Foo") {
		t.Error("Foo went to the wrong file")
	}

	res = moveToFile(t, sess, map[string]any{"symbol": "Bar", "from_package": "src", "to_package": "dst", "to_file": "dst/dst_test.go"})
	if res.IsError {
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
			got := strings.Contains(errText(dry), `"action": "delete"`) || strings.Contains(errText(dry), `"action":"delete"`)
			if got != tc.deleted {
				t.Errorf("dry run delete action = %v, want %v:\n%s", got, tc.deleted, errText(dry))
			}
			res := moveToFile(t, sess, map[string]any{"symbol": "Foo", "from_package": "src", "to_package": "dst"})
			if res.IsError {
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

func TestMoveSymbol_ToFile_MovesAMethodAloneWithinItsPackage(t *testing.T) {
	for _, name := range []string{"T.Get", "Get"} {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"p/t.go":     "package p\n\nimport \"strings\"\n\ntype T struct{ s string }\n\nfunc (t T) Get() string { return strings.ToUpper(t.s) }\n\nfunc (t T) Keep() string { return t.s }\n",
				"use/use.go": "package use\n\nimport \"example.com/threadmcp/p\"\n\nfunc Use() string { return p.T{}.Get() }\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			args := map[string]any{"symbol": name, "from_package": "p", "to_package": "p", "to_file": "p/get.go"}
			args["dry_run"] = true
			res := moveToFile(t, sess, args)
			if res.IsError {
				t.Fatalf("dry run: %s", errText(res))
			}
			delete(args, "dry_run")
			res = moveToFile(t, sess, args)
			if res.IsError {
				t.Fatalf("move_symbol: %s", errText(res))
			}

			assertFileContains(t, dir, "p/get.go", "func (t T) Get()")
			assertFileContains(t, dir, "p/get.go", `"strings"`)
			got := fileText(t, dir, "p/t.go")
			if strings.Contains(got, "Get") || strings.Contains(got, `"strings"`) || !strings.Contains(got, "Keep") {
				t.Errorf("t.go:\n%s", got)
			}
			assertFileContains(t, dir, "use/use.go", "p.T{}.Get()")
			goVets(t, dir, "")
		})
	}
}

func TestMoveSymbol_ToFile_MethodIntoTestFileFollowsItsCallers(t *testing.T) {
	files := map[string]string{
		"p/t.go":      "package p\n\ntype T struct{ n int }\n\nfunc (t T) Dbg() int { return t.n }\n\nfunc (t T) Live() int { return t.n }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = T{}.Dbg(); _ = T{}.Live() }\n",
		"p/use.go":    "package p\n\nfunc Use() int { return T{}.Live() }\n",
	}
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "T.Live", "from_package": "p", "to_package": "p", "to_file": "p/p_test.go"})
	if !res.IsError || !strings.Contains(errText(res), "use.go") {
		t.Errorf("want a refusal naming use.go, got error=%v %s", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/t.go", files["p/t.go"])

	if res := moveToFile(t, sess, map[string]any{"symbol": "T.Dbg", "from_package": "p", "to_package": "p", "to_file": "p/p_test.go"}); res.IsError {
		t.Fatalf("move Dbg: %s", errText(res))
	}
	assertFileContains(t, dir, "p/p_test.go", "func (t T) Dbg()")
	goVets(t, dir, "")
}

func TestMoveSymbol_ToFile_MethodStillRefusedAcrossPackages(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"src/src.go": memberSrc, "dst/dst.go": "package dst\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "T.M", "from_package": "src", "to_package": "dst", "to_file": "dst/m.go"})
	assertRefused(t, res, "moves with its type")
	if fileExists(dir, "dst/m.go") {
		t.Error("a refused move created a file")
	}
}

func TestMoveSymbol_ToFile_MovesASymbolOutOfATestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":        "package p\n\nfunc Live() int { return 1 }\n",
		"p/a_test.go":   "package p\n\nimport \"testing\"\n\nfunc helper() int { return Live() }\n\nfunc TestA(t *testing.T) { _ = helper() }\n",
		"p/b_test.go":   "package p\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { _ = helper() }\n",
		"p/m_test.go":   "package p\n\ntype fake struct{}\n\nfunc (fake) Do() int { return 1 }\n",
		"p/use_test.go": "package p\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) { _ = fake{}.Do() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "helper", "from_package": "p", "to_package": "p", "to_file": "p/b_test.go"})
	if res.IsError {
		t.Fatalf("move helper: %s", errText(res))
	}
	assertFileContains(t, dir, "p/b_test.go", "func helper")
	if strings.Contains(fileText(t, dir, "p/a_test.go"), "func helper") {
		t.Error("helper was not removed from a_test.go")
	}

	res = moveToFile(t, sess, map[string]any{"symbol": "fake.Do", "from_package": "p", "to_package": "p", "to_file": "p/use_test.go"})
	if res.IsError {
		t.Fatalf("move Do: %s", errText(res))
	}
	assertFileContains(t, dir, "p/use_test.go", "func (fake) Do()")
	goVets(t, dir, "")
}

func TestMoveSymbol_ToFile_RefusesAMethodOfAnInterfaceIntoATestFile(t *testing.T) {
	for _, tc := range []struct {
		name, method, iface string
	}{
		{"fmt.Stringer", "String", "func (t T) String() string { return \"t\" }\n\nfunc Show(t T) { fmt.Print(t) }\n"},
		{"workspace interface", "Name", "func (t T) Name() string { return \"t\" }\n\ntype Namer interface{ Name() string }\n\nfunc Show(t T) { var _ Namer = t }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"p/t.go":      "package p\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n\ntype T struct{}\n\n" + tc.iface,
				"p/t_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { Show(T{}) }\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := moveToFile(t, sess, map[string]any{"symbol": "T." + tc.method, "from_package": "p", "to_package": "p", "to_file": "p/t_test.go", "dry_run": true})

			if !res.IsError || !strings.Contains(errText(res), "satisfies") {
				t.Fatalf("want an interface refusal, got error=%v: %s", res.IsError, errText(res))
			}
		})
	}
}

func TestMoveSymbol_ToFile_MovesAMethodNoInterfaceNeeds(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":      "package p\n\ntype T struct{}\n\nfunc (t T) Helper() int { return 1 }\n",
		"p/t_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = T{}.Helper() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "T.Helper", "from_package": "p", "to_package": "p", "to_file": "p/t_test.go"})

	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}
	assertFileContains(t, dir, "p/t_test.go", "func (t T) Helper")
}

func TestMoveSymbol_ToFile_MovesAnInterfaceNamedMethodOfATypeNeverConverted(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":      "package p\n\ntype T struct{}\n\nfunc (t T) String() string { return \"t\" }\n",
		"p/t_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = T{}.String() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := moveToFile(t, sess, map[string]any{"symbol": "T.String", "from_package": "p", "to_package": "p", "to_file": "p/t_test.go"})

	if res.IsError {
		t.Fatalf("move_symbol: %s", errText(res))
	}
	assertFileContains(t, dir, "p/t_test.go", "func (t T) String")
}
