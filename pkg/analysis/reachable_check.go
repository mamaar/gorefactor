package analysis

import (
	"fmt"
	"go/ast"
	gotypes "go/types"
	"slices"

	"github.com/mamaar/gorefactor/pkg/types"
)

// checkedPkg is a package type-checked for SSA, with the files it was checked
// from.
type checkedPkg struct {
	pkg   *gotypes.Package
	files []*ast.File
	info  *gotypes.Info
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
}

func newSourceChecker(ws *types.Workspace) *sourceChecker {
	sc := &sourceChecker{
		ws:       ws,
		byPath:   make(map[string]*types.Package),
		outside:  make(map[string]*gotypes.Package),
		prod:     make(map[*types.Package]*checkedPkg),
		inFlight: make(map[*types.Package]bool),
		withTest: make(map[*types.Package]*checkedPkg),
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
		if pkg.TypesPkg != nil {
			walk(pkg.TypesPkg)
		}
	}
	return sc
}

// checkImporter resolves workspace imports to the production check of the package,
// except for override, which resolves to the given check.
type checkImporter struct {
	sc       *sourceChecker
	override map[string]*gotypes.Package
}

func (i checkImporter) Import(path string) (*gotypes.Package, error) {
	if path == "unsafe" {
		return gotypes.Unsafe, nil
	}
	if p := i.override[path]; p != nil {
		return p, nil
	}
	if wp := i.sc.byPath[path]; wp != nil {
		c, err := i.sc.production(wp)
		if err != nil {
			return nil, err
		}
		return c.pkg, nil
	}
	if p := i.sc.outside[path]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("cannot import %s", path)
}

// production returns the check of the non-test files of pkg.
func (sc *sourceChecker) production(pkg *types.Package) (*checkedPkg, error) {
	if c := sc.prod[pkg]; c != nil {
		return c, nil
	}
	if sc.inFlight[pkg] {
		return nil, fmt.Errorf("import cycle through %s", pkg.ImportPath)
	}
	sc.inFlight[pkg] = true
	defer delete(sc.inFlight, pkg)

	c, err := sc.check(pkg, sourceFiles(pkg, false, false), nil)
	if err != nil {
		return nil, err
	}
	if usesCgo(c.files) || !resolved(c) {
		c.files, c.info = nil, nil
	}
	sc.prod[pkg] = c
	return c, nil
}

// withTests returns the check of pkg with its in-package test files, or nil
// when they do not type-check.
func (sc *sourceChecker) withTests(pkg *types.Package) *checkedPkg {
	files := sourceFiles(pkg, true, false)
	if len(files) == len(sc.prod[pkg].files) {
		return nil
	}
	c, err := sc.check(pkg, files, nil)
	if err != nil {
		return nil
	}
	sc.withTest[pkg] = c
	return c
}

// externalTests returns the check of the external test package of pkg, or nil when
// there is none or it does not type-check.
func (sc *sourceChecker) externalTests(pkg *types.Package) *checkedPkg {
	files := sourceFiles(pkg, false, true)
	if len(files) == 0 {
		return nil
	}
	over := map[string]*gotypes.Package{}
	if t := sc.withTest[pkg]; t != nil {
		over[pkg.ImportPath] = t.pkg
	}
	c, err := sc.check(pkg, files, over)
	if err != nil {
		return nil
	}
	return c
}

func (sc *sourceChecker) check(pkg *types.Package, files []*ast.File, override map[string]*gotypes.Package) (*checkedPkg, error) {
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
	conf := NewTypesConfig(checkImporter{sc, override}, func(err error) {
		if first == nil {
			first = err
		}
	})
	tp, _ := conf.Check(pkg.ImportPath, sc.ws.FileSet, files, info)
	if first != nil {
		return nil, fmt.Errorf("type-checking %s: %w", pkg.ImportPath, first)
	}
	return &checkedPkg{tp, files, info}, nil
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
			if _, excluded := pkg.ExcludedTestFiles[name]; !excluded && isExternalTestFile(f.AST) == external {
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
				if a, isAssign := n.Assign.(*ast.AssignStmt); isAssign {
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

// usesCgo reports whether files import "C". SSA cannot build their code, so
// such a package, like one that is not resolved, is analysed as having no
// bodies.
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
