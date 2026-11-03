package refactor

import (
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// MoveByDependenciesOperation implements moving symbols based on dependency analysis
type MoveByDependenciesOperation struct {
	Request types.MoveByDependenciesRequest
}

// sharedSymbolCandidate holds a symbol and the packages that reference it externally
type sharedSymbolCandidate struct {
	symbol              *types.Symbol
	pkg                 *types.Package
	referencingPackages []string
	targetFSPath        string
	targetImportPath    string
}

func (op *MoveByDependenciesOperation) Type() types.OperationType {
	return types.MoveByDependenciesOperation
}

func (op *MoveByDependenciesOperation) Description() string {
	return fmt.Sprintf("Move symbols by dependencies analysis in workspace %s", op.Request.Workspace)
}

func (op *MoveByDependenciesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}

	return nil
}

// Execute plans the dependency analysis. Moving symbols needs an engine and is
// done by DefaultEngine.MoveByDependencies.
func (op *MoveByDependenciesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	if !op.Request.AnalyzeOnly {
		return nil, fmt.Errorf("moving shared symbols is planned by the engine, not by Execute")
	}

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	candidates := op.identifySharedSymbols(ws)

	packageCoupling := packageCouplingOf(ws, declaredSymbolCounts(ws))

	suggestedMoves := make([]types.SuggestedMove, 0, len(candidates))
	affectedPackagesSet := make(map[string]bool)
	var affectedSymbols []*types.Symbol

	for _, c := range candidates {
		from := c.pkg.ImportPath
		if from == "" {
			from = c.pkg.Path
		}

		to := c.targetImportPath
		if to == "" {
			to = c.targetFSPath
		}

		suggestedMoves = append(suggestedMoves, types.SuggestedMove{
			Symbol:              c.symbol.Name,
			FromPackage:         from,
			ToPackage:           to,
			Reason:              fmt.Sprintf("referenced by %d packages: %s", len(c.referencingPackages), strings.Join(c.referencingPackages, ", ")),
			ReferencingPackages: c.referencingPackages,
		})
		affectedPackagesSet[from] = true

		affectedSymbols = append(affectedSymbols, c.symbol)
	}

	var affectedPackages []string
	for pkg := range affectedPackagesSet {
		affectedPackages = append(affectedPackages, pkg)
	}

	sort.Strings(affectedPackages)

	plan.Impact = &types.ImpactAnalysis{
		SuggestedMoves:   suggestedMoves,
		PackageCoupling:  packageCoupling,
		AffectedPackages: affectedPackages,
		AffectedSymbols:  affectedSymbols,
	}

	return plan, nil
}

func (op *MoveByDependenciesOperation) identifySharedSymbols(ws *types.Workspace) []sharedSymbolCandidate {
	// Ensure dependencies are built
	if ws.Dependencies == nil {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		analyzer := analysis.NewDependencyAnalyzer(ws, logger)
		_ = analyzer.BuildDependencyGraph()
	}

	fileToPackage := buildFilePackageIndex(ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := analysis.NewSymbolResolver(ws, logger)
	idx := resolver.BuildReferenceIndex()

	targetFSPath := op.getTargetFSPath(ws)
	targetImportPath := op.getTargetImportPath(ws)

	var candidates []sharedSymbolCandidate

	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		if targetFSPath != "" && filepath.Clean(pkg.Path) == filepath.Clean(targetFSPath) {
			continue
		}

		if isKeepInternal(pkg, op.Request.KeepInternal) {
			continue
		}

		for _, sym := range getAllExportedSymbols(pkg) {
			refs, err := resolver.FindReferencesIndexed(sym, idx)
			if err != nil || len(refs) == 0 {
				continue
			}

			// Collect distinct external referencing package identifiers
			externalSet := make(map[string]bool)

			for _, ref := range refs {
				refPkg := fileToPackage[ref.File]
				if refPkg == nil || refPkg == pkg {
					continue
				}

				pkgID := refPkg.ImportPath
				if pkgID == "" {
					pkgID = refPkg.Path
				}

				externalSet[pkgID] = true
			}

			if len(externalSet) < 2 {
				continue
			}

			refPkgs := make([]string, 0, len(externalSet))
			for pkgID := range externalSet {
				refPkgs = append(refPkgs, pkgID)
			}

			sort.Strings(refPkgs)
			candidates = append(candidates, sharedSymbolCandidate{
				symbol:              sym,
				pkg:                 pkg,
				referencingPackages: refPkgs,
				targetFSPath:        targetFSPath,
				targetImportPath:    targetImportPath,
			})
		}
	}

	slices.SortFunc(candidates, func(a, b sharedSymbolCandidate) int {
		return cmp.Or(strings.Compare(a.pkg.Path, b.pkg.Path), strings.Compare(a.symbol.Name, b.symbol.Name))
	})

	return candidates
}

func (op *MoveByDependenciesOperation) getTargetFSPath(ws *types.Workspace) string {
	if op.Request.MoveSharedTo != "" {
		return filepath.Join(ws.RootPath, op.Request.MoveSharedTo)
	}

	return ""
}

func (op *MoveByDependenciesOperation) getTargetImportPath(ws *types.Workspace) string {
	if op.Request.MoveSharedTo != "" && ws.Module != nil {
		return ws.Module.Path + "/" + op.Request.MoveSharedTo
	}

	return ""
}

// buildFilePackageIndex returns a map of absolute file path → Package for all files in the workspace.
func buildFilePackageIndex(ws *types.Workspace) map[string]*types.Package {
	index := make(map[string]*types.Package)

	for _, pkg := range ws.Packages {
		for _, f := range pkg.Files {
			index[f.Path] = pkg
		}

		for _, f := range pkg.TestFiles {
			index[f.Path] = pkg
		}
	}

	return index
}

// getAllExportedSymbols returns all exported symbols from a package's symbol table.
func getAllExportedSymbols(pkg *types.Package) []*types.Symbol {
	if pkg.Symbols == nil {
		return nil
	}
	var result []*types.Symbol

	for _, sym := range pkg.Symbols.Functions {
		if sym.Exported {
			result = append(result, sym)
		}
	}

	for _, sym := range pkg.Symbols.Types {
		if sym.Exported {
			result = append(result, sym)
		}
	}

	for _, sym := range pkg.Symbols.Variables {
		if sym.Exported {
			result = append(result, sym)
		}
	}

	for _, sym := range pkg.Symbols.Constants {
		if sym.Exported {
			result = append(result, sym)
		}
	}

	return result
}

// isKeepInternal returns true if the package path or import path has any keepInternal entry as a prefix.
func isKeepInternal(pkg *types.Package, keepInternal []string) bool {
	for _, prefix := range keepInternal {
		if strings.HasPrefix(pkg.Path, prefix) || strings.HasPrefix(pkg.ImportPath, prefix) {
			return true
		}
	}

	return false
}

// computeIncomingDeps returns a map of filesystem path → number of packages that import it.
func computeIncomingDeps(ws *types.Workspace) map[string]int {
	result := make(map[string]int)
	if ws.Dependencies == nil {
		return result
	}

	for _, importPaths := range ws.Dependencies.PackageImports {
		for _, importPath := range importPaths {
			toFSPath, ok := ws.ImportToPath[importPath]
			if ok {
				result[toFSPath]++
			}
		}
	}

	return result
}

// OrganizeByLayersOperation implements organizing packages by architectural layers
type OrganizeByLayersOperation struct {
	Request types.OrganizeByLayersRequest
}

func (op *OrganizeByLayersOperation) Type() types.OperationType {
	return types.OrganizeByLayersOperation
}

func (op *OrganizeByLayersOperation) Description() string {
	return fmt.Sprintf("Organize packages by architectural layers in workspace %s", op.Request.Workspace)
}

func (op *OrganizeByLayersOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}

	return nil
}

func (op *OrganizeByLayersOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Analyze packages and organize by layers
	if op.Request.ReorderImports {
		for _, pkg := range ws.Packages {
			for _, file := range pkg.Files {
				changes := op.reorderImportsByLayers(ws, file)

				plan.Changes = append(plan.Changes, changes...)
				if len(changes) > 0 {
					plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
				}
			}
		}
	}

	// Generate layer organization report
	reportFile := filepath.Join(op.Request.Workspace, "layer_organization.md")
	content := op.generateLayerReport(ws)

	plan.Changes = append(plan.Changes, types.Change{
		File:        reportFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     content,
		Description: "Generate layer organization report",
	})

	plan.AffectedFiles = append(plan.AffectedFiles, reportFile)

	return plan, nil
}

// reorderImportsByLayers classifies each import into a tier and returns a Change
// replacing the import block with the sorted version when the order would change.
//
// Tiers (ascending order): 0 stdlib, 1 external, 2 infrastructure, 3 domain, 4 application.
func (op *OrganizeByLayersOperation) reorderImportsByLayers(ws *types.Workspace, file *types.File) []types.Change {
	if file.AST == nil || len(file.AST.Imports) == 0 || ws.FileSet == nil {
		return nil
	}

	type impEntry struct {
		tier       int
		importPath string
		text       string // original source text for this spec (with surrounding whitespace)
		start      int    // byte offset in file
		end        int    // byte offset in file (exclusive)
	}

	content := file.OriginalContent
	var entries []impEntry

	for _, imp := range file.AST.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		tier := op.classifyImportTier(ws, impPath)

		startPos := ws.FileSet.Position(imp.Pos())
		endPos := ws.FileSet.Position(imp.End())
		start := startPos.Offset
		end := endPos.Offset

		// Expand start to include leading tab
		for start > 0 && content[start-1] == '\t' {
			start--
		}
		// Expand end to include trailing newline
		if end < len(content) && content[end] == '\n' {
			end++
		}

		if start < 0 || end > len(content) || start >= end {
			return nil // safety: cannot determine positions
		}

		entries = append(entries, impEntry{
			tier:       tier,
			importPath: impPath,
			text:       string(content[start:end]),
			start:      start,
			end:        end,
		})
	}

	if len(entries) == 0 {
		return nil
	}

	// Sort by tier then import path
	sorted := make([]impEntry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].tier != sorted[j].tier {
			return sorted[i].tier < sorted[j].tier
		}

		return sorted[i].importPath < sorted[j].importPath
	})

	alreadySorted := true

	for i := range entries {
		if entries[i].importPath != sorted[i].importPath {
			alreadySorted = false
			break
		}
	}

	if alreadySorted {
		return nil
	}

	blockStart := entries[0].start
	blockEnd := entries[len(entries)-1].end

	var newBlock strings.Builder
	for _, e := range sorted {
		newBlock.WriteString(e.text)
	}

	oldBlock := string(content[blockStart:blockEnd])

	newBlockStr := newBlock.String()
	if oldBlock == newBlockStr {
		return nil
	}

	return []types.Change{{
		File:        file.Path,
		Start:       blockStart,
		End:         blockEnd,
		OldText:     oldBlock,
		NewText:     newBlockStr,
		Description: "Reorder imports by architectural layers",
	}}
}

// classifyImportTier maps an import path to a tier number for ordering.
func (op *OrganizeByLayersOperation) classifyImportTier(ws *types.Workspace, importPath string) int {
	// stdlib: no dots in the path (e.g. "fmt", "net/http")
	if !strings.Contains(importPath, ".") {
		return 0
	}
	// Project-local packages: classify by layer
	if ws.Module != nil && strings.HasPrefix(importPath, ws.Module.Path) {
		rel := strings.TrimPrefix(importPath, ws.Module.Path+"/")
		switch op.classifyPackage(rel) {
		case "Infrastructure":
			return 2
		case "Domain":
			return 3
		case "Application":
			return 4
		}

		return 1 // project-local but unclassified → treat as external tier
	}
	// External (third-party)
	return 1
}

func (op *OrganizeByLayersOperation) generateLayerReport(ws *types.Workspace) string {
	var report strings.Builder

	report.WriteString("# Layer Organization Report\n\n")
	report.WriteString("## Architectural Layers\n\n")

	if op.Request.DomainLayer != "" {
		fmt.Fprintf(&report, "**Domain Layer**: %s\n", op.Request.DomainLayer)
	}

	if op.Request.InfrastructureLayer != "" {
		fmt.Fprintf(&report, "**Infrastructure Layer**: %s\n", op.Request.InfrastructureLayer)
	}

	if op.Request.ApplicationLayer != "" {
		fmt.Fprintf(&report, "**Application Layer**: %s\n", op.Request.ApplicationLayer)
	}

	report.WriteString("\n## Package Classification\n\n")

	for pkgPath := range ws.Packages {
		layer := op.classifyPackage(pkgPath)
		fmt.Fprintf(&report, "- `%s` → %s Layer\n", pkgPath, layer)
	}

	return report.String()
}

func (op *OrganizeByLayersOperation) classifyPackage(packagePath string) string {
	if strings.HasPrefix(packagePath, op.Request.DomainLayer) {
		return "Domain"
	}

	if strings.HasPrefix(packagePath, op.Request.InfrastructureLayer) {
		return "Infrastructure"
	}

	if strings.HasPrefix(packagePath, op.Request.ApplicationLayer) {
		return "Application"
	}

	return "Unclassified"
}

func importPathOf(ws *types.Workspace, fsPath string) string {
	pkg := ws.Packages[fsPath]
	if pkg != nil && pkg.ImportPath != "" {
		return pkg.ImportPath
	}

	rel, err := filepath.Rel(ws.RootPath, fsPath)
	if err == nil {
		return filepath.ToSlash(rel)
	}

	return fsPath
}

// AnalyzeDependenciesOperation implements analyzing dependency flow
type AnalyzeDependenciesOperation struct {
	Request types.AnalyzeDependenciesRequest
}

func (op *AnalyzeDependenciesOperation) Type() types.OperationType {
	return types.AnalyzeDependenciesOperation
}

func (op *AnalyzeDependenciesOperation) Description() string {
	return fmt.Sprintf("Analyze dependencies in workspace %s", op.Request.Workspace)
}

func (op *AnalyzeDependenciesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}

	return nil
}

// Execute computes the dependency analysis into plan.Impact. It is read-only
// and produces no changes.
func (op *AnalyzeDependenciesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	ensureDependencyGraph(ws)

	impact := &types.ImpactAnalysis{PackageCoupling: packageCouplingOf(ws, declaredSymbolCounts(ws))}
	if op.Request.DetectBackwardsDeps {
		impact.BackwardsDependencies = detectBackwardsDependencies(ws)
	}

	if op.Request.SuggestMoves {
		impact.SuggestedMoves = op.generateSuggestedMoves(ws)
	}

	plan.Impact = impact

	return plan, nil
}

// ensureDependencyGraph builds ws.Dependencies when it is missing.
func ensureDependencyGraph(ws *types.Workspace) {
	if ws.Dependencies != nil {
		return
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_ = analysis.NewDependencyAnalyzer(ws, logger).BuildDependencyGraph()
}

// packageCouplingOf returns import counts and symbol counts per package, keyed
// by filesystem path.
func packageCouplingOf(ws *types.Workspace, symbolCounts map[string]int) map[string]types.PackageCouplingInfo {
	incoming := computeIncomingDeps(ws)

	coupling := make(map[string]types.PackageCouplingInfo, len(ws.Packages))
	for fsPath := range ws.Packages {
		info := types.PackageCouplingInfo{IncomingDeps: incoming[fsPath], SymbolCount: symbolCounts[fsPath]}
		if ws.Dependencies != nil {
			info.OutgoingDeps = len(ws.Dependencies.PackageImports[fsPath])
		}

		coupling[fsPath] = info
	}

	return coupling
}

// packageTier returns a numeric tier for an import path based on naming conventions:
//
//	-1 = unclassified, 0 = inner (domain/core/model), 1 = middle (service/usecase),
//	2 = outer (handler/http/infra/repository/…)
func packageTier(importPath string) int {
	lower := strings.ToLower(importPath)

	outerKeywords := []string{"handler", "controller", "http", "api", "grpc",
		"infra", "adapter", "repository", "persistence", "delivery", "transport"}
	for _, kw := range outerKeywords {
		if strings.Contains(lower, kw) {
			return 2
		}
	}

	middleKeywords := []string{"service", "usecase", "application", "/app/"}
	for _, kw := range middleKeywords {
		if strings.Contains(lower, kw) {
			return 1
		}
	}

	innerKeywords := []string{"domain", "core", "model", "entity"}
	for _, kw := range innerKeywords {
		if strings.Contains(lower, kw) {
			return 0
		}
	}

	return -1
}

func detectBackwardsDependencies(ws *types.Workspace) []types.BackwardDependency {
	deps := []types.BackwardDependency{}
	if ws.Dependencies == nil {
		return deps
	}

	for fromFSPath, importPaths := range ws.Dependencies.PackageImports {
		fromPkg, ok := ws.Packages[fromFSPath]
		if !ok {
			continue
		}

		fromTier := packageTier(fromPkg.ImportPath)
		if fromTier < 0 {
			continue
		}

		for _, importPath := range importPaths {
			toTier := packageTier(importPath)
			if toTier <= fromTier {
				continue
			}

			from := fromPkg.ImportPath
			if from == "" {
				from = fromPkg.Path
			}

			deps = append(deps, types.BackwardDependency{
				From:   from,
				To:     importPath,
				Reason: fmt.Sprintf("inner layer (tier %d) imports outer layer (tier %d)", fromTier, toTier),
			})
		}
	}

	return deps
}

func (op *AnalyzeDependenciesOperation) generateSuggestedMoves(ws *types.Workspace) []types.SuggestedMove {
	fileToPackage := buildFilePackageIndex(ws)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := analysis.NewSymbolResolver(ws, logger)
	idx := resolver.BuildReferenceIndex()

	moves := []types.SuggestedMove{}

	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		for _, sym := range getAllExportedSymbols(pkg) {
			refs, err := resolver.FindReferencesIndexed(sym, idx)
			if err != nil || len(refs) == 0 {
				continue
			}

			externalSet := make(map[string]bool)

			for _, ref := range refs {
				refPkg := fileToPackage[ref.File]
				if refPkg == nil || refPkg == pkg {
					continue
				}

				pkgID := refPkg.ImportPath
				if pkgID == "" {
					pkgID = refPkg.Path
				}

				externalSet[pkgID] = true
			}

			if len(externalSet) < 2 {
				continue
			}

			from := pkg.ImportPath
			if from == "" {
				from = pkg.Path
			}

			referrers := slices.Sorted(maps.Keys(externalSet))
			moves = append(moves, types.SuggestedMove{
				Symbol:              sym.Name,
				FromPackage:         from,
				ToPackage:           "pkg/shared",
				Reason:              fmt.Sprintf("referenced by %d packages: %s", len(referrers), strings.Join(referrers, ", ")),
				ReferencingPackages: referrers,
			})
		}
	}

	sort.Slice(moves, func(i, j int) bool {
		if moves[i].FromPackage != moves[j].FromPackage {
			return moves[i].FromPackage < moves[j].FromPackage
		}

		return moves[i].Symbol < moves[j].Symbol
	})

	return moves
}
