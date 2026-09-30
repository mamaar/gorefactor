package refactor

import (
	"go/ast"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// buildScopeWorkspace writes files to a temp module and returns a fully
// type-checked workspace. Every predicate here is go/types-based; without
// forcing type-checking, TypesPkg/TypesInfo stay nil and everything
// silently degrades to ctxUnknown/false, masking real bugs as passing
// (vacuously) tests.
func buildScopeWorkspace(t *testing.T, files map[string]string) *pkgtypes.Workspace {
	t.Helper()
	engine, ws := writeRenameFieldWorkspace(t, files)
	de, ok := engine.(*DefaultEngine)
	if !ok {
		t.Fatalf("engine is not *DefaultEngine")
	}
	de.EnsureTypeChecked(ws)
	return ws
}

// findPackageByName returns the single loaded package named name.
func findPackageByName(t *testing.T, ws *pkgtypes.Workspace, name string) *pkgtypes.Package {
	t.Helper()
	for _, pkg := range ws.Packages {
		if pkg.Name == name {
			return pkg
		}
	}
	t.Fatalf("package %q not found in workspace", name)
	return nil
}

func findFuncDecl(pkg *pkgtypes.Package, name string) *ast.FuncDecl {
	for _, f := range allScopeTestFiles(pkg) {
		for _, decl := range f.AST.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Recv == nil {
				return fd
			}
		}
	}
	return nil
}

func allScopeTestFiles(pkg *pkgtypes.Package) []*pkgtypes.File {
	var files []*pkgtypes.File
	for _, f := range pkg.Files {
		files = append(files, f)
	}
	for _, f := range pkg.TestFiles {
		files = append(files, f)
	}
	return files
}

// findCallTo returns the first CallExpr within root whose callee is the
// bare identifier name (e.g. a marker call like "mark()").
func findCallTo(root ast.Node, name string) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			found = call
			return false
		}
		return true
	})
	return found
}

func findFuncLit(root ast.Node) *ast.FuncLit {
	var found *ast.FuncLit
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if lit, ok := n.(*ast.FuncLit); ok {
			found = lit
			return false
		}
		return true
	})
	return found
}

func TestFindCtxSource_NamedParam(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F(c context.Context) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if !ok || src.Kind != ctxInScope || src.Expr != "c" {
		t.Fatalf("expected ctxInScope %q, got %+v ok=%v", "c", src, ok)
	}
}

func TestFindCtxSource_AliasedImport(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import ctxpkg "context"

func F(ctx ctxpkg.Context) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if !ok || src.Kind != ctxInScope || src.Expr != "ctx" {
		t.Fatalf("expected ctxInScope %q despite import alias, got %+v ok=%v", "ctx", src, ok)
	}
}

func TestFindCtxSource_UnnamedParamNotUsable(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F(context.Context) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if ok || src.Kind != ctxNeedsParam {
		t.Fatalf("expected ctxNeedsParam (unnamed param is unusable), got %+v ok=%v", src, ok)
	}
}

func TestFindCtxSource_BlankParamNotUsable(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F(_ context.Context) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if ok || src.Kind != ctxNeedsParam {
		t.Fatalf("expected ctxNeedsParam (blank param is unusable), got %+v ok=%v", src, ok)
	}
}

func TestFindCtxSource_HTTPRequestParam(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "net/http"

func Handler(w http.ResponseWriter, req *http.Request) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "Handler")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if !ok || src.Kind != ctxFromRequest || src.Expr != "req.Context()" {
		t.Fatalf("expected ctxFromRequest %q (proving the name isn't hardcoded to r), got %+v ok=%v", "req.Context()", src, ok)
	}
}

func TestFindCtxSource_FuncLitCapturesOuterCtx(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F(ctx context.Context) {
	fn := func() {
		mark()
	}
	fn()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	lit := findFuncLit(fn.Body)
	call := findCallTo(lit.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if !ok || src.Kind != ctxInScope || src.Expr != "ctx" {
		t.Fatalf("expected the scope walk to see the captured outer ctx, got %+v ok=%v", src, ok)
	}
}

func TestFindCtxSource_PackageScopeVarExcluded(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

var ctx context.Context

func F() {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if ok || src.Kind != ctxNeedsParam {
		t.Fatalf("a package-level var ctx must not be picked up by the scope walk, got %+v ok=%v", src, ok)
	}
}

func TestFindCtxSource_VarDeclaredAfterCallNotUsable(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F() {
	mark()
	ctx := context.Background()
	_ = ctx
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	call := findCallTo(fn.Body, "mark")

	src, ok := findCtxSource(pkg, fn, call)
	if ok || src.Kind != ctxNeedsParam {
		t.Fatalf("a ctx declared after the call must not be usable at the call, got %+v ok=%v", src, ok)
	}
}

func TestIsFakeRootCtxCreation_Background(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F() {
	ctx := context.Background()
	_ = ctx
}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")

	assign, ok := isFakeRootCtxCreation(fn)
	if !ok || assign == nil {
		t.Fatalf("expected ctx := context.Background() to be reported as a fake root")
	}
}

func TestIsFakeRootCtxCreation_RequestContextIsRealNotFake(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import "net/http"

func F(r *http.Request) {
	ctx := r.Context()
	_ = ctx
}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")

	_, ok := isFakeRootCtxCreation(fn)
	if ok {
		t.Fatalf("ctx := r.Context() is a real derived context, must NOT be reported as a fake root")
	}
}

func TestIsFakeRootCtxCreation_WithTimeoutIsRealNotFake(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

import (
	"context"
	"time"
)

func F(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	_ = ctx
}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")

	_, ok := isFakeRootCtxCreation(fn)
	if ok {
		t.Fatalf("ctx, cancel := context.WithTimeout(...) is a real derived context, must NOT be reported as a fake root")
	}
}

func TestClassifyRoot_TestFunc(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/pkg.go":      "package p\n",
		"p/foo_test.go": `package p

import "testing"

func TestFoo(t *testing.T) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "TestFoo")
	file := pkg.TestFiles["foo_test.go"]

	src, ok := classifyRoot(pkg, file, fn, nil)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "t.Context()" {
		t.Fatalf("expected TestFoo to be a root with t.Context(), got %+v ok=%v", src, ok)
	}
}

func TestClassifyRoot_BenchmarkFunc(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/pkg.go":      "package p\n",
		"p/foo_test.go": `package p

import "testing"

func BenchmarkFoo(b *testing.B) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "BenchmarkFoo")
	file := pkg.TestFiles["foo_test.go"]

	src, ok := classifyRoot(pkg, file, fn, nil)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "b.Context()" {
		t.Fatalf("expected BenchmarkFoo to be a root with b.Context(), got %+v ok=%v", src, ok)
	}
}

func TestClassifyRoot_TestMain(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/pkg.go":      "package p\n",
		"p/foo_test.go": `package p

import "testing"

func TestMain(m *testing.M) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "TestMain")
	file := pkg.TestFiles["foo_test.go"]

	src, ok := classifyRoot(pkg, file, fn, nil)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "context.TODO()" {
		t.Fatalf("expected TestMain to be a root with context.TODO() (testing.M has no Context()), got %+v ok=%v", src, ok)
	}
}

// A helper taking *testing.T is a root, like a Test declaration.
func TestClassifyRoot_TestHelperIsRoot(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/pkg.go":      "package p\n",
		"p/foo_test.go": `package p

import "testing"

func helper(t *testing.T) {
	mark()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "helper")
	file := pkg.TestFiles["foo_test.go"]

	src, ok := classifyRoot(pkg, file, fn, nil)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "t.Context()" {
		t.Fatalf("expected helper(t *testing.T) to be a root with t.Context(), got %+v ok=%v", src, ok)
	}
}

func TestClassifyRoot_TRunSubtestClosure(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/pkg.go":      "package p\n",
		"p/foo_test.go": `package p

import "testing"

func TestFoo(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		mark()
	})
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	testFn := findFuncDecl(pkg, "TestFoo")
	file := pkg.TestFiles["foo_test.go"]
	lit := findFuncLit(testFn.Body)

	src, ok := classifyRoot(pkg, file, nil, lit)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "t.Context()" {
		t.Fatalf("expected t.Run subtest closure to be a root with t.Context(), got %+v ok=%v", src, ok)
	}
}

func TestClassifyRoot_OtherFuncLitIsRoot(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

func F() {
	go func() {
		mark()
	}()
}

func mark() {}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")
	lit := findFuncLit(fn.Body)

	src, ok := classifyRoot(pkg, nil, nil, lit)
	if !ok || src.Kind != ctxRootLiteral || src.Expr != "context.TODO()" {
		t.Fatalf("expected go func(){}() closure to be a root with context.TODO(), got %+v ok=%v", src, ok)
	}
}

func TestUniqueCtxName_ShadowedByNonContextLocal(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

func F() {
	ctx := 5
	_ = ctx
}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")

	name := uniqueCtxName(pkg, fn)
	if name != "ctx0" {
		t.Fatalf("expected uniqueCtxName to return %q when ctx is shadowed by a non-context local, got %q", "ctx0", name)
	}
}

func TestUniqueCtxName_NoCollision(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"p/foo.go": `package p

func F() {
	x := 5
	_ = x
}
`,
	})
	pkg := findPackageByName(t, ws, "p")
	fn := findFuncDecl(pkg, "F")

	name := uniqueCtxName(pkg, fn)
	if name != "ctx" {
		t.Fatalf("expected uniqueCtxName to return %q when there is no collision, got %q", "ctx", name)
	}
}
