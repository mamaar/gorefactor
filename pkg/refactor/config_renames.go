package refactor

import (
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// renameStrategy is what a rename supplies to planRenameView.
type renameStrategy interface {
	// planWorkspace plans the requested renames in ws and those of the linked
	// keys it declares. It returns the keys of the symbols renamed, and a
	// SymbolNotFound error when ws has nothing to rename.
	planWorkspace(ws *types.Workspace, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error)
	// oldName is the name the rename replaces, as written at use sites.
	oldName() string
}

// planRenameView plans a rename in v. The view is type-checked so coverage
// reporting knows which of its files it left out.
func planRenameView(c *viewIndexes, s renameStrategy, v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	c.indexOf(v)
	ws := v.Workspace
	declared := declaredSymbolKeys(ws)
	renamed, plan, err := s.planWorkspace(ws, linked)
	if err != nil {
		if !isSymbolNotFound(err) {
			return configResult{}, err
		}
		c.planned(v, nil)
		return configResult{absent: err, declared: declared}, nil
	}
	plan = withoutFiles(plan, v.ExcludedTestFiles())
	res := configResult{plan: plan, changed: renamed, reach: reachOf(plan, []string{s.oldName()}), declared: declared}
	c.planned(v, plan)
	return res, nil
}

// withoutFiles returns plan without the changes to the given files. A rename
// found by name can touch a test file the view could not type-check; coverage
// reporting lists such a file for the user to fix by hand.
func withoutFiles(plan *types.RefactoringPlan, skip map[string]bool) *types.RefactoringPlan {
	if len(skip) == 0 {
		return plan
	}
	skipped := pathSet(skip)
	plan.Changes = slices.DeleteFunc(plan.Changes, skipped.hasChange)
	plan.AffectedFiles = slices.DeleteFunc(plan.AffectedFiles, skipped.has)
	return plan
}

type pathSet map[string]bool

func (s pathSet) has(path string) bool { return s[path] }

func (s pathSet) hasChange(c types.Change) bool { return s[c.File] }

// quietResolver returns a resolver for ws that does not log.
func quietResolver(ws *types.Workspace) *analysis.SymbolResolver {
	return analysis.NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// symbolKeyOf returns the key of a symbol from a package symbol table. It
// reports false for a method or field whose owning type is unknown.
func symbolKeyOf(sym *types.Symbol) (types.SymbolKey, bool) {
	switch sym.Kind {
	case types.MethodSymbol, types.StructFieldSymbol:
		if sym.Parent == nil {
			return types.SymbolKey{}, false
		}
		return types.SymbolKey{PkgPath: sym.Package, Recv: sym.Parent.Name, Name: sym.Name}, true
	}
	return types.SymbolKey{PkgPath: sym.Package, Name: sym.Name}, true
}

// declaredSymbolKeys returns the keys of every symbol the symbol tables of ws hold.
func declaredSymbolKeys(ws *types.Workspace) types.SymbolKeySet {
	keys := make(types.SymbolKeySet)
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}
		for _, table := range []map[string]*types.Symbol{pkg.Symbols.Functions, pkg.Symbols.Types, pkg.Symbols.Variables, pkg.Symbols.Constants} {
			for _, sym := range table {
				addSymbolKey(keys, sym)
			}
		}
		for _, group := range []map[string][]*types.Symbol{pkg.Symbols.Methods, pkg.Symbols.Fields} {
			for _, syms := range group {
				for _, sym := range syms {
					addSymbolKey(keys, sym)
				}
			}
		}
	}
	return keys
}

func addSymbolKey(keys types.SymbolKeySet, sym *types.Symbol) {
	if key, ok := symbolKeyOf(sym); ok {
		keys[key] = struct{}{}
	}
}

// symbolForKey returns the symbol of ws that key names, or nil.
func symbolForKey(ws *types.Workspace, key types.SymbolKey) *types.Symbol {
	pkg := resolveSymbolPackage(ws, &types.Symbol{Package: key.PkgPath})
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}
	table := pkg.Symbols
	if key.Recv == "" {
		for _, group := range []map[string]*types.Symbol{table.Functions, table.Types, table.Variables, table.Constants} {
			if sym := group[key.Name]; sym != nil {
				return sym
			}
		}
		return nil
	}
	for _, group := range [][]*types.Symbol{table.Methods[key.Recv], table.Fields[key.Recv]} {
		for _, sym := range group {
			if sym.Name == key.Name {
				return sym
			}
		}
	}
	return nil
}

// withLinkedSymbols appends the symbols of ws named by linked keys that the
// symbols do not cover yet, in key order.
func withLinkedSymbols(ws *types.Workspace, symbols []*types.Symbol, linked types.SymbolKeySet) []*types.Symbol {
	have := keysOf(symbols)
	var keys []types.SymbolKey
	for key := range linked {
		if !have.Has(key) {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, compareKeys)
	for _, key := range keys {
		if sym := symbolForKey(ws, key); sym != nil {
			symbols = append(symbols, sym)
		}
	}
	return symbols
}

func compareKeys(a, b types.SymbolKey) int { return strings.Compare(a.String(), b.String()) }

// keysOf returns the keys of the symbols that have one.
func keysOf(symbols []*types.Symbol) types.SymbolKeySet {
	keys := make(types.SymbolKeySet)
	for _, sym := range symbols {
		addSymbolKey(keys, sym)
	}
	return keys
}
