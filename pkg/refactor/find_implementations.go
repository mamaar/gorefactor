package refactor

import (
	"cmp"
	"fmt"
	gotypes "go/types"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// Implementation is a type that implements the queried interface, or an
// interface the queried type satisfies. With a method named, its position is
// that of the method.
type Implementation struct {
	Name    string
	Package string
	// File, Line and Column are empty for an interface outside the workspace.
	File   string
	Line   int
	Column int
	// PointerReceiver is set when only a pointer to the type satisfies the interface.
	PointerReceiver bool
	External        bool
	// Configs names the configurations that report it; empty when all do.
	Configs []string
}

// ImplementationsReport is the outcome of FindImplementationsAcrossConfigs.
type ImplementationsReport struct {
	Configs     []string // display names, primary first
	IsInterface bool
	// Found lists the implementing types when the queried type is an interface,
	// otherwise the interfaces it satisfies.
	Found []Implementation
	// Unchecked holds files of the directories involved that no configuration
	// type-checks: implementations in them are missing.
	Unchecked []analysis.UncheckedFile
}

// FindImplementationsAcrossConfigs finds, for the interface typeName, the
// workspace types that implement it and, for any other type, the interfaces it
// satisfies, under every build configuration covering the workspace. Interfaces
// outside the workspace are those the workspace imports or mentions. With
// method, the matching methods are reported instead of the types. Without pkg
// the type must be declared in one package; the first configuration that
// declares it fixes the package the others are searched in.
func (e *DefaultEngine) FindImplementationsAcrossConfigs(pkg, typeName, method string) (*ImplementationsReport, error) {
	run := &implementationsRun{
		pkg: pkg, typeName: typeName, method: method,
		found: make(map[string]*Implementation),
		dirs:  make(map[string]bool),
	}
	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("find implementations across configs: %w", err)
	}
	defer e.endRun(run)

	out := &ImplementationsReport{Configs: make([]string, len(views))}
	declared := false
	for i, v := range views {
		out.Configs[i] = v.Config.String()
		found, err := run.visit(v, out)
		if err != nil {
			return nil, viewError(views, v, err)
		}
		declared = declared || found
	}
	if !declared {
		return nil, fmt.Errorf("type %s not found", typeName)
	}

	for _, impl := range run.found {
		if len(impl.Configs) == len(views) {
			impl.Configs = nil
		}
		out.Found = append(out.Found, *impl)
	}
	slices.SortFunc(out.Found, func(a, b Implementation) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column),
			cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name))
	})
	out.Unchecked = analysis.UncheckedFiles(views, slices.Sorted(maps.Keys(run.dirs)))
	return out, nil
}

type implementationsRun struct {
	viewIndexes
	pkg, typeName, method string
	pkgPath               string // key of the package the first declaring view found
	found                 map[string]*Implementation
	dirs                  map[string]bool
}

// visit adds what v reports to the run and reports whether v declares the type.
func (r *implementationsRun) visit(v *analysis.View, out *ImplementationsReport) (bool, error) {
	defer r.planned(v, nil)
	r.indexOf(v) // waits for v to be type-checked, which the survey needs
	ws := v.Workspace
	named, pkgPath, err := r.lookup(ws)
	if err != nil {
		return false, err
	}
	if named == nil {
		return false, nil
	}
	r.pkgPath = pkgPath
	survey := surveyInterfaces(ws)
	var hits []Implementation
	_, isIface := asInterfaceType(named)
	if isIface {
		out.IsInterface = true
		hits, err = r.implementersOf(ws, survey, named)
	} else {
		hits, err = r.interfacesOf(ws, survey, named)
	}
	if err != nil {
		return true, err
	}
	r.dirs[filepath.Dir(ws.FileSet.Position(named.Obj().Pos()).Filename)] = true
	for _, impl := range hits {
		key := fmt.Sprintf("%s.%s|%s:%d:%d", impl.Package, impl.Name, impl.File, impl.Line, impl.Column)
		entry := r.found[key]
		if entry == nil {
			entry = &impl
			r.found[key] = entry
			if impl.File != "" {
				r.dirs[filepath.Dir(impl.File)] = true
			}
		}
		if !slices.Contains(entry.Configs, v.Config.String()) {
			entry.Configs = append(entry.Configs, v.Config.String())
		}
	}
	return true, nil
}

// lookup finds the type in ws: in the requested package, in the package the
// first declaring view settled on, or else in the only package declaring it.
// It returns the key of that package, and fails when several packages declare
// the type.
func (r *implementationsRun) lookup(ws *types.Workspace) (*gotypes.Named, string, error) {
	if r.pkg != "" {
		path := types.ResolvePackagePath(ws, r.pkg)
		return namedIn(ws.Packages[path], r.typeName), path, nil
	}
	var found []string
	for _, path := range slices.Sorted(maps.Keys(ws.Packages)) {
		if r.pkgPath != "" && path != r.pkgPath {
			continue
		}
		if namedIn(ws.Packages[path], r.typeName) != nil {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return nil, "", nil
	case 1:
		return namedIn(ws.Packages[found[0]], r.typeName), found[0], nil
	}
	return nil, "", fmt.Errorf("type %s is declared in several packages, pass package: %s", r.typeName, strings.Join(found, ", "))
}

func namedIn(pkg *types.Package, name string) *gotypes.Named {
	if pkg == nil || pkg.TypesPkg == nil {
		return nil
	}
	tn, ok := pkg.TypesPkg.Scope().Lookup(name).(*gotypes.TypeName)
	if !ok {
		return nil
	}
	named, _ := tn.Type().(*gotypes.Named)
	return named
}

// implementersOf returns the workspace types that implement the interface
// named, in each checked copy of it, deduplicated by declaration.
func (r *implementationsRun) implementersOf(ws *types.Workspace, s *ifaceSurvey, named *gotypes.Named) ([]Implementation, error) {
	var out []Implementation
	seen := make(map[analysis.ObjectID]bool)
	for _, view := range s.viewsOf(named) {
		iface, _ := asInterfaceType(view)
		if r.method != "" && interfaceMethodNamed(iface, r.method) == nil {
			return nil, fmt.Errorf("interface %s has no method %s", r.typeName, r.method)
		}
		for _, impl := range s.impls {
			id := analysis.ObjectIDOf(impl.Obj())
			if seen[id] || !typeImplements(impl, iface) {
				continue
			}
			where := gotypes.Object(impl.Obj())
			if r.method != "" {
				where = lookupMethod(impl, r.method)
			}
			seen[id] = true
			out = append(out, implementation(ws, impl.Obj(), where, !gotypes.Implements(impl, iface), false))
		}
	}
	return out, nil
}

// interfacesOf returns the interfaces the concrete type named satisfies, in
// each checked copy of it, deduplicated by declaration.
func (r *implementationsRun) interfacesOf(ws *types.Workspace, s *ifaceSurvey, named *gotypes.Named) ([]Implementation, error) {
	if r.method != "" && lookupMethod(named, r.method) == nil {
		return nil, fmt.Errorf("type %s has no method %s", r.typeName, r.method)
	}
	var out []Implementation
	self := analysis.ObjectIDOf(named.Obj())
	seen := make(map[analysis.ObjectID]bool)
	collect := func(view gotypes.Type, cands []ifaceCandidate, external bool) {
		for _, cand := range cands {
			id := analysis.ObjectIDOf(cand.named.Obj())
			if id == self || seen[id] || cand.iface.NumMethods() == 0 || !typeImplements(view, cand.iface) {
				continue
			}
			where := gotypes.Object(cand.named.Obj())
			if r.method != "" {
				m := interfaceMethodNamed(cand.iface, r.method)
				if m == nil {
					continue
				}
				where = m
			}
			seen[id] = true
			out = append(out, implementation(ws, cand.named.Obj(), where, !gotypes.Implements(view, cand.iface), external))
		}
	}
	for _, view := range s.viewsOf(named) {
		collect(view, s.workspace, false)
		collect(view, s.external, true)
	}
	return out, nil
}

// implementation describes obj, located by where when it is in the workspace.
func implementation(ws *types.Workspace, obj, where gotypes.Object, pointer, external bool) Implementation {
	impl := Implementation{Name: obj.Name(), PointerReceiver: pointer, External: external}
	if obj.Pkg() != nil {
		impl.Package = obj.Pkg().Path()
	}
	if !external && where != nil {
		p := ws.FileSet.Position(where.Pos())
		impl.File, impl.Line, impl.Column = p.Filename, p.Line, p.Column
	}
	return impl
}
