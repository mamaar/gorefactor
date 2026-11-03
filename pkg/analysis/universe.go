package analysis

import (
	"fmt"
	gotypes "go/types"
	"maps"
	"slices"

	"github.com/mamaar/gorefactor/pkg/types"
)

// UniverseErrors describes every reference in ws's type information to a
// go/types package other than the one the importer hands out for that import
// path. It is empty when each workspace package has one object per importer.
func (p *GoParser) UniverseErrors(ws *types.Workspace) []string {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	u := universe{ws: ws}
	if p.importer != nil {
		u.productionOnly = p.importer.productionOnly
	}

	for _, path := range slices.Sorted(maps.Keys(ws.Packages)) {
		pkg := ws.Packages[path]
		u.checkImports(pkg.ImportPath, pkg.TypesPkg)
		u.checkImports(pkg.ImportPath+" (production only)", u.productionOnly[pkg])
		u.checkUses(pkg)
	}

	slices.Sort(u.errs)

	return slices.Compact(u.errs)
}

type universe struct {
	ws             *types.Workspace
	productionOnly map[*types.Package]*gotypes.Package
	errs           []string
}

// canonical returns the package the importer hands out for a workspace import
// path, or nil for any other path.
func (u *universe) canonical(importPath string) *gotypes.Package {
	pkg := u.ws.Packages[u.ws.ImportToPath[importPath]]
	if pkg == nil {
		return nil
	}

	rescued, ok := u.productionOnly[pkg]
	if ok {
		return rescued
	}

	return pkg.TypesPkg
}

func (u *universe) checkImports(from string, checked *gotypes.Package) {
	if checked == nil {
		return
	}

	for _, imp := range checked.Imports() {
		u.checkRef(from, imp)
	}
}

// checkUses covers the external test files of pkg, whose own go/types package
// is not kept. References to pkg itself are skipped: its files see the package
// under check, its external tests the one handed to importers.
func (u *universe) checkUses(pkg *types.Package) {
	if pkg.TypesInfo == nil {
		return
	}

	for _, obj := range pkg.TypesInfo.Uses {
		ref := obj.Pkg()
		if ref != nil && ref.Path() != pkg.ImportPath {
			u.checkRef(pkg.ImportPath, ref)
		}
	}
}

func (u *universe) checkRef(from string, ref *gotypes.Package) {
	want := u.canonical(ref.Path())
	if want == nil || want == ref {
		return
	}

	u.errs = append(u.errs, fmt.Sprintf("%s refers to a copy of %s (%p) that is not the one importers get (%p)", from, ref.Path(), ref, want))
}
