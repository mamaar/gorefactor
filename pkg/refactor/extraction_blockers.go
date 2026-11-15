package refactor

import (
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// collectBlockers records, for each candidate of local, the reasons move_symbol
// would refuse to move its set of units in the view ws belongs to. The set is
// the one this view alone sees; what only another configuration adds is
// reported through the configuration blockers.
func (r *extractionRun) collectBlockers(ws *types.Workspace, pkg *types.Package, raw map[declSite]analysis.ExtractionUnit, local map[declSite]*mergedUnit) {
	for site, m := range local {
		if !m.candidate {
			continue
		}

		for _, msg := range setRefusals(ws, pkg, raw, closureOf(local, site)) {
			if r.blockers[site] == nil {
				r.blockers[site] = make(map[string]bool)
			}

			r.blockers[site][msg] = true
		}
	}
}

// setRefusals returns what move_symbol refuses about moving the units of set
// together, reusing its own checks on entries built for the set.
func setRefusals(ws *types.Workspace, pkg *types.Package, raw map[declSite]analysis.ExtractionUnit, set map[declSite]bool) []string {
	op, moving := movingFor(ws, pkg, raw, set)

	var out []string

	for _, m := range moving {
		if m.typ != nil {
			continue
		}

		err := testMethodRefusal(ws, pkg, m.sym.Name)
		if err != nil {
			out = append(out, err.Error())
		}
	}

	err := op.checkGeneratedDeclarations(ws, moving)
	if err != nil {
		out = append(out, err.Error())
	}

	if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
		return out
	}

	for _, u := range strandedUses(ws, pkg, moving) {
		if u.field {
			out = append(out, u.error(pkg.Path).Error())
		}
	}

	err = checkSplitInterfaceImplementers(ws, pkg, moving)
	if err != nil {
		out = append(out, err.Error())
	}

	return out
}

// movingFor builds the operation and the entries move_symbol would hold for
// the names of the units in set: one entry per declaration, a method pointing
// at its type, each covering the declaration's text.
func movingFor(ws *types.Workspace, pkg *types.Package, raw map[declSite]analysis.ExtractionUnit, set map[declSite]bool) (*MoveSymbolOperation, []*movingSymbol) {
	var sites []declSite

	for site := range set {
		sites = append(sites, site)
	}

	slices.SortFunc(sites, func(a, b declSite) int { return int(a.Pos) - int(b.Pos) })

	files := make(map[string]*types.File, len(pkg.Files))
	for _, f := range pkg.Files {
		files[f.Path] = f
	}

	op := &MoveSymbolOperation{}
	byType := make(map[string]*movingSymbol)

	var (
		moving []*movingSymbol
		decls  []analysis.ExtractionDecl
	)

	for _, site := range sites {
		u := raw[site]
		owner := len(op.Request.Symbols)

		for _, n := range u.Names {
			op.Request.Symbols = append(op.Request.Symbols, types.QualifiedSymbol{Package: pkg.Path, Name: n})
		}

		for _, d := range u.Decls {
			start, end := ws.FileSet.Position(d.Site.Pos), ws.FileSet.Position(d.End)
			m := &movingSymbol{
				sym:     &types.Symbol{Name: d.Name, File: d.Site.File, Position: d.Site.Pos},
				pkg:     pkg,
				file:    files[d.Site.File],
				owner:   owner,
				removal: []types.Change{{File: d.Site.File, Start: start.Offset, End: end.Offset}},
			}

			if d.Type == "" {
				byType[d.Name] = m
			}

			moving = append(moving, m)
			decls = append(decls, d)
		}
	}

	for i, d := range decls {
		if d.Type != "" {
			moving[i].typ = byType[d.Type]
		}
	}

	return op, moving
}
