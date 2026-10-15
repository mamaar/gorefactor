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
	pkg    *types.Package
	name   string
	test   bool // declared in a _test.go file

	aliasOf string       // for a type alias, the name of the type it denotes
	recv    *usageNode   // receiver type of a method; nil otherwise or when unresolved
	methods []*usageNode // on a type node

	out     []*usageNode // declarations this one references (counted edges only)
	testOut []*usageNode // non-test declarations a test declaration references

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
	nodes     map[token.Pos]*usageNode // by name identifier position
	spans     []declSpan               // sorted by pos, whole workspace
	typeNodes map[receiverKey]*usageNode
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
	typeNodes := make(map[receiverKey]*usageNode)
	g := &usageGraph{nodes: make(map[token.Pos]*usageNode), typeNodes: typeNodes}
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

// linkMethod attaches m to its receiver type, following aliases of the same
// package to the type they denote.
func linkMethod(m methodDecl, typeNodes map[receiverKey]*usageNode) {
	typ := typeNodes[m.key]
	for hops := 0; typ != nil && typ.aliasOf != "" && hops < len(typeNodes); hops++ {
		typ = typeNodes[receiverKey{m.key.pkg, m.key.clause, typ.aliasOf}]
	}
	if typ == nil || typ.aliasOf != "" {
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
			if id, ok := s.Type.(*ast.Ident); ok && s.Assign.IsValid() {
				n.aliasOf = id.Name
			}
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
		pkg:    b.pkg,
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
	g.addEmbeddedEdges(ws, resolver, idx.workspaceIdx)
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

// addEmbeddedEdges adds the edges from a struct or interface type to the types
// it embeds. The package index skips them: an embedded field name is also a
// definition.
func (g *usageGraph) addEmbeddedEdges(ws *types.Workspace, resolver *SymbolResolver, wi *workspaceIndex) {
	typed := g.addTypedEmbeddedEdges(wi)

	for _, pkg := range ws.Packages {
		for _, file := range indexedFiles(pkg) {
			c := &embedCollector{}
			ast.Walk(c, file.AST)
			for _, ref := range c.refs {
				if !typed[ref.name] {
					g.addEmbeddedByName(resolver, pkg, file, ref)
				}
			}
		}
	}
}

// addTypedEmbeddedEdges resolves the embedded types of every type-checked
// package through go/types and returns the identifiers it resolved.
func (g *usageGraph) addTypedEmbeddedEdges(wi *workspaceIndex) map[*ast.Ident]bool {
	resolved := make(map[*ast.Ident]bool)
	if wi == nil {
		return resolved
	}

	for _, pix := range wi.packages {
		for cur := range pix.inspect.Root().Preorder((*ast.StructType)(nil), (*ast.InterfaceType)(nil)) {
			for _, ref := range embeddedRefs(cur.Node()) {
				obj := pix.info.Uses[ref.name]
				if obj == nil {
					continue
				}
				resolved[ref.name] = true

				target := g.nodes[canonicalObject(obj).Pos()]
				if target != nil && target.name == ref.name.Name {
					g.addUseAt(ref.name.Pos(), target)
				}
			}
		}
	}
	return resolved
}

// addEmbeddedByName matches an embedded type by name: within the package for
// T, and through the import alias for pkg.T.
func (g *usageGraph) addEmbeddedByName(resolver *SymbolResolver, pkg *types.Package, file *types.File, ref embeddedRef) {
	if ref.qual == nil {
		clause := file.AST.Name.Name
		if target := g.typeNodes[receiverKey{pkg, clause, ref.name.Name}]; target != nil {
			g.addUseAt(ref.name.Pos(), target)
		}
		return
	}

	for key, target := range g.typeNodes {
		if key.typName == ref.name.Name && target.symbol != nil &&
			resolver.importAliasRefersToPackage(ref.qual.Name, file, target.symbol.Package) {
			g.addUseAt(ref.name.Pos(), target)
		}
	}
}

// embeddedRef names an embedded type.
type embeddedRef struct {
	name *ast.Ident
	qual *ast.Ident // package qualifier of pkg.T; nil otherwise
}

// embedCollector gathers the embedded types of every struct and interface type
// in a tree.
type embedCollector struct{ refs []embeddedRef }

func (c *embedCollector) Visit(n ast.Node) ast.Visitor {
	c.refs = append(c.refs, embeddedRefs(n)...)
	return c
}

// embeddedRefs returns the types embedded in n if it is a struct or interface
// type, including the terms of an embedded constraint.
func embeddedRefs(n ast.Node) []embeddedRef {
	var fields *ast.FieldList
	switch t := n.(type) {
	case *ast.StructType:
		fields = t.Fields
	case *ast.InterfaceType:
		fields = t.Methods
	}
	if fields == nil {
		return nil
	}

	var refs []embeddedRef
	for _, f := range fields.List {
		if len(f.Names) == 0 {
			refs = appendTypeNames(refs, f.Type)
		}
	}
	return refs
}

func appendTypeNames(refs []embeddedRef, expr ast.Expr) []embeddedRef {
	switch e := expr.(type) {
	case *ast.Ident:
		return append(refs, embeddedRef{name: e})
	case *ast.SelectorExpr:
		if qual, ok := e.X.(*ast.Ident); ok {
			return append(refs, embeddedRef{name: e.Sel, qual: qual})
		}
	case *ast.StarExpr:
		return appendTypeNames(refs, e.X)
	case *ast.ParenExpr:
		return appendTypeNames(refs, e.X)
	case *ast.IndexExpr:
		return appendTypeNames(refs, e.X)
	case *ast.IndexListExpr:
		return appendTypeNames(refs, e.X)
	case *ast.UnaryExpr:
		return appendTypeNames(refs, e.X)
	case *ast.BinaryExpr:
		return appendTypeNames(appendTypeNames(refs, e.X), e.Y)
	}
	return refs
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
		if n := len(referrer.testOut); n == 0 || referrer.testOut[n-1] != target {
			referrer.testOut = append(referrer.testOut, target)
		}
		return
	}

	target.prodRefs++
	if n := len(referrer.out); n == 0 || referrer.out[n-1] != target {
		referrer.out = append(referrer.out, target)
	}
}
