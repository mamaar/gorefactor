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
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		t.Fatalf("symbol table: %v", err)
	}
	return ws
}

// posAfter returns the position of the first byte of needle in file.
func posAfter(t *testing.T, ws *types.Workspace, file, needle string) token.Pos {
	t.Helper()
	for _, pkg := range ws.Packages {
		for _, group := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			f := group[file]
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

	if got := len(g.nodes); got != 4 {
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
