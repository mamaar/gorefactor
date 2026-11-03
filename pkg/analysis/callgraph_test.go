package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// The callgraph tests need a real, type-checked, multi-package workspace —
// CallSitesOf/NonCallUsesOf are no-ops against an index with no object
// identity, so a test built on untyped sources would trivially "pass" every
// assertion without exercising anything. importerFunc is reused from
// symbols_test.go (package-scope collision if redeclared here).

const callgraphDepSrc = `package dep

func Seed() {}

func F[X any]() {}
`

const callgraphMainSrc = `package main

import "example.com/dep"

type Server struct{}

func (s *Server) M() {}

type Box[T any] struct{}

func (b Box[T]) Get() T {
	var zero T
	return zero
}

func Register(fn func()) {}

func direct() {
	dep.Seed()
}

func useMethod() {
	var srv Server
	srv.M()
}

func useGeneric() {
	dep.F[int]()
}

func useBox() {
	var b Box[int]
	b.Get()
}

func valueUses() {
	var h = dep.Seed
	_ = h
	Register(dep.Seed)
}

func closures() {
	go func() {
		dep.Seed()
	}()
	defer func() {
		dep.Seed()
	}()
}
`

const callgraphMainTestSrc = `package main

import "example.com/dep"

func testHelperCallsSeed() {
	dep.Seed()
}
`

// buildCallgraphWorkspace type-checks a two-package workspace (dep, main,
// plus an in-package main_test.go), wires it into a *types.Workspace exactly
// like a real parsed workspace (file.Package back-references set, Files vs.
// TestFiles split), and builds the ReferenceIndex via the normal
// SymbolResolver.BuildReferenceIndex path rather than a hand-rolled
// packageIndex, so the test exercises the same code production uses.
func buildCallgraphWorkspace(t *testing.T) (*types.Workspace, *ReferenceIndex) {
	t.Helper()

	fset := token.NewFileSet()

	depFile, err := parser.ParseFile(fset, "dep.go", callgraphDepSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse dep: %v", err)
	}

	depInfo := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	depConf := gotypes.Config{}

	depPkg, err := depConf.Check("example.com/dep", fset, []*ast.File{depFile}, depInfo)
	if err != nil {
		t.Fatalf("type-check dep: %v", err)
	}

	mainFile, err := parser.ParseFile(fset, "main.go", callgraphMainSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse main: %v", err)
	}

	mainTestFile, err := parser.ParseFile(fset, "main_test.go", callgraphMainTestSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse main_test: %v", err)
	}

	mainInfo := &gotypes.Info{
		Defs: make(map[*ast.Ident]gotypes.Object),
		Uses: make(map[*ast.Ident]gotypes.Object),
	}
	mainConf := gotypes.Config{
		Importer: importerFunc(func(path string) (*gotypes.Package, error) {
			if path == "example.com/dep" {
				return depPkg, nil
			}

			return nil, fmt.Errorf("unknown import %q", path)
		}),
	}
	// Check main.go and main_test.go together, mirroring TypeCheckPackage's
	// merging of in-package test files into a single Check call so that
	// identifiers in the test file land in Defs/Uses too.
	mainPkg, err := mainConf.Check("example.com/main", fset, []*ast.File{mainFile, mainTestFile}, mainInfo)
	if err != nil {
		t.Fatalf("type-check main: %v", err)
	}

	depTypesFile := &types.File{Path: "dep.go", AST: depFile, OriginalContent: []byte(callgraphDepSrc)}
	depTypesPkg := &types.Package{
		Name:      "dep",
		Path:      "example.com/dep",
		Files:     map[string]*types.File{"dep.go": depTypesFile},
		TestFiles: map[string]*types.File{},
		TypesInfo: depInfo,
		TypesPkg:  depPkg,
	}
	depTypesFile.Package = depTypesPkg

	mainTypesFile := &types.File{Path: "main.go", AST: mainFile, OriginalContent: []byte(callgraphMainSrc)}
	mainTestTypesFile := &types.File{Path: "main_test.go", AST: mainTestFile, OriginalContent: []byte(callgraphMainTestSrc)}
	mainTypesPkg := &types.Package{
		Name:      "main",
		Path:      "example.com/main",
		Files:     map[string]*types.File{"main.go": mainTypesFile},
		TestFiles: map[string]*types.File{"main_test.go": mainTestTypesFile},
		TypesInfo: mainInfo,
		TypesPkg:  mainPkg,
	}
	mainTypesFile.Package = mainTypesPkg
	mainTestTypesFile.Package = mainTypesPkg

	ws := &types.Workspace{
		Packages: map[string]*types.Package{
			"example.com/dep":  depTypesPkg,
			"example.com/main": mainTypesPkg,
		},
		FileSet: fset,
		ImportToPath: map[string]string{
			"example.com/dep":  "example.com/dep",
			"example.com/main": "example.com/main",
		},
	}

	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	return ws, idx
}

// lookupPkgObj resolves a callee by name within pkg's scope, canonicalized.
// "T.M" names the method M on package-level type T; a bare name is looked up
// directly in package scope.
func lookupPkgObj(t *testing.T, pkg *gotypes.Package, name string) gotypes.Object {
	t.Helper()

	typeName, methodName, isMethod := strings.Cut(name, ".")
	if !isMethod {
		obj := pkg.Scope().Lookup(name)
		if obj == nil {
			t.Fatalf("%q not found in package %s", name, pkg.Path())
		}

		return canonicalObject(obj)
	}

	obj := pkg.Scope().Lookup(typeName)
	if obj == nil {
		t.Fatalf("type %q not found in package %s", typeName, pkg.Path())
	}

	named, ok := obj.Type().(*gotypes.Named)
	if !ok {
		t.Fatalf("%q is not a named type", typeName)
	}

	for m := range named.Methods() {
		if m.Name() == methodName {
			return canonicalObject(m)
		}
	}

	t.Fatalf("method %q not found on %q", methodName, typeName)

	return nil
}

// TestCallSitesOf_DirectAndMethodCalls covers the two simplest cases: a
// direct function call and a method call, plus the cross-package part (Seed
// is declared in dep, called from main) that is the whole point of a
// workspace-wide lookup.
func TestCallSitesOf_DirectAndMethodCalls(t *testing.T) {
	ws, idx := buildCallgraphWorkspace(t)
	if !idx.HasTypeInfo() {
		t.Fatal("expected HasTypeInfo() true for a type-checked workspace")
	}

	depPkg := ws.Packages["example.com/dep"].TypesPkg
	mainPkg := ws.Packages["example.com/main"].TypesPkg
	mainTypesPkg := ws.Packages["example.com/main"]

	seed := lookupPkgObj(t, depPkg, "Seed")
	seedSites := idx.CallSitesOf(seed)
	// direct(), closures() go, closures() defer, and the test file = 4.
	if len(seedSites) != 4 {
		t.Fatalf("CallSitesOf(Seed) = %d sites, want 4", len(seedSites))
	}

	m := lookupPkgObj(t, mainPkg, "Server.M")

	methodSites := idx.CallSitesOf(m)
	if len(methodSites) != 1 {
		t.Fatalf("CallSitesOf(Server.M) = %d sites, want 1", len(methodSites))
	}

	site := methodSites[0]
	if site.Fn == nil || site.Fn.Name.Name != "useMethod" {
		t.Errorf("method call site Fn = %v, want useMethod", site.Fn)
	}

	if site.Lit != nil {
		t.Errorf("method call site Lit = %v, want nil (not inside a closure)", site.Lit)
	}

	if site.Pkg != mainTypesPkg {
		t.Errorf("method call site Pkg = %v, want the main package", site.Pkg)
	}
}

// TestCallSitesOf_ClosuresAndTestFile covers the go/defer closure cases and
// the _test.go case: a call inside go func(){...}() must report Lit != nil,
// InGo == true, and Fn as the *enclosing* FuncDecl (not nil, even though the
// call itself sits inside the FuncLit); a call inside defer func(){...}()
// must report InDefer == true; and a call site inside a _test.go file must
// be found at all.
func TestCallSitesOf_ClosuresAndTestFile(t *testing.T) {
	ws, idx := buildCallgraphWorkspace(t)
	depPkg := ws.Packages["example.com/dep"].TypesPkg
	seed := lookupPkgObj(t, depPkg, "Seed")

	sites := idx.CallSitesOf(seed)

	var direct, goSites, deferSites, testFileSites []CallSite

	for _, s := range sites {
		switch {
		case s.File.Path == "main_test.go":
			testFileSites = append(testFileSites, s)
		case s.Lit != nil && s.InGo:
			goSites = append(goSites, s)
		case s.Lit != nil && s.InDefer:
			deferSites = append(deferSites, s)
		case s.Fn != nil && s.Fn.Name.Name == "direct":
			direct = append(direct, s)
		}
	}

	t.Run("direct call", func(t *testing.T) {
		if len(direct) == 0 {
			t.Error("did not find the direct call site in direct()")
		}

		for _, s := range direct {
			if s.Lit != nil {
				t.Errorf("direct-call site Lit = %v, want nil", s.Lit)
			}
		}
	})

	t.Run("go closure", func(t *testing.T) {
		if len(goSites) == 0 {
			t.Error("did not find the go func(){...}() call site")
		}

		for _, s := range goSites {
			if s.Fn == nil || s.Fn.Name.Name != "closures" {
				t.Errorf("go-closure site Fn = %v, want closures", s.Fn)
			}

			if s.InDefer {
				t.Errorf("go-closure site has InDefer == true, want false")
			}
		}
	})

	t.Run("defer closure", func(t *testing.T) {
		if len(deferSites) == 0 {
			t.Error("did not find the defer func(){...}() call site")
		}

		for _, s := range deferSites {
			if s.Fn == nil || s.Fn.Name.Name != "closures" {
				t.Errorf("defer-closure site Fn = %v, want closures", s.Fn)
			}

			if s.InGo {
				t.Errorf("defer-closure site has InGo == true, want false")
			}
		}
	})

	t.Run("test file", func(t *testing.T) {
		if len(testFileSites) == 0 {
			t.Error("did not find the call site in main_test.go")
		}

		for _, s := range testFileSites {
			if s.Fn == nil || s.Fn.Name.Name != "testHelperCallsSeed" {
				t.Errorf("test-file site Fn = %v, want testHelperCallsSeed", s.Fn)
			}

			if s.Lit != nil {
				t.Errorf("test-file site Lit = %v, want nil", s.Lit)
			}
		}
	})
}

// TestNonCallUsesOf covers method values, func values, and a symbol passed
// as an argument — references a signature change cannot rewrite by editing
// call sites — and confirms real call sites are excluded.
func TestNonCallUsesOf(t *testing.T) {
	ws, idx := buildCallgraphWorkspace(t)
	depPkg := ws.Packages["example.com/dep"].TypesPkg
	seed := lookupPkgObj(t, depPkg, "Seed")

	uses := idx.NonCallUsesOf(seed)
	if len(uses) != 2 {
		t.Fatalf("NonCallUsesOf(Seed) = %d, want 2 (var h = Seed, Register(Seed))", len(uses))
	}

	for _, u := range uses {
		if u.File.Path != "main.go" {
			t.Errorf("unexpected file for non-call use: %s", u.File.Path)
		}

		if u.Ident == nil || u.Ident.Name != "Seed" {
			t.Errorf("unexpected ident for non-call use: %v", u.Ident)
		}
	}
}

// TestCallSitesAndNonCallUses_Disjoint checks the trap called out in the
// brief directly: CallSitesOf and NonCallUsesOf must never disagree about
// which set a use belongs to, and together (plus the definition, which
// appears in neither) they must cover every raw go/types Use of the object.
func TestCallSitesAndNonCallUses_Disjoint(t *testing.T) {
	ws, idx := buildCallgraphWorkspace(t)
	depPkg := ws.Packages["example.com/dep"].TypesPkg
	seed := lookupPkgObj(t, depPkg, "Seed")

	calls := idx.CallSitesOf(seed)
	nonCalls := idx.NonCallUsesOf(seed)

	seen := make(map[token.Pos]bool)

	for _, s := range calls {
		pos := s.Call.Pos()
		if seen[pos] {
			t.Errorf("position %v reported twice within CallSitesOf", pos)
		}

		seen[pos] = true
	}

	for _, u := range nonCalls {
		if seen[u.Pos] {
			t.Errorf("NonCallUsesOf position %v overlaps a CallSitesOf site", u.Pos)
		}

		seen[u.Pos] = true
	}

	// Every raw go/types Use of Seed across the workspace must land in
	// exactly one of the two sets above.
	var totalUses int

	for _, pkg := range ws.Packages {
		if pkg.TypesInfo == nil {
			continue
		}

		for _, obj := range pkg.TypesInfo.Uses {
			if obj == seed {
				totalUses++
			}
		}
	}

	if totalUses != len(seen) {
		t.Fatalf("raw Uses(Seed) = %d, but CallSitesOf+NonCallUsesOf cover %d distinct positions", totalUses, len(seen))
	}
}

// TestCallSitesOf_GenericInstantiation exercises the case canonicalObject
// exists for: a call through an instantiated generic type's method
// (Box[int].Get()) produces a distinct *types.Func object in the raw
// go/types Uses map (Origin() points back to the generic declaration).
// CallSitesOf must canonicalize on the way in so passing either the raw
// instantiated object or the origin finds the same call site, and the
// cross-package plain-generic-function case (dep.F[int]()) must be found
// too.
func TestCallSitesOf_GenericInstantiation(t *testing.T) {
	ws, idx := buildCallgraphWorkspace(t)
	depPkg := ws.Packages["example.com/dep"].TypesPkg
	mainPkg := ws.Packages["example.com/main"]

	// Cross-package plain generic function: dep.F[int]().
	fOrigin := lookupPkgObj(t, depPkg, "F")

	fSites := idx.CallSitesOf(fOrigin)
	if len(fSites) != 1 {
		t.Fatalf("CallSitesOf(F) = %d, want 1", len(fSites))
	}

	// Find the raw (non-canonicalized) Uses entry for the Get identifier in
	// `b.Get()` inside useBox — this is the actual instantiated method
	// object the type checker produced, not yet passed through
	// canonicalObject.
	var rawGetObj gotypes.Object

	ast.Inspect(mainPkg.Files["main.go"].AST, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "useBox" {
			return true
		}

		ast.Inspect(fn, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Get" {
				return true
			}

			rawGetObj = mainPkg.TypesInfo.Uses[sel.Sel]

			return false
		})

		return false
	})

	if rawGetObj == nil {
		t.Fatal("could not find raw Uses entry for Box[int].Get() call")
	}

	getOrigin := lookupPkgObj(t, mainPkg.TypesPkg, "Box.Get")
	if rawGetObj == getOrigin {
		t.Fatal("expected the instantiated Get object to differ from the generic origin — test setup did not exercise instantiation")
	}

	rawSites := idx.CallSitesOf(rawGetObj)

	originSites := idx.CallSitesOf(getOrigin)
	if len(rawSites) != 1 || len(originSites) != 1 {
		t.Fatalf("CallSitesOf(instantiated Get) = %d, CallSitesOf(origin Get) = %d, want 1 and 1", len(rawSites), len(originSites))
	}

	if rawSites[0].Call.Pos() != originSites[0].Call.Pos() {
		t.Fatalf("instantiated and origin Get lookups found different call sites")
	}
}

// TestHasTypeInfo pins the documented contract: an index built from a
// workspace whose packages have never been type-checked must report false
// (an empty result set from CallSitesOf would otherwise look like a
// legitimate zero-use answer instead of "there is no data to search"), and
// true once the package is type-checked.
func TestHasTypeInfo(t *testing.T) {
	fset := token.NewFileSet()
	src := `package untyped

func Foo() {}
func Bar() { Foo() }
`

	astFile, err := parser.ParseFile(fset, "untyped.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	file := &types.File{Path: "untyped.go", AST: astFile, OriginalContent: []byte(src)}
	pkg := &types.Package{
		Name:  "untyped",
		Path:  "test/untyped",
		Files: map[string]*types.File{"untyped.go": file},
	}
	file.Package = pkg

	ws := &types.Workspace{
		Packages:     map[string]*types.Package{"test/untyped": pkg},
		FileSet:      fset,
		ImportToPath: make(map[string]string),
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := NewSymbolResolver(ws, logger)

	idx := resolver.BuildReferenceIndex()
	if idx.HasTypeInfo() {
		t.Fatal("expected HasTypeInfo() == false before type-checking (LoadWorkspace does not type-check)")
	}

	p := NewParser(logger)
	p.importer = &workspaceImporter{ws: ws, fset: ws.FileSet, parser: p}
	p.EnsureTypeChecked(ws, pkg)

	if pkg.TypesPkg == nil {
		t.Fatal("expected package to type-check successfully")
	}

	idx2 := resolver.BuildReferenceIndex()
	if !idx2.HasTypeInfo() {
		t.Fatal("expected HasTypeInfo() == true after EnsureTypeChecked")
	}
}
