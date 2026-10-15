package analysis

import (
	"cmp"
	"fmt"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Target is a function or method whose call sites ReachableSites reports.
type Target struct {
	Spec string
	Obj  gotypes.Object
}

// Name is pkg.Func or pkg.Type.Method.
func (t Target) Name() string {
	if fn, ok := t.Obj.(*gotypes.Func); ok {
		if recv := fn.Signature().Recv(); recv != nil {
			typ := recv.Type()
			if p, ok := typ.(*gotypes.Pointer); ok {
				typ = p.Elem()
			}
			if named, _, _, ok := namedOf(typ); ok {
				return t.Obj.Pkg().Name() + "." + named.Obj().Name() + "." + fn.Name()
			}
		}
	}
	return t.Obj.Pkg().Name() + "." + t.Obj.Name()
}

// ReachableSite is a reference to a target from code that runs when the entry
// program runs.
type ReachableSite struct {
	File      string
	Line      int
	Pos       token.Pos
	Package   string
	Enclosing string // Recv.Name of the declaration holding the site, or its name
	Target    string
	Kind      string   // "call" or "reference"
	Path      []string // declarations from a root to Enclosing, as pkg.Name
}

// ResolveTargets resolves specs to objects. A spec is a package path (every
// exported func and method of it), pkg.Func, or pkg.Type.Method, where pkg is
// a workspace package or one a workspace package imports. Overlapping specs
// yield each object once.
func ResolveTargets(ws *types.Workspace, specs []string) ([]Target, error) {
	pkgs := make(map[string]*gotypes.Package)
	for _, pkg := range ws.Packages {
		if pkg.TypesPkg == nil {
			continue
		}
		pkgs[pkg.TypesPkg.Path()] = pkg.TypesPkg
		for _, imp := range pkg.TypesPkg.Imports() {
			pkgs[imp.Path()] = imp
		}
	}

	var targets []Target
	seen := make(map[ObjectID]bool)
	for _, spec := range specs {
		objs, ok := resolveSpec(pkgs, spec)
		if !ok {
			return nil, fmt.Errorf("unknown target %q", spec)
		}
		for _, obj := range objs {
			if id := ObjectIDOf(obj); !seen[id] {
				seen[id] = true
				targets = append(targets, Target{Spec: spec, Obj: canonicalObject(obj)})
			}
		}
	}
	return targets, nil
}

func resolveSpec(pkgs map[string]*gotypes.Package, spec string) ([]gotypes.Object, bool) {
	if pkg := pkgs[spec]; pkg != nil {
		return exportedCallables(pkg), true
	}

	var pkg *gotypes.Package
	for path, p := range pkgs {
		if strings.HasPrefix(spec, path+".") && (pkg == nil || len(path) > len(pkg.Path())) {
			pkg = p
		}
	}
	if pkg == nil {
		return nil, false
	}

	parts := strings.Split(spec[len(pkg.Path())+1:], ".")
	switch len(parts) {
	case 1:
		if fn, ok := pkg.Scope().Lookup(parts[0]).(*gotypes.Func); ok {
			return []gotypes.Object{fn}, true
		}
	case 2:
		tn, ok := pkg.Scope().Lookup(parts[0]).(*gotypes.TypeName)
		if !ok {
			return nil, false
		}
		obj, _, _ := gotypes.LookupFieldOrMethod(tn.Type(), true, pkg, parts[1])
		if fn, ok := obj.(*gotypes.Func); ok {
			return []gotypes.Object{fn}, true
		}
	}
	return nil, false
}

// exportedCallables lists the exported funcs of pkg and the exported methods
// of its named types.
func exportedCallables(pkg *gotypes.Package) []gotypes.Object {
	var objs []gotypes.Object
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		switch obj := scope.Lookup(name).(type) {
		case *gotypes.Func:
			if obj.Exported() {
				objs = append(objs, obj)
			}
		case *gotypes.TypeName:
			named, ok := obj.Type().(*gotypes.Named)
			if !ok {
				continue
			}
			for m := range named.Methods() {
				if m.Exported() {
					objs = append(objs, m)
				}
			}
			if iface, ok := named.Underlying().(*gotypes.Interface); ok {
				for m := range iface.ExplicitMethods() {
					if m.Exported() {
						objs = append(objs, m)
					}
				}
			}
		}
	}
	return objs
}

// ReachableSites returns the call sites and references of targets that sit in
// code reachable from main of entry, across the packages it imports. An
// interface call reaches every exported method, and every method named by a
// workspace interface, of every type reachable code mentions; that
// over-approximates. Exported funcs are not roots. Test code counts only with
// includeTests. The type-checked packages must already be in the index.
func (ua *UnusedAnalyzer) ReachableSites(entry *types.Package, targets []Target, includeTests bool) ([]ReachableSite, error) {
	if entry.Name != "main" {
		return nil, fmt.Errorf("entry package %s is not package main", entry.Path)
	}
	g, idx, m, err := ua.prepare()
	if err != nil {
		return nil, err
	}
	if !idx.HasTypeInfo() {
		return nil, fmt.Errorf("packages are not type-checked")
	}

	closure := ua.importClosure(entry, includeTests)
	isRoot := func(n *usageNode) bool { return m.isReachableRoot(n, entry, closure, includeTests) }
	hasMain := false
	for _, n := range g.nodes {
		hasMain = hasMain || (n.name == "main" && isRoot(n))
	}
	if !hasMain {
		return nil, fmt.Errorf("entry package %s has no func main", entry.Path)
	}

	m.includeExported, m.followTests = false, includeTests
	m.markFrom(g, isRoot)

	var sites []ReachableSite
	add := func(target Target, file *types.File, pos token.Pos, kind string) {
		if !includeTests && strings.HasSuffix(file.Path, "_test.go") {
			return
		}
		for _, owner := range g.enclosing(pos) {
			if !m.live[owner] {
				continue
			}
			sites = append(sites, ReachableSite{
				File:      file.Path,
				Line:      idx.workspaceIdx.fileSet.Position(pos).Line,
				Pos:       pos,
				Package:   packageLabel(file.Package),
				Enclosing: declName(owner),
				Target:    target.Name(),
				Kind:      kind,
				Path:      m.pathTo(owner),
			})
			return
		}
	}
	for _, target := range targets {
		for _, cs := range idx.CallSitesOf(target.Obj) {
			add(target, cs.File, cs.Call.Pos(), "call")
		}
		for _, use := range idx.NonCallUsesOf(target.Obj) {
			add(target, use.File, use.Pos, "reference")
		}
	}

	slices.SortFunc(sites, func(a, b ReachableSite) int {
		return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.File, b.File), cmp.Compare(a.Pos, b.Pos), cmp.Compare(a.Target, b.Target))
	})
	return sites, nil
}

// importClosure returns entry and the workspace packages it imports
// transitively, through test files too when includeTests.
func (ua *UnusedAnalyzer) importClosure(entry *types.Package, includeTests bool) map[*types.Package]bool {
	ws := ua.workspace
	closure := map[*types.Package]bool{}
	work := []*types.Package{entry}
	for len(work) > 0 {
		pkg := work[len(work)-1]
		work = work[:len(work)-1]
		if closure[pkg] {
			continue
		}
		closure[pkg] = true
		for _, file := range indexedFiles(pkg) {
			if !includeTests && strings.HasSuffix(file.Path, "_test.go") {
				continue
			}
			for _, imp := range file.AST.Imports {
				if path, ok := ws.ImportToPath[strings.Trim(imp.Path.Value, `"`)]; ok && ws.Packages[path] != nil {
					work = append(work, ws.Packages[path])
				}
			}
		}
	}
	return closure
}

// isReachableRoot reports whether n runs without being referenced when the
// entry program runs: main, init, package variable initializers, and methods
// that may be called through an interface from outside the graph. With
// includeTests it also accepts tests.
func (m *marker) isReachableRoot(n *usageNode, entry *types.Package, closure map[*types.Package]bool, includeTests bool) bool {
	if !closure[n.pkg] || (n.test && !includeTests) {
		return false
	}
	s := n.symbol
	if s != nil && s.Kind == types.MethodSymbol {
		return n.recv == nil && m.isKeptMethod(n)
	}
	switch {
	case n.name == "main":
		return n.pkg == entry && !n.test && s != nil && s.Kind == types.FunctionSymbol
	case n.name == "init" || n.name == "_":
		return true
	case s == nil:
		return n.test
	case s.Kind == types.VariableSymbol:
		return true
	}
	return n.test && m.ua.isTestFunction(s)
}

// pathTo returns the declarations from a root to n.
func (m *marker) pathTo(n *usageNode) []string {
	var path []string
	for ; n != nil; n = m.from[n] {
		path = append(path, packageName(n)+"."+declName(n))
	}
	slices.Reverse(path)
	return path
}

func packageName(n *usageNode) string {
	if n.pkg == nil {
		return ""
	}
	return n.pkg.Name
}

func declName(n *usageNode) string {
	if n.recv != nil {
		return n.recv.name + "." + n.name
	}
	return n.name
}

func packageLabel(pkg *types.Package) string {
	return cmp.Or(pkg.ImportPath, pkg.Path)
}

// ReachableDirs returns the directories of the packages ReachableSites
// examines for entry, sorted.
func (ua *UnusedAnalyzer) ReachableDirs(entry *types.Package, includeTests bool) []string {
	var dirs []string
	for pkg := range ua.importClosure(entry, includeTests) {
		dirs = append(dirs, pkg.Dir)
	}
	slices.Sort(dirs)
	return dirs
}
