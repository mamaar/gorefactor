package refactor

import (
	"cmp"
	"fmt"
	gotypes "go/types"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/mamaar/gorefactor/pkg/analysis"
)

// DependencyUsageRequest selects what DependencyUsageAcrossConfigs reports.
type DependencyUsageRequest struct {
	// MaxReferences keeps the modules referenced at most this many times; nil
	// keeps every module.
	MaxReferences *int
}

// SymbolUsage is how often one symbol of a dependency package is referenced.
type SymbolUsage struct {
	Symbol         string // Name or Type.Method
	References     int
	TestReferences int
}

// PackageUsage is how often a dependency package is referenced.
type PackageUsage struct {
	Package        string
	References     int
	TestReferences int
	Symbols        []SymbolUsage
}

// ModuleUsage is how often a required module is referenced.
type ModuleUsage struct {
	Module         string
	Version        string
	Indirect       bool
	References     int
	TestReferences int
	Files          int
	Packages       []PackageUsage
}

// DependencyUsageReport is the outcome of DependencyUsageAcrossConfigs.
type DependencyUsageReport struct {
	DetectHeader
	Modules []ModuleUsage
}

// DependencyUsageAcrossConfigs counts the references the workspace makes to
// each module in its go.mod require list, merged across build configurations.
func (e *DefaultEngine) DependencyUsageAcrossConfigs(req DependencyUsageRequest) (*DependencyUsageReport, error) {
	run := &dependencyUsageRun{
		detectRun:   newDetectRun(nil),
		refs:        make(map[depRefSite]depRefInfo),
		fieldOwners: make(map[*gotypes.Package]map[*gotypes.Var]string),
	}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("dependency usage across configs: %w", err)
	}
	defer e.endRun(run)

	err = run.parseGoMod(views[0])
	if err != nil {
		return nil, err
	}

	for _, v := range views {
		err := run.visit(v)
		if err != nil {
			return nil, viewError(views, v, err)
		}
	}

	return run.report(req, views), nil
}

// depRefSite is one reference, identified so that every configuration checking a
// file counts it once.
type depRefSite struct {
	file   string
	offset int
}

type depRefInfo struct {
	module, pkg, symbol string
	test                bool
}

type dependencyUsageRun struct {
	detectRun
	refs     map[depRefSite]depRefInfo
	requires []*modfile.Require
	// paths are the require paths; modulePath is the workspace's own module.
	paths      []string
	modulePath string
	// fieldOwners names the struct type declaring each field, per package.
	fieldOwners map[*gotypes.Package]map[*gotypes.Var]string
}

func (r *dependencyUsageRun) parseGoMod(v *analysis.View) error {
	mod := v.Workspace.Module
	if mod == nil {
		return fmt.Errorf("no go.mod at workspace root")
	}

	mf, err := modfile.ParseLax("go.mod", []byte(mod.GoMod), nil)
	if err != nil {
		return fmt.Errorf("parse go.mod: %w", err)
	}

	r.requires = mf.Require
	r.modulePath = mod.Path

	for _, req := range r.requires {
		r.paths = append(r.paths, req.Mod.Path)
	}

	return nil
}

func (r *dependencyUsageRun) visit(v *analysis.View) error {
	idx := r.indexOf(v)
	if !idx.HasTypeInfo() {
		return fmt.Errorf("no package has type information")
	}

	ws := v.Workspace

	for _, pkg := range ws.Packages {
		if pkg.TypesInfo == nil {
			continue
		}

		for id, obj := range pkg.TypesInfo.Uses {
			_, isPkgName := obj.(*gotypes.PkgName)
			if isPkgName || obj.Pkg() == nil {
				continue
			}

			path := obj.Pkg().Path()
			if moduleOf(path, []string{r.modulePath}) != "" {
				continue
			}

			module := moduleOf(path, r.paths)
			if module == "" {
				continue
			}

			pos := ws.FileSet.Position(id.Pos())
			r.refs[depRefSite{pos.Filename, pos.Offset}] = depRefInfo{
				module: module,
				pkg:    obj.Pkg().Path(),
				symbol: r.symbolName(obj),
				test:   strings.HasSuffix(pos.Filename, "_test.go"),
			}
		}
	}

	r.collectTypeErrors(v)
	r.planned(v, nil)

	return nil
}

func (r *dependencyUsageRun) report(req DependencyUsageRequest, views []*analysis.View) *DependencyUsageReport {
	type tally struct {
		total, test int
	}

	type bucket struct {
		tally
		files   map[string]bool
		pkgs    map[string]*tally
		symbols map[[2]string]*tally
	}

	newBucket := func() *bucket {
		return &bucket{files: map[string]bool{}, pkgs: map[string]*tally{}, symbols: map[[2]string]*tally{}}
	}

	buckets := make(map[string]*bucket)

	for _, req := range r.requires {
		if !req.Indirect {
			buckets[req.Mod.Path] = newBucket()
		}
	}

	for site, ref := range r.refs {
		b := buckets[ref.module]
		if b == nil {
			b = newBucket()
			buckets[ref.module] = b
		}

		symKey := [2]string{ref.pkg, ref.symbol}
		if b.pkgs[ref.pkg] == nil {
			b.pkgs[ref.pkg] = &tally{}
		}

		if b.symbols[symKey] == nil {
			b.symbols[symKey] = &tally{}
		}

		for _, t := range []*tally{&b.tally, b.pkgs[ref.pkg], b.symbols[symKey]} {
			t.total++
			if ref.test {
				t.test++
			}
		}

		b.files[site.file] = true
	}

	out := &DependencyUsageReport{DetectHeader: r.header(views)}

	for _, rq := range r.requires {
		b := buckets[rq.Mod.Path]
		if b == nil || (req.MaxReferences != nil && b.total > *req.MaxReferences) {
			continue
		}

		m := ModuleUsage{
			Module: rq.Mod.Path, Version: rq.Mod.Version, Indirect: rq.Indirect,
			References: b.total, TestReferences: b.test, Files: len(b.files),
		}

		for pkg, t := range b.pkgs {
			p := PackageUsage{Package: pkg, References: t.total, TestReferences: t.test}

			for key, s := range b.symbols {
				if key[0] == pkg {
					p.Symbols = append(p.Symbols, SymbolUsage{key[1], s.total, s.test})
				}
			}

			slices.SortFunc(p.Symbols, func(a, b SymbolUsage) int {
				return cmp.Or(cmp.Compare(a.References, b.References), cmp.Compare(a.Symbol, b.Symbol))
			})

			m.Packages = append(m.Packages, p)
		}

		slices.SortFunc(m.Packages, func(a, b PackageUsage) int {
			return cmp.Or(cmp.Compare(a.References, b.References), cmp.Compare(a.Package, b.Package))
		})

		out.Modules = append(out.Modules, m)
	}

	slices.SortFunc(out.Modules, func(a, b ModuleUsage) int {
		return cmp.Or(cmp.Compare(a.References, b.References), cmp.Compare(a.Module, b.Module))
	})

	return out
}

// moduleOf returns the longest of required that equals pkgPath or is a
// /-bounded prefix of it, or "" when none is.
func moduleOf(pkgPath string, required []string) string {
	best := ""

	for _, m := range required {
		if pkgPath != m && !strings.HasPrefix(pkgPath, m+"/") {
			continue
		}

		if len(m) > len(best) {
			best = m
		}
	}

	return best
}

// symbolName is Type.Method for a method, Type.Field for a field of a named
// struct type and the object's name otherwise.
func (r *dependencyUsageRun) symbolName(obj gotypes.Object) string {
	v, ok := obj.(*gotypes.Var)
	if ok && v.IsField() {
		owner := r.fieldOwnersOf(v.Pkg())[v.Origin()]
		if owner == "" {
			// A field of an unnamed struct has no owner to name; same-named ones merge.
			return v.Name()
		}

		return owner + "." + v.Name()
	}

	fn, ok := obj.(*gotypes.Func)
	if !ok {
		return obj.Name()
	}

	recv := fn.Signature().Recv()
	if recv == nil {
		return fn.Name()
	}

	t := recv.Type()

	ptr, ok := t.(*gotypes.Pointer)
	if ok {
		t = ptr.Elem()
	}

	named, ok := t.(*gotypes.Named)
	if !ok {
		return fn.Name()
	}

	return named.Obj().Name() + "." + fn.Name()
}

// fieldOwnersOf maps each field of pkg's named struct types to the type's name.
func (r *dependencyUsageRun) fieldOwnersOf(pkg *gotypes.Package) map[*gotypes.Var]string {
	owners, ok := r.fieldOwners[pkg]
	if ok {
		return owners
	}

	owners = make(map[*gotypes.Var]string)
	r.fieldOwners[pkg] = owners

	for _, name := range pkg.Scope().Names() {
		tn, ok := pkg.Scope().Lookup(name).(*gotypes.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}

		st, ok := tn.Type().Underlying().(*gotypes.Struct)
		if !ok {
			continue
		}

		for f := range st.Fields() {
			owners[f.Origin()] = name
		}
	}

	return owners
}
