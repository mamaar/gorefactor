package analysis

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// DeclSite identifies a declaration across views, which share the file set.
type DeclSite struct {
	File string
	Pos  token.Pos
}

// ExtractionDecl is one declaration of an ExtractionUnit: where it starts and
// ends and the lines it spans.
type ExtractionDecl struct {
	Site  DeclSite
	End   token.Pos
	Lines int
	Name  string // the name, or the first of a group; the method name for a method
	Type  string // the receiver type of a method; empty otherwise
}

// ExtractionRef is a reference from one unit to a member of another.
type ExtractionRef struct {
	To       DeclSite // the referenced unit
	Name     string   // the referenced member: a name, or Type.method
	Exported bool
}

// ExtractionUnit is what move_symbol must move in one piece: a type with its
// methods, the names of one declaration, or a const group using iota.
type ExtractionUnit struct {
	Site      DeclSite // the type, or the first name
	Line      int
	Names     []string // the symbols to pass to move_symbol; methods excluded
	Candidate bool     // false for init, main and units without a movable name
	Decls     []ExtractionDecl
	// Refs are the production references the unit's members make to other
	// units of the package.
	Refs []ExtractionRef
	// TestRefs names the unexported members test declarations reference.
	TestRefs []string
}

// extractionGroup is a unit being built.
type extractionGroup struct {
	pos   token.Pos
	nodes []*usageNode
	names []string
	decls []ExtractionDecl
	skip  bool
}

// ExtractionUnits returns the production units of pkg with the references
// between them. idx must index ws. grouped says whether the specs of a const
// or var declaration move as one.
func ExtractionUnits(ws *types.Workspace, resolver *SymbolResolver, idx *ReferenceIndex, pkg *types.Package, grouped func(*ast.GenDecl) bool) []ExtractionUnit {
	for _, p := range ws.Packages {
		if p.Symbols == nil {
			_ = resolver.BuildSymbolTable(p)
		}
	}

	g := buildUsageGraph(ws, resolver, idx)
	b := &extractionBuilder{g: g, fset: ws.FileSet, pkg: pkg, grouped: grouped, groupOf: make(map[*usageNode]*extractionGroup), recvName: make(map[*usageNode]string)}

	for _, file := range indexedFiles(pkg) {
		if !strings.HasSuffix(file.Path, "_test.go") {
			b.addFile(file.AST)
		}
	}

	b.attachMethods()

	return b.units()
}

type extractionBuilder struct {
	g       *usageGraph
	fset    *token.FileSet
	pkg     *types.Package
	groups  []*extractionGroup
	grouped func(*ast.GenDecl) bool
	groupOf map[*usageNode]*extractionGroup
	methods []methodNode
	// recvName is the type name a method's receiver spells, which may be an alias.
	recvName map[*usageNode]string
}

type methodNode struct {
	node *usageNode
	pos  token.Pos
	decl ast.Decl
}

func (b *extractionBuilder) site(pos token.Pos) DeclSite {
	return DeclSite{File: b.fset.Position(pos).Filename, Pos: pos}
}

func (b *extractionBuilder) decl(n ast.Node, name *usageNode) ExtractionDecl {
	return ExtractionDecl{
		Site:  b.site(n.Pos()),
		End:   n.End(),
		Lines: b.fset.Position(n.End()).Line - b.fset.Position(n.Pos()).Line + 1,
		Name:  name.name,
	}
}

func (b *extractionBuilder) addGroup(pos token.Pos) *extractionGroup {
	grp := &extractionGroup{pos: pos}
	b.groups = append(b.groups, grp)

	return grp
}

// add puts n in grp; a method is not a name to move.
func (b *extractionBuilder) add(grp *extractionGroup, n *usageNode) {
	grp.nodes = append(grp.nodes, n)
	b.groupOf[n] = grp

	if n.recv == nil && n.symbol != nil && n.name != "_" {
		grp.names = append(grp.names, n.name)
	}
}

func (b *extractionBuilder) addFile(file *ast.File) {
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			n := b.g.nodes[d.Name.Pos()]
			if d.Recv != nil {
				b.methods = append(b.methods, methodNode{n, d.Name.Pos(), d})
				b.recvName[n] = receiverTypeName(d.Recv)

				continue
			}

			grp := b.addGroup(d.Name.Pos())
			b.add(grp, n)
			grp.decls = append(grp.decls, b.decl(d, n))
			grp.skip = d.Name.Name == "init" || (d.Name.Name == "main" && b.pkg.Name == "main")
		case *ast.GenDecl:
			b.addGenDecl(d)
		}
	}
}

func (b *extractionBuilder) addGenDecl(d *ast.GenDecl) {
	var shared *extractionGroup

	grouped := (d.Tok == token.CONST || d.Tok == token.VAR) && b.grouped(d)

	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			n := b.g.nodes[s.Name.Pos()]
			grp := b.addGroup(s.Name.Pos())
			b.add(grp, n)
			grp.decls = append(grp.decls, b.decl(s, n))
		case *ast.ValueSpec:
			grp := shared
			if grp == nil {
				grp = b.addGroup(s.Names[0].Pos())
			}

			for _, name := range s.Names {
				b.add(grp, b.g.nodes[name.Pos()])
			}

			grp.decls = append(grp.decls, b.decl(s, b.g.nodes[s.Names[0].Pos()]))

			if shared == nil && grouped {
				shared = grp
			}
		}
	}
}

// attachMethods joins each method to its receiver type's group; one whose type
// is not in the package becomes a group nothing can move.
func (b *extractionBuilder) attachMethods() {
	for _, m := range b.methods {
		grp := b.groupOf[m.node.recv]
		if grp == nil {
			grp = b.addGroup(m.pos)
			grp.skip = true
		}

		b.add(grp, m.node)

		decl := b.decl(m.decl, m.node)
		if m.node.recv != nil {
			decl.Type = m.node.recv.name
		}

		grp.decls = append(grp.decls, decl)
	}
}

// isReceiverAlias reports whether t is the alias method n spells its receiver
// with: the method belongs to the aliased type, not to a dependency on the alias.
func (b *extractionBuilder) isReceiverAlias(n, t *usageNode) bool {
	return n.recv != nil && t.aliasOf != "" && t.name == b.recvName[n]
}

func (b *extractionBuilder) units() []ExtractionUnit {
	out := make([]ExtractionUnit, len(b.groups))
	index := make(map[*extractionGroup]int, len(b.groups))

	for i, grp := range b.groups {
		index[grp] = i
		out[i] = ExtractionUnit{Site: b.site(grp.pos), Line: b.fset.Position(grp.pos).Line, Decls: grp.decls}

		out[i].Names = slices.Sorted(slices.Values(grp.names))
		out[i].Candidate = !grp.skip && len(out[i].Names) > 0
	}

	for i, grp := range b.groups {
		for _, n := range grp.nodes {
			for _, t := range n.out {
				tg := b.groupOf[t]
				if tg == nil || tg == grp || b.isReceiverAlias(n, t) {
					continue
				}

				ref := ExtractionRef{To: out[index[tg]].Site, Name: declName(t), Exported: ast.IsExported(t.name)}
				if !slices.Contains(out[i].Refs, ref) {
					out[i].Refs = append(out[i].Refs, ref)
				}
			}
		}
	}

	for _, n := range b.g.nodes {
		if !n.test || n.pkg != b.pkg {
			continue
		}

		for _, t := range n.testOut {
			tg := b.groupOf[t]
			if tg == nil || ast.IsExported(t.name) {
				continue
			}

			u := &out[index[tg]]
			if !slices.Contains(u.TestRefs, declName(t)) {
				u.TestRefs = append(u.TestRefs, declName(t))
			}
		}
	}

	return out
}
