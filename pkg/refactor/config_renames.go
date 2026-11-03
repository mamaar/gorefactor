package refactor

import (
	"fmt"
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
	planWorkspace(ws *types.Workspace, idx *analysis.ReferenceIndex, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error)
	// oldName is the name the rename replaces, as written at use sites.
	oldName() string
}

// renameRefuser is a renameStrategy that refuses a view whose type errors hide uses.
type renameRefuser interface {
	// refuseIn returns the error that stops the rename of the keys in v. It
	// reads v's type information, so it runs before v is dropped.
	refuseIn(v *analysis.View, renamed types.SymbolKeySet) error
}

// planRenameView plans a rename in v. The view is type-checked so coverage
// reporting knows which of its files it left out, and so s can use idx.
func planRenameView(c *viewIndexes, s renameStrategy, v *analysis.View, linked types.SymbolKeySet) (configResult, error) {
	idx := c.indexOf(v)
	ws := v.Workspace
	declared := declaredSymbolKeys(ws)
	renamed, plan, err := s.planWorkspace(ws, idx, linked)
	if err != nil {
		if !isSymbolNotFound(err) {
			return configResult{}, err
		}
		c.planned(v, nil)
		return configResult{absent: err, declared: declared}, nil
	}
	plan = withoutFiles(plan, v.ExcludedTestFiles())
	r, ok := s.(renameRefuser)
	if ok {
		err := r.refuseIn(v, renamed)
		if err != nil {
			return configResult{}, err
		}
	}
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
	key, ok := symbolKeyOf(sym)
	if ok {
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
			sym := group[key.Name]
			if sym != nil {
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
	for _, key := range orderedKeys(linked) {
		if have.Has(key) {
			continue
		}
		sym := symbolForKey(ws, key)
		if sym != nil {
			symbols = append(symbols, sym)
		}
	}
	return symbols
}

func orderedKeys(set types.SymbolKeySet) []types.SymbolKey {
	keys := make([]types.SymbolKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareKeys)
	return keys
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

// memberRename is a rename operation on one method or field.
type memberRename interface {
	// validateIn validates the operation in ws and returns the keys of the
	// members it renames. A target ws lacks is a SymbolNotFound error.
	validateIn(ws *types.Workspace) (types.SymbolKeySet, error)
	Execute(ws *types.Workspace) (*types.RefactoringPlan, error)
}

// memberRenames is what a method or field rename supplies to planMemberRenames.
type memberRenames interface {
	// label names the operation in errors, e.g. "rename method".
	label() string
	requestedOp(idx *analysis.ReferenceIndex) memberRename
	// linkedOp returns the operation renaming the member key names, or nil
	// when key is not one this rename changes or ws cannot host it.
	linkedOp(ws *types.Workspace, idx *analysis.ReferenceIndex, key types.SymbolKey) memberRename
}

// planMemberRenames plans the requested rename in ws and one for each linked
// key ws declares, and returns the keys they rename. When ws has no target it
// returns the requested operation's not-found error.
func planMemberRenames(ws *types.Workspace, idx *analysis.ReferenceIndex, s memberRenames, linked types.SymbolKeySet) (types.SymbolKeySet, *types.RefactoringPlan, error) {
	run := &memberRun{label: s.label(), renamed: make(types.SymbolKeySet)}
	err := run.validate(ws, s.requestedOp(idx), true)
	if err != nil {
		return nil, nil, err
	}
	for _, key := range orderedKeys(linked) {
		if run.renamed.Has(key) {
			continue
		}
		op := s.linkedOp(ws, idx, key)
		if op != nil {
			err := run.validate(ws, op, false)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if len(run.ops) == 0 {
		return nil, nil, fmt.Errorf("%s operation validation failed: %w", run.label, run.notFound)
	}
	var acc planAccum
	for _, op := range run.ops {
		plan, err := op.Execute(ws)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to generate %s plan: %w", run.label, err)
		}
		acc.add(plan)
	}
	return run.renamed, acc.plan(), nil
}

// memberRun collects the validated operations of one view.
type memberRun struct {
	label    string
	ops      []memberRename
	renamed  types.SymbolKeySet
	notFound error // of the requested operation
}

// validate keeps op when it validates in ws. A missing target is not an error
// here; only the requested operation's is reported, when no operation is kept.
func (r *memberRun) validate(ws *types.Workspace, op memberRename, requested bool) error {
	keys, err := op.validateIn(ws)
	if isSymbolNotFound(err) {
		if requested {
			r.notFound = err
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s operation validation failed: %w", r.label, err)
	}
	r.ops = append(r.ops, op)
	r.renamed.AddAll(keys)
	return nil
}
