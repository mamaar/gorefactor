package analysis

import (
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// parsedGraphWorkspace parses files (name -> source) into one untyped package
// "p", with test files under TestFiles, and builds its symbol table.
func parsedGraphWorkspace(t *testing.T, files map[string]string) *types.Workspace {
	t.Helper()
	fset := token.NewFileSet()
	pkg := &types.Package{
		Name:      "p",
		Path:      "test/p",
		Files:     map[string]*types.File{},
		TestFiles: map[string]*types.File{},
	}
	for name, src := range files {
		astFile, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		f := &types.File{Path: name, AST: astFile, OriginalContent: []byte(src), Package: pkg}
		if strings.HasSuffix(name, "_test.go") {
			pkg.TestFiles[name] = f
		} else {
			pkg.Files[name] = f
		}
	}
	ws := &types.Workspace{
		Packages: map[string]*types.Package{pkg.Path: pkg},
		FileSet:  fset,
	}
	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := resolver.BuildSymbolTable(pkg)
	if err != nil {
		t.Fatalf("symbol table: %v", err)
	}
	return ws
}

// posAfter returns the position of the first byte of needle in file.
func posAfter(t *testing.T, ws *types.Workspace, file, needle string) token.Pos {
	t.Helper()
	for _, pkg := range ws.Packages {
		for _, group := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			f := fileNamed(group, file)
			if f == nil {
				continue
			}
			off := strings.Index(string(f.OriginalContent), needle)
			if off < 0 {
				t.Fatalf("%q not found in %s", needle, file)
			}
			return ws.FileSet.File(f.AST.Pos()).Pos(off)
		}
	}
	t.Fatalf("no file %s", file)
	return token.NoPos
}

// fileNamed returns the file whose path is name or ends in "/"+name.
func fileNamed(group map[string]*types.File, name string) *types.File {
	for path, f := range group {
		if path == name || strings.HasSuffix(path, "/"+name) {
			return f
		}
	}
	return nil
}

func nodeNames(nodes []*usageNode) []string {
	var names []string
	for _, n := range nodes {
		names = append(names, n.name)
	}
	slices.Sort(names)
	return names
}

func assertEnclosing(t *testing.T, files map[string]string, file, needle string, want ...string) {
	t.Helper()
	ws := parsedGraphWorkspace(t, files)
	g := newUsageGraph(ws)
	got := nodeNames(g.enclosing(posAfter(t, ws, file, needle)))
	if !slices.Equal(got, want) {
		t.Errorf("enclosing(%q) = %v, want %v", needle, got, want)
	}
}

func TestUsageGraph_PositionInVarInitialiserBelongsToTheVar(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\nfunc f() int { return 1 }\n\nvar v = f()\n"},
		"a.go", "f()\n", "v")
}

func TestUsageGraph_PositionInMethodBodyBelongsToTheMethod(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype T struct{}\n\nfunc (t T) M() int {\n\treturn helper()\n}\n\nfunc helper() int { return 1 }\n"},
		"a.go", "helper()\n", "M")
}

func TestUsageGraph_PositionInMethodReceiverBelongsToTheMethod(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype T struct{}\n\nfunc (t T) M() {}\n"},
		"a.go", "T) M", "M")
}

func TestUsageGraph_PositionInStructFieldTypeBelongsToTheStructType(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\ntype S struct {\n\tf Dep\n}\n"},
		"a.go", "Dep\n}", "S")
}

func TestUsageGraph_PositionInInterfaceMethodSignatureBelongsToTheInterfaceType(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\ntype I interface {\n\tM(d Dep)\n}\n"},
		"a.go", "Dep)", "I")
}

func TestUsageGraph_PositionInParameterTypeBelongsToTheFunction(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\nfunc f(d Dep) {}\n"},
		"a.go", "Dep) {}", "f")
}

func TestUsageGraph_PositionInLocalTypeBelongsToTheEnclosingFunction(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\nfunc f() {\n\ttype local struct{ d Dep }\n\t_ = local{}\n}\n"},
		"a.go", "Dep }", "f")
}

func TestUsageGraph_PositionOutsideEveryDeclarationHasNoOwner(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\nimport \"fmt\"\n\nfunc f() { fmt.Println() }\n"},
		"a.go", "fmt\"")
}

func TestUsageGraph_MultiNameVarSpecIsOwnedByEveryName(t *testing.T) {
	assertEnclosing(t, map[string]string{"a.go": "package p\n\nfunc f() int { return 1 }\nfunc g() int { return 2 }\n\nvar a, b = f(), g()\n"},
		"a.go", "g()\n", "a", "b")
}

func TestUsageGraph_TestFileDeclarationIsMarkedTest(t *testing.T) {
	ws := parsedGraphWorkspace(t, map[string]string{
		"a.go":      "package p\n\nfunc prod() {}\n",
		"a_test.go": "package p\n\nfunc helper() {}\n",
	})
	g := newUsageGraph(ws)

	prod := g.nodes[posAfter(t, ws, "a.go", "prod")]
	helper := g.nodes[posAfter(t, ws, "a_test.go", "helper")]
	if prod == nil || prod.test {
		t.Errorf("prod = %+v, want a non-test node", prod)
	}
	if helper == nil || !helper.test {
		t.Errorf("helper = %+v, want a test node", helper)
	}
}

func TestUsageGraph_MethodReceiverPointsAtItsTypeNode(t *testing.T) {
	ws := parsedGraphWorkspace(t, map[string]string{
		"a.go": "package p\n\nfunc (t *T) M() {}\n\nfunc (g G[X]) N() {}\n\ntype T struct{}\n\ntype G[X any] struct{}\n",
	})
	g := newUsageGraph(ws)

	typ := g.nodes[posAfter(t, ws, "a.go", "T struct")]
	m := g.nodes[posAfter(t, ws, "a.go", "M()")]
	if m.recv != typ {
		t.Errorf("M.recv = %v, want the T node", m.recv)
	}
	if !slices.Contains(typ.methods, m) {
		t.Errorf("T.methods = %v, want M in it", nodeNames(typ.methods))
	}

	generic := g.nodes[posAfter(t, ws, "a.go", "G[X any]")]
	n := g.nodes[posAfter(t, ws, "a.go", "N()")]
	if n.recv != generic {
		t.Errorf("N.recv = %v, want the G node", n.recv)
	}
}

func TestUsageGraph_BlankAndRepeatedInitAreSeparateNodes(t *testing.T) {
	ws := parsedGraphWorkspace(t, map[string]string{
		"a.go": "package p\n\nfunc init() {}\n\nfunc init() {}\n\nvar _ = 1\n\nvar _ = 2\n",
	})
	g := newUsageGraph(ws)

	got := len(g.nodes)
	if got != 4 {
		t.Errorf("len(nodes) = %d, want 4", got)
	}
}

func TestUsageGraph_NodeCarriesTheSymbolAtItsPosition(t *testing.T) {
	ws := parsedGraphWorkspace(t, map[string]string{"a.go": "package p\n\nfunc f() {}\n\ntype T struct{}\n\nconst c = 1\n"})
	g := newUsageGraph(ws)

	for name, needle := range map[string]string{"f": "f() {}", "T": "T struct", "c": "c = 1"} {
		n := g.nodes[posAfter(t, ws, "a.go", needle)]
		if n == nil || n.symbol == nil || n.symbol.Name != name {
			t.Errorf("node %s = %+v, want it to carry symbol %s", name, n, name)
		}
	}
}

// graphFixture is a usage graph over a workspace, built either untyped or
// type-checked.
type graphFixture struct {
	ws *types.Workspace
	g  *usageGraph
}

func (f graphFixture) node(t *testing.T, file, needle string) *usageNode {
	t.Helper()
	n := f.g.nodes[posAfter(t, f.ws, file, needle)]
	if n == nil {
		t.Fatalf("no node at %q in %s", needle, file)
	}
	return n
}

// untypedGraph builds the graph of files (name -> source, one package) from
// parsed, unchecked sources.
func untypedGraph(t *testing.T, files map[string]string) graphFixture {
	t.Helper()
	ws := parsedGraphWorkspace(t, files)
	resolver := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return graphFixture{ws: ws, g: buildUsageGraph(ws, resolver, resolver.BuildReferenceIndex())}
}

// typedGraph builds the graph of files (path -> source, relative to a module
// "example.com/w") from a loaded and type-checked workspace, test files
// included.
func typedGraph(t *testing.T, files map[string]string) graphFixture {
	t.Helper()
	set, _ := loadSet(t, files)
	v := set.Primary()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
		if pkg.Symbols == nil {
			_, err := v.Resolver.BuildSymbolTable(pkg)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	idx, err := v.ReferenceIndex()
	if err != nil {
		t.Fatal(err)
	}
	return graphFixture{ws: v.Workspace, g: buildUsageGraph(v.Workspace, v.Resolver, idx)}
}

// inBothModes runs check on the untyped and the typed graph of a one-package
// workspace.
func inBothModes(t *testing.T, files map[string]string, check func(t *testing.T, f graphFixture)) {
	t.Helper()
	t.Run("untyped", func(t *testing.T) { check(t, untypedGraph(t, files)) })

	prefixed := make(map[string]string, len(files))
	for name, src := range files {
		prefixed["p/"+name] = src
	}
	t.Run("typed", func(t *testing.T) { check(t, typedGraph(t, prefixed)) })
}

func assertRefs(t *testing.T, n *usageNode, prod, test int) {
	t.Helper()
	if n.prodRefs != prod || n.testRefs != test {
		t.Errorf("%s: prodRefs=%d testRefs=%d, want %d and %d", n.name, n.prodRefs, n.testRefs, prod, test)
	}
}

func assertEdge(t *testing.T, from, to *usageNode, want bool) {
	t.Helper()
	got := slices.Contains(from.out, to)
	if got != want {
		t.Errorf("edge %s -> %s = %v, want %v", from.name, to.name, got, want)
	}
}

func TestUsageGraph_CallFromProductionFunctionIsACountedEdge(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\nfunc callee() {}\n\nfunc caller() { callee() }\n"},
		func(t *testing.T, f graphFixture) {
			callee, caller := f.node(t, "a.go", "callee()"), f.node(t, "a.go", "caller()")
			assertRefs(t, callee, 1, 0)
			assertEdge(t, caller, callee, true)
			if !slices.Equal(callee.referrers, []*usageNode{caller}) {
				t.Errorf("referrers = %v, want [caller]", nodeNames(callee.referrers))
			}
		})
}

func TestUsageGraph_CallFromTestFileToProductionIsATestRefWithoutEdge(t *testing.T) {
	inBothModes(t, map[string]string{
		"a.go":      "package p\n\nfunc callee() {}\n",
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { callee() }\n",
	}, func(t *testing.T, f graphFixture) {
		callee, test := f.node(t, "a.go", "callee()"), f.node(t, "a_test.go", "TestX")
		assertRefs(t, callee, 0, 1)
		assertEdge(t, test, callee, false)
		if !slices.Equal(callee.referrers, []*usageNode{test}) {
			t.Errorf("referrers = %v, want [TestX]", nodeNames(callee.referrers))
		}
	})
}

func TestUsageGraph_CallFromExternalTestPackageIsATestRef(t *testing.T) {
	f := typedGraph(t, map[string]string{
		"p/p.go":      "package p\n\nfunc Exported() {}\n",
		"p/p_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/w/p\"\n)\n\nfunc TestX(t *testing.T) { p.Exported() }\n",
	})

	exported := f.node(t, "p.go", "Exported")
	assertRefs(t, exported, 0, 1)
	assertEdge(t, f.node(t, "p_test.go", "TestX"), exported, false)
}

func TestUsageGraph_TestHelperCalledFromTestIsACountedEdge(t *testing.T) {
	inBothModes(t, map[string]string{
		"a.go":      "package p\n",
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc helper() {}\n\nfunc TestX(t *testing.T) { helper() }\n",
	}, func(t *testing.T, f graphFixture) {
		helper, test := f.node(t, "a_test.go", "helper()"), f.node(t, "a_test.go", "TestX")
		assertRefs(t, helper, 1, 0)
		assertEdge(t, test, helper, true)
	})
}

func TestUsageGraph_RecursionDoesNotCountAsAUse(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\nfunc rec(n int) {\n\tif n > 0 {\n\t\trec(n - 1)\n\t}\n}\n"},
		func(t *testing.T, f graphFixture) {
			rec := f.node(t, "a.go", "rec(n int)")
			assertRefs(t, rec, 0, 0)
			assertEdge(t, rec, rec, false)
		})
}

func TestUsageGraph_MethodDoesNotCountAsAUseOfItsReceiverType(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\ntype T struct{}\n\nfunc (t *T) M() *T { return &T{} }\n\nfunc (t T) N() { _ = T{} }\n"},
		func(t *testing.T, f graphFixture) {
			typ := f.node(t, "a.go", "T struct")
			assertRefs(t, typ, 0, 0)
			for _, m := range []string{"M()", "N()"} {
				assertEdge(t, f.node(t, "a.go", m), typ, false)
			}
		})
}

func TestUsageGraph_PackageVarInitialiserReferenceIsAnEdgeFromTheVar(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\nfunc callee() int { return 1 }\n\nvar v = callee()\n"},
		func(t *testing.T, f graphFixture) {
			callee, v := f.node(t, "a.go", "callee()"), f.node(t, "a.go", "v =")
			assertRefs(t, callee, 1, 0)
			assertEdge(t, v, callee, true)
		})
}

func TestUsageGraph_StructFieldTypeIsAnEdgeFromTheStructType(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\ntype S struct{ d Dep }\n"},
		func(t *testing.T, f graphFixture) {
			dep, s := f.node(t, "a.go", "Dep struct"), f.node(t, "a.go", "S struct")
			assertRefs(t, dep, 1, 0)
			assertEdge(t, s, dep, true)
		})
}

func TestUsageGraph_InterfaceMethodSignatureTypeIsAnEdgeFromTheInterfaceType(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\ntype I interface{ M(d Dep) }\n"},
		func(t *testing.T, f graphFixture) {
			dep, i := f.node(t, "a.go", "Dep struct"), f.node(t, "a.go", "I interface")
			assertRefs(t, dep, 1, 0)
			assertEdge(t, i, dep, true)
		})
}

func TestUsageGraph_ParameterTypeIsAnEdgeFromTheFunction(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\ntype Dep struct{}\n\nfunc f(d Dep) {}\n"},
		func(t *testing.T, f graphFixture) {
			dep, fn := f.node(t, "a.go", "Dep struct"), f.node(t, "a.go", "f(d")
			assertRefs(t, dep, 1, 0)
			assertEdge(t, fn, dep, true)
		})
}

func TestUsageGraph_MethodCallIsAnEdgeToTheMethod(t *testing.T) {
	inBothModes(t, map[string]string{"a.go": "package p\n\ntype T struct{}\n\nfunc (t T) M() {}\n\nfunc f(t T) { t.M() }\n"},
		func(t *testing.T, f graphFixture) {
			m, fn := f.node(t, "a.go", "M()"), f.node(t, "a.go", "f(t")
			assertRefs(t, m, 1, 0)
			assertEdge(t, fn, m, true)
		})
}

func TestUsageGraph_EmbeddingStructHasEdgeToEmbeddedType(t *testing.T) {
	set, _ := loadSet(t, map[string]string{
		"p/a.go": "package p\n\ntype base struct{}\n\ntype Impl struct{ base }\n",
	})
	v := set.Primary()
	resolver := NewSymbolResolver(v.Workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
		_, err := resolver.BuildSymbolTable(pkg)
		if err != nil {
			t.Fatal(err)
		}
	}
	idx, err := v.ReferenceIndex()
	if err != nil {
		t.Fatal(err)
	}
	g := buildUsageGraph(v.Workspace, resolver, idx)

	base := g.nodes[posAfter(t, v.Workspace, "a.go", "base struct")]
	impl := g.nodes[posAfter(t, v.Workspace, "a.go", "Impl struct")]
	if !slices.Contains(impl.out, base) {
		t.Errorf("Impl.out = %v, want base in it", nodeNames(impl.out))
	}
}
