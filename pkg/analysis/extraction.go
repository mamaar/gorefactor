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

// ExtractionDecl is one declaration of an ExtractionUnit and the lines it spans.
type ExtractionDecl struct {
	Site  DeclSite
	Lines int
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
// between them. idx must index ws.
func ExtractionUnits(ws *types.Workspace, resolver *SymbolResolver, idx *ReferenceIndex, pkg *types.Package) []ExtractionUnit {
	for _, p := range ws.Packages {
		if p.Symbols == nil {
			_ = resolver.BuildSymbolTable(p)
		}
	}

	g := buildUsageGraph(ws, resolver, idx)
	b := &extractionBuilder{g: g, fset: ws.FileSet, pkg: pkg, groupOf: make(map[*usageNode]*extractionGroup)}

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
	groupOf map[*usageNode]*extractionGroup
	methods []methodNode
}

type methodNode struct {
	node *usageNode
	pos  token.Pos
	decl ExtractionDecl
}

func (b *extractionBuilder) site(pos token.Pos) DeclSite {
	return DeclSite{File: b.fset.Position(pos).Filename, Pos: pos}
}

func (b *extractionBuilder) decl(pos, end token.Pos) ExtractionDecl {
	return ExtractionDecl{Site: b.site(pos), Lines: b.fset.Position(end).Line - b.fset.Position(pos).Line + 1}
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
				b.methods = append(b.methods, methodNode{n, d.Name.Pos(), b.decl(d.Pos(), d.End())})
				continue
			}

			grp := b.addGroup(d.Name.Pos())
			b.add(grp, n)
			grp.decls = append(grp.decls, b.decl(d.Pos(), d.End()))
			grp.skip = d.Name.Name == "init" || (d.Name.Name == "main" && b.pkg.Name == "main")
		case *ast.GenDecl:
			b.addGenDecl(d)
		}
	}
}

func (b *extractionBuilder) addGenDecl(d *ast.GenDecl) {
	var shared *extractionGroup

	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			n := b.g.nodes[s.Name.Pos()]
			grp := b.addGroup(s.Name.Pos())
			b.add(grp, n)
			grp.decls = append(grp.decls, b.decl(s.Pos(), s.End()))
		case *ast.ValueSpec:
			grp := shared
			if grp == nil {
				grp = b.addGroup(s.Names[0].Pos())
			}

			for _, name := range s.Names {
				b.add(grp, b.g.nodes[name.Pos()])
			}

			grp.decls = append(grp.decls, b.decl(s.Pos(), s.End()))

			if d.Tok == token.CONST && isIotaGroup(d) {
				shared = grp
			}
		}
	}
}

// isIotaGroup reports whether a const declaration repeats an implicit
// expression or uses iota, which ties its specs together.
func isIotaGroup(d *ast.GenDecl) bool {
	for _, spec := range d.Specs {
		s := spec.(*ast.ValueSpec)
		if len(s.Values) == 0 {
			return true
		}

		found := false

		for _, v := range s.Values {
			ast.Inspect(v, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				found = found || (ok && id.Name == "iota")

				return !found
			})
		}

		if found {
			return true
		}
	}

	return false
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
		grp.decls = append(grp.decls, m.decl)
	}
}

// member names a node the way move_symbol does: Type.method for a method.
func member(n *usageNode) string {
	if n.recv == nil {
		return n.name
	}

	return n.recv.name + "." + n.name
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
				if tg == nil || tg == grp {
					continue
				}

				ref := ExtractionRef{To: out[index[tg]].Site, Name: member(t), Exported: ast.IsExported(t.name)}
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
			if !slices.Contains(u.TestRefs, member(t)) {
				u.TestRefs = append(u.TestRefs, member(t))
			}
		}
	}

	return out
}
