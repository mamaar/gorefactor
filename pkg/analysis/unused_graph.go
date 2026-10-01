package analysis

import (
	"cmp"
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// MaxReferrers caps UnusedSymbol.Referrers.
const MaxReferrers = 5

// usageNode is one top-level declaration name.
type usageNode struct {
	symbol *types.Symbol // nil when the symbol table has none (a second init, _)
	name   string
	test   bool // declared in a _test.go file

	recv    *usageNode   // receiver type of a method; nil otherwise or when unresolved
	methods []*usageNode // on a type node

	out []*usageNode // declarations this one references (counted edges only)

	// Inbound references, excluding the node itself. testRefs counts the
	// uncounted ones from a test declaration to a non-test one.
	prodRefs, testRefs int
	referrers          []*usageNode // first few inbound referrers
}

// declSpan is the extent of one top-level declaration or spec.
type declSpan struct {
	pos, end token.Pos
	owners   []*usageNode
}

type usageGraph struct {
	nodes map[token.Pos]*usageNode // by name identifier position
	spans []declSpan               // sorted by pos, whole workspace
}

// receiverKey names a type within one package and one package clause, so an
// external test package's types stay apart from the package's own.
type receiverKey struct {
	pkg     *types.Package
	clause  string
	typName string
}

// newUsageGraph creates a node for every top-level declaration name of ws and
// the spans that locate them. Symbol tables must already be built.
func newUsageGraph(ws *types.Workspace) *usageGraph {
	g := &usageGraph{nodes: make(map[token.Pos]*usageNode)}
	typeNodes := make(map[receiverKey]*usageNode)
	var methodDecls []methodDecl

	for _, pkg := range ws.Packages {
		symbols := symbolsByPosition(pkg)
		for _, file := range indexedFiles(pkg) {
			b := fileNodes{g: g, pkg: pkg, file: file, symbols: symbols, typeNodes: typeNodes}
			methodDecls = append(methodDecls, b.add()...)
		}
	}

	for _, m := range methodDecls {
		linkMethod(m, typeNodes)
	}

	slices.SortFunc(g.spans, compareSpans)
	return g
}

// methodDecl is a method node awaiting its receiver type.
type methodDecl struct {
	node *usageNode
	key  receiverKey
}

func linkMethod(m methodDecl, typeNodes map[receiverKey]*usageNode) {
	typ := typeNodes[m.key]
	if typ == nil {
		return
	}
	m.node.recv = typ
	typ.methods = append(typ.methods, m.node)
}

// fileNodes adds the declarations of one file to a graph.
type fileNodes struct {
	g         *usageGraph
	pkg       *types.Package
	file      *types.File
	symbols   map[token.Pos]*types.Symbol
	typeNodes map[receiverKey]*usageNode
}

// add records the file's declarations and returns its methods.
func (b fileNodes) add() []methodDecl {
	var methods []methodDecl
	clause := b.file.AST.Name.Name

	for _, decl := range b.file.AST.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			n := b.newNode(d.Name)
			b.addSpan(d.Pos(), d.End(), n)
			if d.Recv != nil {
				methods = append(methods, methodDecl{node: n, key: receiverKey{b.pkg, clause, receiverTypeName(d.Recv)}})
			}
		case *ast.GenDecl:
			b.addGenDecl(d, clause)
		}
	}
	return methods
}

func (b fileNodes) addGenDecl(d *ast.GenDecl, clause string) {
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			n := b.newNode(s.Name)
			b.typeNodes[receiverKey{b.pkg, clause, s.Name.Name}] = n
			b.addSpan(s.Pos(), s.End(), n)
		case *ast.ValueSpec:
			owners := make([]*usageNode, 0, len(s.Names))
			for _, name := range s.Names {
				owners = append(owners, b.newNode(name))
			}
			b.addSpan(s.Pos(), s.End(), owners...)
		}
	}
}

func (b fileNodes) newNode(name *ast.Ident) *usageNode {
	n := &usageNode{
		symbol: b.symbols[name.Pos()],
		name:   name.Name,
		test:   strings.HasSuffix(b.file.Path, "_test.go"),
	}
	b.g.nodes[name.Pos()] = n
	return n
}

func (b fileNodes) addSpan(pos, end token.Pos, owners ...*usageNode) {
	b.g.spans = append(b.g.spans, declSpan{pos: pos, end: end, owners: owners})
}

// symbolsByPosition indexes every symbol of pkg's table by declaration position.
func symbolsByPosition(pkg *types.Package) map[token.Pos]*types.Symbol {
	byPos := make(map[token.Pos]*types.Symbol)
	if pkg.Symbols == nil {
		return byPos
	}
	for _, m := range []map[string]*types.Symbol{pkg.Symbols.Functions, pkg.Symbols.Types, pkg.Symbols.Variables, pkg.Symbols.Constants} {
		for _, s := range m {
			byPos[s.Position] = s
		}
	}
	for _, methods := range pkg.Symbols.Methods {
		for _, s := range methods {
			byPos[s.Position] = s
		}
	}
	return byPos
}

// receiverTypeName returns the base type name of a method receiver, without
// pointer or type parameters.
func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	expr := recv.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e.Name
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		default:
			return ""
		}
	}
}

func compareSpans(a, b declSpan) int { return cmp.Compare(a.pos, b.pos) }

func compareSpanToPos(s declSpan, pos token.Pos) int { return cmp.Compare(s.pos, pos) }

// enclosing returns the nodes of the top-level declaration containing pos, or
// nil when pos is outside every declaration.
func (g *usageGraph) enclosing(pos token.Pos) []*usageNode {
	i, found := slices.BinarySearchFunc(g.spans, pos, compareSpanToPos)
	if !found {
		i--
	}
	if i < 0 || pos >= g.spans[i].end {
		return nil
	}
	return g.spans[i].owners
}

// buildUsageGraph returns the usage graph of ws with its reference edges. Symbol
// tables must be built; idx must index the same workspace.
func buildUsageGraph(ws *types.Workspace, resolver *SymbolResolver, idx *ReferenceIndex) *usageGraph {
	g := newUsageGraph(ws)
	typed := g.addTypedEdges(idx.workspaceIdx)
	g.addNameEdges(resolver, idx, typed)
	return g
}

// addTypedEdges adds the edges of every type-checked package and returns the
// positions that package defines.
func (g *usageGraph) addTypedEdges(wi *workspaceIndex) map[token.Pos]bool {
	defined := make(map[token.Pos]bool)
	if wi == nil {
		return defined
	}

	for _, pix := range wi.packages {
		for id := range pix.def {
			defined[id.pos] = true
		}
		for id, u := range pix.uses {
			target := g.nodes[id.pos]
			if target == nil {
				continue
			}
			g.addUsesOf(target, pix, u)
		}
	}
	return defined
}

// addUsesOf adds an edge from the declaration enclosing each use of target.
// The name check guards against a position that belongs to another file set.
func (g *usageGraph) addUsesOf(target *usageNode, pix *packageIndex, u *uses) {
	for i := range u.decode() {
		ident, ok := pix.inspect.At(i).Node().(*ast.Ident)
		if !ok || ident.Name != target.name {
			continue
		}
		g.addUseAt(ident.Pos(), target)
	}
}

// addNameEdges adds the edges to every node that no type-checked package
// defines, matching the name index.
func (g *usageGraph) addNameEdges(resolver *SymbolResolver, idx *ReferenceIndex, defined map[token.Pos]bool) {
	for pos, target := range g.nodes {
		if defined[pos] || target.symbol == nil {
			continue
		}
		entries, _ := idx.NameEntries(target.name)
		for i := range entries {
			if resolver.isNonDeclarationReference(&entries[i], target.symbol, nil) {
				g.addUseAt(entries[i].Pos, target)
			}
		}
	}
}

// addUseAt records a use of target at pos by the declaration enclosing pos.
func (g *usageGraph) addUseAt(pos token.Pos, target *usageNode) {
	for _, referrer := range g.enclosing(pos) {
		addUse(referrer, target)
	}
}

// addUse records that referrer uses target. A declaration's own references
// and a method's references to its receiver type are not uses; a test
// declaration's references to non-test ones are counted apart and give no edge.
func addUse(referrer, target *usageNode) {
	if referrer == target || referrer.recv == target {
		return
	}

	if len(target.referrers) < MaxReferrers && !slices.Contains(target.referrers, referrer) {
		target.referrers = append(target.referrers, referrer)
	}

	if referrer.test && !target.test {
		target.testRefs++
		return
	}

	target.prodRefs++
	if n := len(referrer.out); n == 0 || referrer.out[n-1] != target {
		referrer.out = append(referrer.out, target)
	}
}
