package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// valueUnit is the declaration that moves as one when a constant or variable
// is requested: its own spec, or the whole group when the group's specs depend
// on each other (iota, implicit repetition).
type valueUnit struct {
	decl *ast.GenDecl
	spec *ast.ValueSpec
	// whole is set when the unit is the whole parenthesised group.
	whole bool
}

func (u *valueUnit) node() ast.Node {
	if u.whole {
		return u.decl
	}
	return u.spec
}

// names returns the non-blank names the unit declares.
func (u *valueUnit) names() []*ast.Ident {
	var names []*ast.Ident
	specs := []ast.Spec{u.spec}
	if u.whole {
		specs = u.decl.Specs
	}
	for _, spec := range specs {
		for _, id := range spec.(*ast.ValueSpec).Names {
			if id.Name != "_" {
				names = append(names, id)
			}
		}
	}
	return names
}

// findValueUnit returns the unit declaring the constant or variable named at
// pos, or nil when file declares none there.
func findValueUnit(file *types.File, pos token.Pos) *valueUnit {
	if file.AST == nil {
		return nil
	}
	for _, decl := range file.AST.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok || d.Tok != token.CONST && d.Tok != token.VAR {
			continue
		}
		for _, spec := range d.Specs {
			vs := spec.(*ast.ValueSpec)
			for _, id := range vs.Names {
				if id.Pos() != pos {
					continue
				}
				whole := d.Lparen.IsValid() && len(d.Specs) > 1 && !selfContained(d)
				return &valueUnit{decl: d, spec: vs, whole: whole}
			}
		}
	}
	return nil
}

// selfContained reports whether every spec of d stands alone: explicit values
// that do not use iota, or a variable with a type and no value.
func selfContained(d *ast.GenDecl) bool {
	for _, spec := range d.Specs {
		vs := spec.(*ast.ValueSpec)
		if len(vs.Values) == 0 && (d.Tok == token.CONST || vs.Type == nil) || usesIota(vs) {
			return false
		}
	}
	return true
}

func usesIota(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
			found = true
		}
		return !found
	})
	return found
}

// groupValueUnits makes the entries of one unit share it: the first in
// declaration order carries the removal and the text, the others point to it.
// A unit with a name that is not requested is refused.
func groupValueUnits(moving []*movingSymbol) error {
	type group struct {
		unit    *valueUnit
		members []*movingSymbol
	}
	var groups []*group
	byNode := make(map[ast.Node]*group)
	for _, m := range moving {
		if m.typ != nil || m.sym.Kind != types.ConstantSymbol && m.sym.Kind != types.VariableSymbol || m.file == nil {
			continue
		}
		u := findValueUnit(m.file, m.sym.Position)
		if u == nil {
			continue
		}
		g := byNode[u.node()]
		if g == nil {
			g = &group{unit: u}
			byNode[u.node()] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, m)
	}
	for _, g := range groups {
		requested := make(map[token.Pos]bool, len(g.members))
		carrier := g.members[0]
		for _, m := range g.members {
			requested[m.sym.Position] = true
			if m.sym.Position < carrier.sym.Position {
				carrier = m
			}
		}
		var missing []string
		for _, id := range g.unit.names() {
			if !requested[id.Pos()] {
				missing = append(missing, id.Name)
			}
		}
		if len(missing) > 0 {
			first := g.members[0]
			return &groupMemberError{index: first.owner, err: &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: unitRefusal(first, g.unit, missing),
			}}
		}
		for _, m := range g.members {
			if m != carrier {
				m.sharedWith = carrier
			}
		}
	}
	return nil
}

// unitRefusal explains why name cannot move without the missing names.
func unitRefusal(m *movingSymbol, u *valueUnit, missing []string) string {
	with := strings.Join(missing, ", ")
	if u.whole {
		kind := "an iota group"
		if !usesIota(u.decl) {
			kind = "a group with implicit repetition"
		}
		return fmt.Sprintf("cannot move %s: it is declared in %s with %s; move them together", m.sym.Name, kind, with)
	}
	return fmt.Sprintf("cannot move %s: it is declared together with %s (%s); move them together", m.sym.Name, with, specHeader(m.file, u))
}

// specHeader is the first line of the unit's spec, with its keyword.
func specHeader(file *types.File, u *valueUnit) string {
	start := int(u.spec.Pos() - file.AST.FileStart)
	end := int(u.spec.End() - file.AST.FileStart)
	text, _, multiline := strings.Cut(string(file.OriginalContent[start:end]), "\n")
	if multiline {
		text += " ..."
	}
	return u.decl.Tok.String() + " " + text
}
