package refactor

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// SymbolDeclaration is one place a symbol is declared.
type SymbolDeclaration struct {
	File   string
	Line   int
	Column int
	// Configs names the configurations that declare it; empty when all do.
	Configs []string
}

// SymbolReport is the outcome of AnalyzeSymbolAcrossConfigs.
type SymbolReport struct {
	Configs []string // display names, primary first
	// Symbol is the declaration of the primary configuration, or of the first
	// configuration that declares the symbol.
	Symbol       *types.Symbol
	Declarations []SymbolDeclaration
	// References are deduplicated by position.
	References []*types.Reference
	// Unchecked holds files of the declaring and referencing directories that
	// no configuration type-checks: references in them are missing.
	Unchecked []analysis.UncheckedFile
}

// AnalyzeSymbolAcrossConfigs finds the declarations of name, in pkg when given,
// and their references under every build configuration covering the workspace.
// Without pkg the first package declaring name in the first configuration that
// does fixes the package the other configurations are searched in.
func (e *DefaultEngine) AnalyzeSymbolAcrossConfigs(name, pkg string) (*SymbolReport, error) {
	run := &symbolRun{
		name:  name,
		pkg:   pkg,
		decls: make(map[declSite]*SymbolDeclaration),
		refs:  make(map[symbolRefSite]*types.Reference),
		dirs:  make(map[string]bool),
	}

	views, err := e.beginRun(run)
	if err != nil {
		return nil, fmt.Errorf("analyze symbol across configs: %w", err)
	}
	defer e.endRun(run)

	out := &SymbolReport{Configs: make([]string, len(views))}
	for i, v := range views {
		out.Configs[i] = v.Config.String()

		err := run.visit(e, v, out)
		if err != nil {
			return nil, viewError(views, v, err)
		}
	}

	if out.Symbol == nil {
		return nil, fmt.Errorf("symbol %s not found", name)
	}

	for _, d := range run.decls {
		if len(d.Configs) == len(views) {
			d.Configs = nil
		}

		out.Declarations = append(out.Declarations, *d)
	}

	slices.SortFunc(out.Declarations, func(a, b SymbolDeclaration) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	out.References = slices.Collect(maps.Values(run.refs))
	slices.SortFunc(out.References, func(a, b *types.Reference) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	out.Unchecked = analysis.UncheckedFiles(views, slices.Sorted(maps.Keys(run.dirs)))

	return out, nil
}

type symbolRefSite struct {
	file         string
	line, column int
}

type symbolRun struct {
	viewIndexes
	name, pkg string
	pkgPath   string // directory of the package the first declaring view found
	decls     map[declSite]*SymbolDeclaration
	refs      map[symbolRefSite]*types.Reference
	dirs      map[string]bool
}

func (r *symbolRun) visit(e *DefaultEngine, v *analysis.View, out *SymbolReport) error {
	defer r.planned(v, nil)

	symbol, pkgPath := r.lookup(v.Workspace)
	if symbol == nil {
		return nil
	}

	r.pkgPath = pkgPath

	if out.Symbol == nil {
		out.Symbol = symbol
	}

	site := declSite{symbol.File, symbol.Position}

	d := r.decls[site]
	if d == nil {
		d = &SymbolDeclaration{File: symbol.File, Line: symbol.Line, Column: symbol.Column}
		r.decls[site] = d
	}

	d.Configs = append(d.Configs, v.Config.String())
	r.dirs[filepath.Dir(symbol.File)] = true

	refs, err := analysis.NewSymbolResolver(v.Workspace, e.logger).FindReferencesIndexed(symbol, r.indexOf(v))
	if err != nil {
		return fmt.Errorf("visit: %w", err)
	}

	for _, ref := range refs {
		r.refs[symbolRefSite{ref.File, ref.Line, ref.Column}] = ref
		r.dirs[filepath.Dir(ref.File)] = true
	}

	return nil
}

// lookup finds the symbol in ws: in the requested package, in the package the
// first declaring view settled on, or else in the first package in path order.
// It returns the key of that package.
func (r *symbolRun) lookup(ws *types.Workspace) (*types.Symbol, string) {
	if r.pkg != "" {
		path := types.ResolvePackagePath(ws, r.pkg)
		return symbolIn(ws.Packages[path], r.name), path
	}

	for _, path := range slices.Sorted(maps.Keys(ws.Packages)) {
		if r.pkgPath != "" && path != r.pkgPath {
			continue
		}

		s := symbolIn(ws.Packages[path], r.name)
		if s != nil {
			return s, path
		}
	}

	return nil, ""
}

func symbolIn(pkg *types.Package, name string) *types.Symbol {
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	return pkg.Symbols.FindSymbol(name)
}
