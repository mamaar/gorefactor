package refactor

import (
	"fmt"
	gotypes "go/types"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// movedUses is what one moved declaration refers to outside itself.
type movedUses struct {
	// imports are the import paths of the packages it names through a qualifier.
	imports map[string]bool
	// leftBehind are the file offsets of its names of exported package-level
	// symbols that stay in the source package; each needs the source
	// package's qualifier in the target.
	leftBehind []int
}

// scanMovedUses finds, through go/types, what each moving declaration uses
// besides itself.
func scanMovedUses(ws *types.Workspace, moving []*movingSymbol) map[*movingSymbol]*movedUses {
	uses := make(map[*movingSymbol]*movedUses, len(moving))
	for _, m := range moving {
		uses[m] = &movedUses{imports: make(map[string]bool)}
	}
	done := make(map[*types.Package]bool)
	for _, m := range moving {
		pkg := m.pkg
		if done[pkg] || pkg.TypesInfo == nil || pkg.TypesPkg == nil {
			continue
		}
		done[pkg] = true
		for ident, obj := range pkg.TypesInfo.Uses {
			at := ws.FileSet.Position(ident.Pos())
			user := movingAt(moving, at.Filename, at.Offset)
			if user == nil {
				continue
			}
			switch o := obj.(type) {
			case *gotypes.PkgName:
				uses[user].imports[o.Imported().Path()] = true
			default:
				if obj.Pkg() != pkg.TypesPkg || !obj.Exported() || obj.Parent() != pkg.TypesPkg.Scope() {
					continue
				}
				def := ws.FileSet.Position(obj.Pos())
				if movingAt(moving, def.Filename, def.Offset) == nil {
					uses[user].leftBehind = append(uses[user].leftBehind, at.Offset)
				}
			}
		}
	}
	return uses
}

// importCycle returns the import cycle, as the packages from target around to
// target, that the move closes when target comes to import imports and every
// package in importers comes to import target; nil when it closes none.
func importCycle(graph map[string][]string, target string, imports []string, importers []string) []string {
	parent := map[string]string{target: ""}
	var queue []string
	// closing returns the cycle that runs from target through last and back.
	closing := func(last string) []string {
		var rev []string
		for p := last; p != target; p = parent[p] {
			rev = append(rev, p)
		}
		slices.Reverse(rev)
		return append(append([]string{target}, rev...), target)
	}
	visit := func(from string, tos []string) []string {
		for _, to := range tos {
			if to == target {
				return closing(from)
			}
			if _, seen := parent[to]; seen {
				continue
			}
			parent[to] = from
			if slices.Contains(importers, to) {
				return closing(to)
			}
			queue = append(queue, to)
		}
		return nil
	}
	if path := visit(target, mergeSorted(graph[target], imports)); path != nil {
		return path
	}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if path := visit(next, graph[next]); path != nil {
			return path
		}
	}
	return nil
}

func mergeSorted(a, b []string) []string {
	merged := slices.Concat(a, b)
	sort.Strings(merged)
	return slices.Compact(merged)
}

// importsAndImporters returns the packages the target imports for the moved
// code and the packages that import the target for the references left
// behind, each with the moving symbol that brings it about.
func importsAndImporters(ws *types.Workspace, moving []*movingSymbol, uses map[*movingSymbol]*movedUses, surviving []*types.Reference, target string) (imports, importers map[string]*movingSymbol) {
	imports = make(map[string]*movingSymbol)
	for _, m := range moving {
		for importPath := range uses[m].imports {
			if to := ws.ImportToPath[importPath]; to != "" && to != target && imports[to] == nil {
				imports[to] = m
			}
		}
		if len(uses[m].leftBehind) > 0 && imports[m.pkg.Path] == nil {
			imports[m.pkg.Path] = m
		}
	}
	importers = make(map[string]*movingSymbol)
	for _, ref := range surviving {
		pkg := findPackageForFile(ws, ref.File)
		file := findParsedFile(ws, ref.File)
		if pkg == nil || file == nil || file.AST == nil || pkg.Path == target || strings.HasSuffix(file.AST.Name.Name, "_test") {
			continue
		}
		if i := slices.IndexFunc(moving, func(m *movingSymbol) bool { return m.sym == ref.Symbol }); i >= 0 && importers[pkg.Path] == nil {
			importers[pkg.Path] = moving[i]
		}
	}
	return imports, importers
}

// checkImportCycles refuses the move when the imports it adds close a cycle,
// blaming the symbol whose moved code brings the target its import or, when
// the target already imports the way round, the symbol a referrer uses.
func (op *MoveSymbolOperation) checkImportCycles(ws *types.Workspace, moving []*movingSymbol, uses map[*movingSymbol]*movedUses, surviving []*types.Reference, target string) error {
	imports, importers := importsAndImporters(ws, moving, uses, surviving, target)
	op.targetImports, op.importers = slices.Sorted(maps.Keys(imports)), slices.Sorted(maps.Keys(importers))
	cycle := importCycle(analysis.ImportGraph(ws).Union(), target, op.targetImports, op.importers)
	if cycle == nil {
		return nil
	}
	blamed := importers[cycle[len(cycle)-2]]
	if m := imports[cycle[1]]; m != nil {
		blamed = m
	}
	names := make([]string, len(cycle))
	for i, p := range cycle {
		names[i] = packagePathToImportPath(ws, p)
	}
	return &groupMemberError{index: blamed.owner, err: &types.RefactorError{
		Type: types.CyclicDependency,
		Message: fmt.Sprintf("moving %s to %s would create an import cycle: %s",
			blamed.sym.Name, names[0], strings.Join(names, " -> ")),
	}}
}
