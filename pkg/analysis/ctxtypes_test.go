package analysis

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"testing"
)

// typeCheckSrc type-checks src as a standalone package and returns the
// resulting *gotypes.Package, matching the pattern used elsewhere in this
// package (see createTypedTestWorkspace in symbols_test.go) rather than
// hand-rolling a different Config. importPath is the go/types import path
// to assign the package (distinct from its package clause name, since Go
// allows a package's declared name and its import path to differ — that
// distinction is exactly what the local-"context"-package test case below
// exercises).
func typeCheckSrc(t *testing.T, importPath, src string) *gotypes.Package {
	t.Helper()
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	conf := gotypes.Config{Importer: importer.Default()}
	info := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	typesPkg, err := conf.Check(importPath, fset, []*ast.File{astFile}, info)
	if err != nil {
		t.Fatalf("type-checking failed: %v", err)
	}
	return typesPkg
}

// paramType returns the type of the i-th parameter of the package-level
// function named funcName.
func paramType(t *testing.T, pkg *gotypes.Package, funcName string, i int) gotypes.Type {
	t.Helper()
	obj := pkg.Scope().Lookup(funcName)
	if obj == nil {
		t.Fatalf("function %q not found in package", funcName)
	}
	sig, ok := obj.Type().(*gotypes.Signature)
	if !ok {
		t.Fatalf("%q is not a function", funcName)
	}
	if i >= sig.Params().Len() {
		t.Fatalf("function %q has no parameter %d", funcName, i)
	}
	return sig.Params().At(i).Type()
}

func TestIsContextType(t *testing.T) {
	t.Run("plain import", func(t *testing.T) {
		src := `package p
import "context"
func f(ctx context.Context) {}
`
		pkg := typeCheckSrc(t, "p", src)
		if !IsContextType(paramType(t, pkg, "f", 0)) {
			t.Error("expected context.Context to be recognized")
		}
	})

	t.Run("aliased import", func(t *testing.T) {
		// This is the case the old AST-based isContextType in
		// pkg/analyzers/missingctx gets wrong: it matches the identifier
		// "context", so renaming the import breaks it.
		src := `package p
import ctxpkg "context"
func f(ctx ctxpkg.Context) {}
`
		pkg := typeCheckSrc(t, "p", src)
		if !IsContextType(paramType(t, pkg, "f", 0)) {
			t.Error("expected ctxpkg.Context (aliased import) to be recognized as context.Context")
		}
	})

	t.Run("type alias declaration", func(t *testing.T) {
		src := `package p
import "context"
type Ctx = context.Context
func f(ctx Ctx) {}
`
		pkg := typeCheckSrc(t, "p", src)
		if !IsContextType(paramType(t, pkg, "f", 0)) {
			t.Error("expected type alias Ctx = context.Context to be recognized")
		}
	})

	t.Run("local type named Context is not context.Context", func(t *testing.T) {
		// Package *name* is "context" (like the real stdlib package), but its
		// import path is "example.com/localcontext" — distinguishing name from
		// path is exactly what the old AST-identifier-matching approach cannot do.
		src := `package context
type Context struct{}
func f(ctx Context) {}
`
		pkg := typeCheckSrc(t, "example.com/localcontext", src)
		if IsContextType(paramType(t, pkg, "f", 0)) {
			t.Error("local Context struct in a package named context must not match context.Context")
		}
	})

	t.Run("universe types are not context.Context", func(t *testing.T) {
		src := `package p
func f(e error, a any) {}
`
		pkg := typeCheckSrc(t, "p", src)
		if IsContextType(paramType(t, pkg, "f", 0)) {
			t.Error("error should not be recognized as context.Context")
		}
		if IsContextType(paramType(t, pkg, "f", 1)) {
			t.Error("any should not be recognized as context.Context")
		}
	})

	t.Run("nil type", func(t *testing.T) {
		if IsContextType(nil) {
			t.Error("nil type must not be recognized as context.Context")
		}
	})
}

func TestIsHTTPRequestPtr(t *testing.T) {
	src := `package p
import "net/http"
func f(r *http.Request, notPtr http.Request) {}
`
	pkg := typeCheckSrc(t, "p", src)
	if !IsHTTPRequestPtr(paramType(t, pkg, "f", 0)) {
		t.Error("expected *http.Request to be recognized")
	}
	if IsHTTPRequestPtr(paramType(t, pkg, "f", 1)) {
		t.Error("bare http.Request (non-pointer) must not be recognized")
	}
}

func TestIsTestingTBAndM(t *testing.T) {
	src := `package p
import "testing"
func f(t2 *testing.T, b *testing.B, ff *testing.F, m *testing.M) {}
`
	pkg := typeCheckSrc(t, "p", src)

	if !IsTestingTB(paramType(t, pkg, "f", 0)) {
		t.Error("expected *testing.T to be recognized by IsTestingTB")
	}
	if !IsTestingTB(paramType(t, pkg, "f", 1)) {
		t.Error("expected *testing.B to be recognized by IsTestingTB")
	}
	if !IsTestingTB(paramType(t, pkg, "f", 2)) {
		t.Error("expected *testing.F to be recognized by IsTestingTB")
	}
	if IsTestingTB(paramType(t, pkg, "f", 3)) {
		t.Error("*testing.M must not be recognized by IsTestingTB")
	}
	if !IsTestingM(paramType(t, pkg, "f", 3)) {
		t.Error("expected *testing.M to be recognized by IsTestingM")
	}
}

func TestIsTestingTBInterface(t *testing.T) {
	src := `package p
import "testing"
func f(tb testing.TB, v testing.T) {}
`
	pkg := typeCheckSrc(t, "p", src)
	if !IsTestingTB(paramType(t, pkg, "f", 0)) {
		t.Error("expected testing.TB interface to be recognized by IsTestingTB")
	}
	if IsTestingTB(paramType(t, pkg, "f", 1)) {
		t.Error("non-pointer testing.T must not be recognized by IsTestingTB")
	}
}

func TestPredicatesNilSafe(t *testing.T) {
	// Must not panic on nil input.
	_ = IsContextType(nil)
	_ = IsHTTPRequestPtr(nil)
	_ = IsTestingTB(nil)
	_ = IsTestingM(nil)
}
