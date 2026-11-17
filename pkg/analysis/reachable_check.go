package analysis

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"maps"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// checkedPkg is a package type-checked for SSA, with the files it was checked
// from.
type checkedPkg struct {
	pkg      *gotypes.Package
	files    []*ast.File
	info     *gotypes.Info
	bodiless string // why SSA cannot build the package's code; empty when it can
}

// EntryBodilessError is returned when the entry package of an analysis is
// bodiless, so nothing can be said about what it runs.
type EntryBodilessError struct{ Package, Reason string }

func (e *EntryBodilessError) Error() string {
	return fmt.Sprintf("entry package %s cannot be analysed: %s", e.Package, e.Reason)
}

// BodilessPackage is a package whose code the analysis cannot see: its
// functions have no bodies, so calls inside them are not found.
type BodilessPackage struct {
	Package string // import path; test variants carry a [tests] or [xtests] suffix
	Reason  string
}

// sourceChecker type-checks workspace packages for SSA. The loaded packages
// cannot serve: one re-checked after its dependents leaves them importing a
// different *types.Package, which SSA treats as another package.
type sourceChecker struct {
	ws       *types.Workspace
	byPath   map[string]*types.Package
	outside  map[string]*gotypes.Package // packages outside the workspace, by path
	prod     map[*types.Package]*checkedPkg
	inFlight map[*types.Package]bool
	withTest map[*types.Package]*checkedPkg
	reported map[string]bool
	bodiless []BodilessPackage
}

func newSourceChecker(ws *types.Workspace) *sourceChecker {
	sc := &sourceChecker{
		ws:       ws,
		byPath:   make(map[string]*types.Package),
		outside:  make(map[string]*gotypes.Package),
		prod:     make(map[*types.Package]*checkedPkg),
		inFlight: make(map[*types.Package]bool),
		withTest: make(map[*types.Package]*checkedPkg),
		reported: make(map[string]bool),
	}
	for _, pkg := range ws.Packages {
		sc.byPath[pkg.ImportPath] = pkg
	}
	var walk func(p *gotypes.Package)
	walk = func(p *gotypes.Package) {
		for _, imp := range p.Imports() {
			if sc.byPath[imp.Path()] == nil && sc.outside[imp.Path()] == nil {
				sc.outside[imp.Path()] = imp
				walk(imp)
			}
		}
	}

	for _, pkg := range ws.Packages {
		for _, tp := range []*gotypes.Package{pkg.TypesPkg, pkg.ExternalTestTypesPkg} {
			if tp != nil {
				walk(tp)
			}
		}
	}

	return sc
}

// checkImporter resolves workspace imports to the production check of the
// package. Under override, which maps a path to the check to use for it, a
// workspace package that depends on an overridden one is rechecked against it,
// as the go tool does for an external test package.
type checkImporter struct {
	sc       *sourceChecker
	override map[string]*gotypes.Package
	rebuilt  map[string]*checkedPkg
}

func (i checkImporter) Import(path string) (*gotypes.Package, error) {
	if path == "unsafe" {
		return gotypes.Unsafe, nil
	}

	p := i.override[path]
	if p != nil {
		return p, nil
	}

	wp := i.sc.byPath[path]
	if wp != nil {
		c, err := i.sc.production(wp)
		if err != nil {
			return nil, err
		}

		if len(i.override) == 0 || !i.sc.dependsOn(c.pkg, i.override, map[string]bool{}) {
			return c.pkg, nil
		}

		r := i.rebuilt[path]
		if r != nil {
			return r.pkg, nil
		}

		r, err = i.sc.check(wp, sourceFiles(wp, false, false), i)
		if err != nil {
			return nil, err
		}

		r.bodiless = bodilessReason(r)
		if r.bodiless != "" {
			r.files, r.info = nil, nil
		}

		i.rebuilt[path] = r

		return r.pkg, nil
	}

	p = i.sc.outside[path]
	if p != nil {
		return p, nil
	}

	return nil, fmt.Errorf("cannot import %s", path)
}

// dependsOn reports whether p imports, directly or through workspace
// packages, a package whose path is in paths.
func (sc *sourceChecker) dependsOn(p *gotypes.Package, paths map[string]*gotypes.Package, seen map[string]bool) bool {
	for _, imp := range p.Imports() {
		if paths[imp.Path()] != nil {
			return true
		}

		if seen[imp.Path()] || sc.byPath[imp.Path()] == nil {
			continue
		}

		seen[imp.Path()] = true
		if sc.dependsOn(imp, paths, seen) {
			return true
		}
	}

	return false
}

// production returns the check of the non-test files of pkg.
func (sc *sourceChecker) production(pkg *types.Package) (*checkedPkg, error) {
	c := sc.prod[pkg]
	if c != nil {
		return c, nil
	}

	if sc.inFlight[pkg] {
		return nil, fmt.Errorf("import cycle through %s", pkg.ImportPath)
	}

	sc.inFlight[pkg] = true
	defer delete(sc.inFlight, pkg)

	c, err := sc.check(pkg, sourceFiles(pkg, false, false), checkImporter{sc: sc})
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}

	c.bodiless = bodilessReason(c)
	if c.bodiless != "" {
		c.files, c.info = nil, nil
		sc.report(pkg.ImportPath, c.bodiless)
	}

	sc.prod[pkg] = c

	return c, nil
}

// report records that the code of label cannot be analysed.
func (sc *sourceChecker) report(label, reason string) {
	if !sc.reported[label] {
		sc.reported[label] = true
		sc.bodiless = append(sc.bodiless, BodilessPackage{label, reason})
	}
}

// withTests returns the check of pkg with its in-package test files, or nil
// when there are none or they cannot be analysed.
func (sc *sourceChecker) withTests(pkg *types.Package) *checkedPkg {
	files := sourceFiles(pkg, true, false)
	if sc.prod[pkg].bodiless != "" || len(files) == len(sc.prod[pkg].files) {
		return nil
	}

	c := sc.checkTests(pkg.ImportPath+" [tests]", pkg, files, checkImporter{sc: sc})
	sc.withTest[pkg] = c

	return c
}

// externalTests returns the check of the external test package of pkg, nil
// when there is none or it cannot be analysed, and the checks of the packages
// rechecked against the tests of pkg.
func (sc *sourceChecker) externalTests(pkg *types.Package) (*checkedPkg, []*checkedPkg) {
	files := sourceFiles(pkg, false, true)
	if len(files) == 0 {
		return nil, nil
	}

	imp := checkImporter{sc: sc, override: map[string]*gotypes.Package{}, rebuilt: map[string]*checkedPkg{}}

	t := sc.withTest[pkg]
	if t != nil {
		imp.override[pkg.ImportPath] = t.pkg
	}

	c := sc.checkTests(pkg.ImportPath+" [xtests]", pkg, files, imp)
	rebuilt := slices.SortedFunc(maps.Values(imp.rebuilt), func(a, b *checkedPkg) int { return strings.Compare(a.pkg.Path(), b.pkg.Path()) })

	return c, rebuilt
}

// checkTests checks a test variant of pkg, reporting it under label and
// returning nil when it cannot be analysed.
func (sc *sourceChecker) checkTests(label string, pkg *types.Package, files []*ast.File, imp gotypes.Importer) *checkedPkg {
	c, err := sc.check(pkg, files, imp)
	if err != nil {
		sc.report(label, err.Error())
		return nil
	}

	c.bodiless = bodilessReason(c)
	if c.bodiless != "" {
		sc.report(label, c.bodiless)
		return nil
	}

	return c
}

func (sc *sourceChecker) check(pkg *types.Package, files []*ast.File, imp gotypes.Importer) (*checkedPkg, error) {
	info := &gotypes.Info{
		Types:      make(map[ast.Expr]gotypes.TypeAndValue),
		Defs:       make(map[*ast.Ident]gotypes.Object),
		Uses:       make(map[*ast.Ident]gotypes.Object),
		Implicits:  make(map[ast.Node]gotypes.Object),
		Scopes:     make(map[ast.Node]*gotypes.Scope),
		Selections: make(map[*ast.SelectorExpr]*gotypes.Selection),
		Instances:  make(map[*ast.Ident]gotypes.Instance),
	}
	var first error
	conf := NewTypesConfig(imp, PackageGoVersion(sc.ws, pkg.Dir), func(err error) {
		if first == nil {
			first = err
		}
	})

	tp, _ := conf.Check(pkg.ImportPath, sc.ws.FileSet, files, info)
	if first != nil {
		return nil, fmt.Errorf("type-checking %s: %w", pkg.ImportPath, first)
	}

	return &checkedPkg{pkg: tp, files: files, info: info}, nil
}

// sourceFiles returns the syntax trees of pkg in file name order: the
// non-test files, plus with tests the in-package test files, or with external
// only the external test files.
func sourceFiles(pkg *types.Package, tests, external bool) []*ast.File {
	var files []*ast.File

	if !external {
		for _, name := range sortedKeys(pkg.Files) {
			files = append(files, pkg.Files[name].AST)
		}
	}

	if tests || external {
		for _, name := range sortedKeys(pkg.TestFiles) {
			f := pkg.TestFiles[name]

			_, excluded := pkg.ExcludedTestFiles[name]
			if !excluded && isExternalTestFile(f.AST) == external {
				files = append(files, f.AST)
			}
		}
	}

	return files
}

func sortedKeys(m map[string]*types.File) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// resolved reports whether the checker resolved every identifier of c. An
// import the loader could only partly check leaves some without a type error.
func resolved(c *checkedPkg) bool {
	ok := true

	for _, f := range c.files {
		guards := map[*ast.Ident]bool{f.Name: true} // a type switch guard has only per-clause objects
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSwitchStmt:
				a, isAssign := n.Assign.(*ast.AssignStmt)
				if isAssign {
					guards[a.Lhs[0].(*ast.Ident)] = true
				}
			case *ast.Ident:
				if !guards[n] && n.Name != "_" && c.info.Uses[n] == nil && c.info.Defs[n] == nil && c.info.Implicits[n] == nil {
					ok = false
				}
			}

			return ok
		})
	}

	return ok
}

// bodilessReason says why SSA cannot build c, or returns "" when it can.
func bodilessReason(c *checkedPkg) string {
	switch {
	case usesCgo(c.files):
		return "cgo"
	case !resolved(c):
		return "unresolved identifiers"
	}

	return ""
}

// usesCgo reports whether files import "C".
func usesCgo(files []*ast.File) bool {
	for _, f := range files {
		for _, imp := range f.Imports {
			if imp.Path.Value == `"C"` {
				return true
			}
		}
	}

	return false
}

// WithinUnreachedError is returned when the declaration a scoped analysis starts
// from is not reached by the entry program.
type WithinUnreachedError struct{ Function string }

func (e *WithinUnreachedError) Error() string {
	return fmt.Sprintf("within_function %s is not reached by the entry program", e.Function)
}
