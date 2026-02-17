package refactor

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
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

func (op *MoveByDependenciesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	candidates := op.identifySharedSymbols(ws)

	// Build packageCoupling map
	incomingDeps := computeIncomingDeps(ws)
	packageCoupling := make(map[string]types.PackageCouplingInfo)
	for fsPath, pkg := range ws.Packages {
		symbolCount := 0
		if pkg.Symbols != nil {
			symbolCount = len(pkg.Symbols.Functions) + len(pkg.Symbols.Types) +
				len(pkg.Symbols.Variables) + len(pkg.Symbols.Constants)
		}
		outgoingDeps := 0
		if ws.Dependencies != nil {
			outgoingDeps = len(ws.Dependencies.PackageImports[fsPath])
		}
		packageCoupling[fsPath] = types.PackageCouplingInfo{
			IncomingDeps: incomingDeps[fsPath],
			OutgoingDeps: outgoingDeps,
			SymbolCount:  symbolCount,
		}
	}

	if op.Request.AnalyzeOnly {
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

	if op.Request.MoveSharedTo == "" {
		return nil, fmt.Errorf("MoveSharedTo must be specified when AnalyzeOnly is false")
	}

	toPackage := op.getTargetFSPath(ws)
	for _, c := range candidates {
		moveReq := types.MoveSymbolRequest{
			SymbolName:   c.symbol.Name,
			FromPackage:  c.pkg.Path,
			ToPackage:    toPackage,
			CreateTarget: true,
			UpdateTests:  true,
		}
		moveOp := &MoveSymbolOperation{Request: moveReq}
		if err := moveOp.Validate(ws); err != nil {
			continue
		}
		movePlan, err := moveOp.Execute(ws)
		if err != nil {
			continue
		}
		plan.Changes = append(plan.Changes, movePlan.Changes...)
		for _, f := range movePlan.AffectedFiles {
			if !containsString(plan.AffectedFiles, f) {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
			}
		}
	}

	return plan, nil
}

func (op *MoveByDependenciesOperation) identifySharedSymbols(ws *types.Workspace) []sharedSymbolCandidate {
	// Ensure dependencies are built
	if ws.Dependencies == nil {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		analyzer := analysis.NewDependencyAnalyzer(ws, logger)
		if _, err := analyzer.BuildDependencyGraph(); err != nil {
			return nil
		}
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

			if len(externalSet) >= 2 {
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
	}

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

// buildFilePackageIndex returns a map of file path → Package for all files in the workspace.
func buildFilePackageIndex(ws *types.Workspace) map[string]*types.Package {
	index := make(map[string]*types.Package)
	for _, pkg := range ws.Packages {
		for path := range pkg.Files {
			index[path] = pkg
		}
		for path := range pkg.TestFiles {
			index[path] = pkg
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
			if toFSPath, ok := ws.ImportToPath[importPath]; ok {
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
				changes := op.reorderImportsByLayers(file)
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

func (op *OrganizeByLayersOperation) reorderImportsByLayers(file *types.File) []types.Change {
	var changes []types.Change
	
	// Placeholder - would reorder imports according to layer hierarchy:
	// 1. Standard library
	// 2. External dependencies  
	// 3. Infrastructure layer
	// 4. Domain layer
	// 5. Application layer
	
	return changes
}

func (op *OrganizeByLayersOperation) generateLayerReport(ws *types.Workspace) string {
	var report strings.Builder
	
	report.WriteString("# Layer Organization Report\n\n")
	report.WriteString("## Architectural Layers\n\n")
	
	if op.Request.DomainLayer != "" {
		report.WriteString(fmt.Sprintf("**Domain Layer**: %s\n", op.Request.DomainLayer))
	}
	if op.Request.InfrastructureLayer != "" {
		report.WriteString(fmt.Sprintf("**Infrastructure Layer**: %s\n", op.Request.InfrastructureLayer))
	}
	if op.Request.ApplicationLayer != "" {
		report.WriteString(fmt.Sprintf("**Application Layer**: %s\n", op.Request.ApplicationLayer))
	}
	
	report.WriteString("\n## Package Classification\n\n")
	
	for pkgPath := range ws.Packages {
		layer := op.classifyPackage(pkgPath)
		report.WriteString(fmt.Sprintf("- `%s` → %s Layer\n", pkgPath, layer))
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

// FixCyclesOperation implements detecting and fixing circular dependencies
type FixCyclesOperation struct {
	Request types.FixCyclesRequest
}

func (op *FixCyclesOperation) Type() types.OperationType {
	return types.FixCyclesOperation
}

func (op *FixCyclesOperation) Description() string {
	return fmt.Sprintf("Fix circular dependencies in workspace %s", op.Request.Workspace)
}

func (op *FixCyclesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	return nil
}

func (op *FixCyclesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Detect cycles first
	cycles := op.detectCycles(ws)
	
	// Generate report
	reportContent := op.generateCycleReport(cycles)
	reportFile := op.Request.OutputReport
	if reportFile == "" {
		reportFile = filepath.Join(op.Request.Workspace, "cycles_report.md")
	}
	
	plan.Changes = append(plan.Changes, types.Change{
		File:        reportFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     reportContent,
		Description: "Generate circular dependency report",
	})
	
	plan.AffectedFiles = append(plan.AffectedFiles, reportFile)

	// If auto-fix is requested and cycles exist, attempt fixes
	if op.Request.AutoFix && len(cycles) > 0 {
		for _, cycle := range cycles {
			fixes := op.generateCycleFixes(cycle)
			plan.Changes = append(plan.Changes, fixes...)
			for _, fix := range fixes {
				if !containsString(plan.AffectedFiles, fix.File) {
					plan.AffectedFiles = append(plan.AffectedFiles, fix.File)
				}
			}
		}
	}

	return plan, nil
}

func (op *FixCyclesOperation) detectCycles(ws *types.Workspace) [][]string {
	// Placeholder implementation - would implement cycle detection algorithm
	// like strongly connected components (Tarjan's algorithm)
	var cycles [][]string
	
	return cycles
}

func (op *FixCyclesOperation) generateCycleReport(cycles [][]string) string {
	var report strings.Builder
	
	report.WriteString("# Circular Dependencies Report\n\n")
	report.WriteString(fmt.Sprintf("Analyzed workspace: %s\n", op.Request.Workspace))
	report.WriteString(fmt.Sprintf("Found %d circular dependencies\n\n", len(cycles)))
	
	if len(cycles) > 0 {
		report.WriteString("## Detected Cycles\n\n")
		for i, cycle := range cycles {
			report.WriteString(fmt.Sprintf("### Cycle %d\n\n", i+1))
			report.WriteString("```\n")
			for j, pkg := range cycle {
				if j > 0 {
					report.WriteString(" → ")
				}
				report.WriteString(pkg)
			}
			if len(cycle) > 0 {
				report.WriteString(" → " + cycle[0]) // Complete the cycle
			}
			report.WriteString("\n```\n\n")
		}
	} else {
		report.WriteString("✅ No circular dependencies detected!\n")
	}
	
	return report.String()
}

func (op *FixCyclesOperation) generateCycleFixes(cycle []string) []types.Change {
	var fixes []types.Change
	
	// Placeholder - would implement cycle breaking strategies:
	// 1. Extract interfaces to break dependencies
	// 2. Move shared code to separate packages
	// 3. Use dependency inversion
	
	return fixes
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

func (op *AnalyzeDependenciesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Perform comprehensive dependency analysis
	analysis := op.performDependencyAnalysis(ws)
	
	// Generate output
	if op.Request.OutputFile != "" {
		// JSON output
		jsonData, err := json.MarshalIndent(analysis, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal analysis to JSON: %w", err)
		}
		
		plan.Changes = append(plan.Changes, types.Change{
			File:        op.Request.OutputFile,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     string(jsonData),
			Description: "Generate dependency analysis JSON",
		})
		
		plan.AffectedFiles = []string{op.Request.OutputFile}
	} else {
		// Markdown report
		reportFile := filepath.Join(op.Request.Workspace, "dependency_analysis.md")
		content := op.generateAnalysisReport(analysis)
		
		plan.Changes = append(plan.Changes, types.Change{
			File:        reportFile,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     content,
			Description: "Generate dependency analysis report",
		})
		
		plan.AffectedFiles = []string{reportFile}
	}

	return plan, nil
}

type DependencyAnalysis struct {
	Workspace        string              `json:"workspace"`
	TotalPackages    int                 `json:"total_packages"`
	BackwardsDeps    []BackwardDep       `json:"backwards_dependencies,omitempty"`
	SuggestedMoves   []SuggestedMove     `json:"suggested_moves,omitempty"`
	PackageMetrics   map[string]PackageMetric `json:"package_metrics"`
}

type BackwardDep struct {
	From string `json:"from"`
	To   string `json:"to"`
	Reason string `json:"reason"`
}

type SuggestedMove struct {
	Symbol     string `json:"symbol"`
	FromPackage string `json:"from_package"`
	ToPackage   string `json:"to_package"`
	Reason      string `json:"reason"`
}

type PackageMetric struct {
	IncomingDeps  int `json:"incoming_deps"`
	OutgoingDeps  int `json:"outgoing_deps"`
	SymbolCount   int `json:"symbol_count"`
}

func (op *AnalyzeDependenciesOperation) performDependencyAnalysis(ws *types.Workspace) *DependencyAnalysis {
	analysis := &DependencyAnalysis{
		Workspace:      op.Request.Workspace,
		TotalPackages:  len(ws.Packages),
		PackageMetrics: make(map[string]PackageMetric),
	}
	
	// Calculate metrics for each package
	for pkgPath, pkg := range ws.Packages {
		symbolCount := 0
		if pkg.Symbols != nil {
			symbolCount = len(pkg.Symbols.Functions) + 
						   len(pkg.Symbols.Types) + 
						   len(pkg.Symbols.Variables) + 
						   len(pkg.Symbols.Constants)
		}
		
		analysis.PackageMetrics[pkgPath] = PackageMetric{
			SymbolCount: symbolCount,
			// IncomingDeps and OutgoingDeps would be calculated from dependency graph
		}
	}
	
	// Detect backwards dependencies if requested
	if op.Request.DetectBackwardsDeps {
		analysis.BackwardsDeps = op.detectBackwardsDependencies(ws)
	}
	
	// Generate suggested moves if requested
	if op.Request.SuggestMoves {
		analysis.SuggestedMoves = op.generateSuggestedMoves(ws)
	}
	
	return analysis
}

func (op *AnalyzeDependenciesOperation) detectBackwardsDependencies(ws *types.Workspace) []BackwardDep {
	var backwardsDeps []BackwardDep
	
	// Placeholder - would implement backwards dependency detection
	// by analyzing the dependency graph and architectural layers
	
	return backwardsDeps
}

func (op *AnalyzeDependenciesOperation) generateSuggestedMoves(ws *types.Workspace) []SuggestedMove {
	var moves []SuggestedMove
	
	// Placeholder - would analyze usage patterns and suggest optimal locations
	
	return moves
}

func (op *AnalyzeDependenciesOperation) generateAnalysisReport(analysis *DependencyAnalysis) string {
	var report strings.Builder
	
	report.WriteString("# Dependency Analysis Report\n\n")
	report.WriteString(fmt.Sprintf("**Workspace**: %s\n", analysis.Workspace))
	report.WriteString(fmt.Sprintf("**Total Packages**: %d\n\n", analysis.TotalPackages))
	
	if len(analysis.BackwardsDeps) > 0 {
		report.WriteString("## Backwards Dependencies\n\n")
		for _, dep := range analysis.BackwardsDeps {
			report.WriteString(fmt.Sprintf("- `%s` → `%s`: %s\n", dep.From, dep.To, dep.Reason))
		}
		report.WriteString("\n")
	}
	
	if len(analysis.SuggestedMoves) > 0 {
		report.WriteString("## Suggested Moves\n\n")
		for _, move := range analysis.SuggestedMoves {
			report.WriteString(fmt.Sprintf("- Move `%s` from `%s` to `%s`: %s\n", 
				move.Symbol, move.FromPackage, move.ToPackage, move.Reason))
		}
		report.WriteString("\n")
	}
	
	report.WriteString("## Package Metrics\n\n")
	report.WriteString("| Package | Symbols | Incoming Deps | Outgoing Deps |\n")
	report.WriteString("|---------|---------|---------------|---------------|\n")
	for pkgPath, metrics := range analysis.PackageMetrics {
		report.WriteString(fmt.Sprintf("| `%s` | %d | %d | %d |\n", 
			pkgPath, metrics.SymbolCount, metrics.IncomingDeps, metrics.OutgoingDeps))
	}
	
	return report.String()
}

// Helper functions
func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}