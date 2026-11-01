package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/ast/inspector"
)

// testIndex is a packageIndex over an in-memory package, plus the type-checked
// packages needed to resolve callees by name (including an optional dependency).
type testIndex struct {
	ix   *packageIndex
	pkgs []*gotypes.Package
}

const testDepPath = "example.com/dep"

// buildTestIndex type-checks src as package p, importing depSrc as example.com/dep
// when depSrc is non-empty, and indexes p.
func buildTestIndex(t *testing.T, src, depSrc string) *testIndex {
	t.Helper()

	fset := token.NewFileSet()
	imports := make(map[string]*gotypes.Package)

	check := func(path, filename, code string) (*gotypes.Package, *gotypes.Info, *ast.File) {
		t.Helper()
		f, err := parser.ParseFile(fset, filename, code, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		info := &gotypes.Info{
			Types: make(map[ast.Expr]gotypes.TypeAndValue),
			Defs:  make(map[*ast.Ident]gotypes.Object),
			Uses:  make(map[*ast.Ident]gotypes.Object),
		}
		conf := gotypes.Config{
			Importer: importerFunc(func(p string) (*gotypes.Package, error) {
				if pkg, ok := imports[p]; ok {
					return pkg, nil
				}
				return nil, fmt.Errorf("unknown import %q", p)
			}),
		}
		pkg, err := conf.Check(path, fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("type-check %s: %v", path, err)
		}
		return pkg, info, f
	}

	ti := &testIndex{}
	if depSrc != "" {
		depPkg, _, _ := check(testDepPath, "dep.go", depSrc)
		imports[testDepPath] = depPkg
		ti.pkgs = append(ti.pkgs, depPkg)
	}

	pkg, info, file := check("example.com/p", "p.go", src)
	ti.pkgs = append([]*gotypes.Package{pkg}, ti.pkgs...)
	ti.ix = newPackageIndex(inspector.New([]*ast.File{file}), pkg, info)
	return ti
}

// lookup resolves a callee by name across every type-checked package. "T.M" names
// the method M on the package-level type T; a bare name is looked up in package scope.
func (ti *testIndex) lookup(t *testing.T, name string) gotypes.Object {
	t.Helper()

	typeName, methodName, isMethod := strings.Cut(name, ".")
	for _, pkg := range ti.pkgs {
		if !isMethod {
			if obj := pkg.Scope().Lookup(name); obj != nil {
				return canonicalObject(obj)
			}
			continue
		}
		obj := pkg.Scope().Lookup(typeName)
		if obj == nil {
			continue
		}
		named, ok := obj.Type().(*gotypes.Named)
		if !ok {
			continue
		}
		for m := range named.Methods() {
			if m := m; m.Name() == methodName {
				return canonicalObject(m)
			}
		}
	}
	t.Fatalf("callee %q not found in any test package", name)
	return nil
}

const callsTestDep = `package dep

func F()            {}
func G[X any]()     {}
func H[X, Y any]()  {}
`

func TestPackageIndexCalls(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		dep    string
		callee string
		want   int
	}{
		{
			name: "direct call",
			src: `package p
func f()   {}
func use() { f() }`,
			callee: "f",
			want:   1,
		},
		{
			name: "method call",
			src: `package p
type T struct{}
func (T) f() {}
func use()   { var x T; x.f() }`,
			callee: "T.f",
			want:   1,
		},
		{
			name: "cross-package call",
			src: `package p
import "example.com/dep"
func use() { dep.F() }`,
			dep:    callsTestDep,
			callee: "F",
			want:   1,
		},
		{
			name: "generic call",
			src: `package p
func F[X any]() {}
func use()      { F[int]() }`,
			callee: "F",
			want:   1,
		},
		{
			// Go has no generic methods, so the Ident->Selector->Index->Call chain that
			// x.M[int]() would exercise is only reachable through a qualified identifier.
			name: "cross-package generic call",
			src: `package p
import "example.com/dep"
func use() { dep.G[int]() }`,
			dep:    callsTestDep,
			callee: "G",
			want:   1,
		},
		{
			// Two type arguments parse as IndexListExpr rather than IndexExpr.
			name: "cross-package generic call with two type args",
			src: `package p
import "example.com/dep"
func use() { dep.H[int, string]() }`,
			dep:    callsTestDep,
			callee: "H",
			want:   1,
		},
		{
			name: "passed as argument",
			src: `package p
func f()             {}
func g(fn func())    {}
func use()           { g(f) }`,
			callee: "f",
			want:   0,
		},
		{
			name: "assigned to var",
			src: `package p
func f() {}
var h = f`,
			callee: "f",
			want:   0,
		},
		{
			name: "used in composite literal",
			src: `package p
func f() {}
type S struct{ Fn func() }
var s = S{Fn: f}`,
			callee: "f",
			want:   0,
		},
		{
			name: "call inside go func literal",
			src: `package p
func f()   {}
func use() { go func() { f() }() }`,
			callee: "f",
			want:   1,
		},
		{
			name: "callee used as receiver",
			src: `package p
type Handler func()
func (h Handler) Method() {}
var f Handler
func use()                { f.Method() }`,
			callee: "f",
			want:   0,
		},
		{
			name: "receiver use alongside direct call",
			src: `package p
type Handler func()
func (h Handler) Method() {}
var f Handler
func use()                { f.Method(); f() }`,
			callee: "f",
			want:   1,
		},
		{
			name: "repeated calls",
			src: `package p
func f()   {}
func use() { f(); f(); g(f) }
func g(fn func()) {}`,
			callee: "f",
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := buildTestIndex(t, tt.src, tt.dep)
			callee := ti.lookup(t, tt.callee)

			var got int
			for cur := range ti.ix.Calls(callee) {
				if _, ok := cur.Node().(*ast.CallExpr); !ok {
					t.Errorf("Calls yielded %T, want *ast.CallExpr", cur.Node())
				}
				got++
			}
			if got != tt.want {
				t.Errorf("Calls(%s) yielded %d call sites, want %d", tt.callee, got, tt.want)
			}
		})
	}
}

// TestPackageIndexCallsEarlyExit pins the iterator's early-exit contract, since
// callers may stop at the first call site.
func TestPackageIndexCallsEarlyExit(t *testing.T) {
	ti := buildTestIndex(t, `package p
func f()   {}
func use() { f(); f(); f() }`, "")

	var got int
	for range ti.ix.Calls(ti.lookup(t, "f")) {
		got++
		break
	}
	if got != 1 {
		t.Errorf("early-exit yielded %d call sites, want 1", got)
	}
}
