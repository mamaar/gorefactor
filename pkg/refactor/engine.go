package refactor

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// Engine is the main interface for refactoring operations
type RefactorEngine interface {
	// Workspace management
	LoadWorkspace(path string) (*types.Workspace, error)
	SaveWorkspace(ws *types.Workspace) error

	// Refactoring operations
	MoveSymbol(ws *types.Workspace, req types.MoveSymbolRequest) (*types.RefactoringPlan, error)
	RenameSymbol(ws *types.Workspace, req types.RenameSymbolRequest) (*types.RefactoringPlan, error)
	RenamePackage(ws *types.Workspace, req types.RenamePackageRequest) (*types.RefactoringPlan, error)
	RenameInterfaceMethod(ws *types.Workspace, req types.RenameInterfaceMethodRequest) (*types.RefactoringPlan, error)
	RenameMethod(ws *types.Workspace, req types.RenameMethodRequest) (*types.RefactoringPlan, error)
	ExtractMethod(ws *types.Workspace, req types.ExtractMethodRequest) (*types.RefactoringPlan, error)
	ExtractFunction(ws *types.Workspace, req types.ExtractFunctionRequest) (*types.RefactoringPlan, error)
	ExtractInterface(ws *types.Workspace, req types.ExtractInterfaceRequest) (*types.RefactoringPlan, error)
	ExtractVariable(ws *types.Workspace, req types.ExtractVariableRequest) (*types.RefactoringPlan, error)
	InlineMethod(ws *types.Workspace, req types.InlineMethodRequest) (*types.RefactoringPlan, error)
	InlineVariable(ws *types.Workspace, req types.InlineVariableRequest) (*types.RefactoringPlan, error)
	InlineFunction(ws *types.Workspace, req types.InlineFunctionRequest) (*types.RefactoringPlan, error)
	BatchRefactor(ws *types.Workspace, ops []types.Operation) (*types.RefactoringPlan, error)

	// Bulk operations
	MovePackage(ws *types.Workspace, req types.MovePackageRequest) (*types.RefactoringPlan, error)
	MoveDir(ws *types.Workspace, req types.MoveDirRequest) (*types.RefactoringPlan, error)
	MovePackages(ws *types.Workspace, req types.MovePackagesRequest) (*types.RefactoringPlan, error)
	
	// Facade operations
	CreateFacade(ws *types.Workspace, req types.CreateFacadeRequest) (*types.RefactoringPlan, error)
	GenerateFacades(ws *types.Workspace, req types.GenerateFacadesRequest) (*types.RefactoringPlan, error)
	UpdateFacades(ws *types.Workspace, req types.UpdateFacadesRequest) (*types.RefactoringPlan, error)
	
	// Import alias operations
	CleanAliases(ws *types.Workspace, req types.CleanAliasesRequest) (*types.RefactoringPlan, error)
	StandardizeImports(ws *types.Workspace, req types.StandardizeImportsRequest) (*types.RefactoringPlan, error)
	ResolveAliasConflicts(ws *types.Workspace, req types.ResolveAliasConflictsRequest) (*types.RefactoringPlan, error)
	ConvertAliases(ws *types.Workspace, req types.ConvertAliasesRequest) (*types.RefactoringPlan, error)
	
	// Dependency graph operations
	MoveByDependencies(ws *types.Workspace, req types.MoveByDependenciesRequest) (*types.RefactoringPlan, error)
	OrganizeByLayers(ws *types.Workspace, req types.OrganizeByLayersRequest) (*types.RefactoringPlan, error)
	FixCycles(ws *types.Workspace, req types.FixCyclesRequest) (*types.RefactoringPlan, error)
	AnalyzeDependencies(ws *types.Workspace, req types.AnalyzeDependenciesRequest) (*types.RefactoringPlan, error)
	
	// Batch operations with rollback
	BatchOperations(ws *types.Workspace, req types.BatchOperationRequest) (*types.RefactoringPlan, error)
	CreatePlan(ws *types.Workspace, req types.PlanOperationRequest) (*types.RefactoringPlan, error)
	ExecutePlanFromFile(req types.ExecuteOperationRequest) (*types.RefactoringPlan, error)
	RollbackOperations(req types.RollbackOperationRequest) (*types.RefactoringPlan, error)

	// Analysis
	AnalyzeImpact(ws *types.Workspace, op types.Operation) (*types.ImpactAnalysis, error)
	ValidateRefactoring(plan *types.RefactoringPlan) error

	// Execution
	ExecutePlan(plan *types.RefactoringPlan) error
	PreviewPlan(plan *types.RefactoringPlan) (string, error)
}

// DefaultEngine implements the Engine interface
type DefaultEngine struct {
	parser     *analysis.GoParser
	resolver   *analysis.SymbolResolver
	analyzer   *analysis.DependencyAnalyzer
	validator  *Validator
	serializer *Serializer
	config     *EngineConfig
}

// EngineConfig contains configuration options for the refactoring engine
type EngineConfig struct {
	SkipCompilation bool
	AllowBreaking   bool
}

// DefaultConfig returns the default engine configuration
func DefaultConfig() *EngineConfig {
	return &EngineConfig{
		SkipCompilation: false,
		AllowBreaking:   false,
	}
}

func CreateEngine() RefactorEngine {
	return CreateEngineWithConfig(DefaultConfig())
}

func CreateEngineWithConfig(config *EngineConfig) RefactorEngine {
	return &DefaultEngine{
		parser:     analysis.NewParser(),
		validator:  NewValidator(),
		serializer: NewSerializer(),
		config:     config,
	}
}

// LoadWorkspace loads and parses a complete workspace
func (e *DefaultEngine) LoadWorkspace(path string) (*types.Workspace, error) {
	// Parse the workspace
	workspace, err := e.parser.ParseWorkspace(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workspace: %w", err)
	}

	// Create resolver with parsed workspace
	e.resolver = analysis.NewSymbolResolver(workspace)

	// Build symbol tables for all packages
	for _, pkg := range workspace.Packages {
		_, err := e.resolver.BuildSymbolTable(pkg)
		if err != nil {
			return nil, fmt.Errorf("failed to build symbol table for package %s: %w", pkg.Path, err)
		}
	}

	// Create dependency analyzer and build dependency graph
	e.analyzer = analysis.NewDependencyAnalyzer(workspace)
	_, err = e.analyzer.BuildDependencyGraph()
	if err != nil {
		return nil, fmt.Errorf("failed to build dependency graph: %w", err)
	}

	return workspace, nil
}

// SaveWorkspace saves all changes in the workspace to disk
func (e *DefaultEngine) SaveWorkspace(ws *types.Workspace) error {
	var allChanges []types.Change

	// Collect all pending changes from all files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if len(file.Modifications) > 0 {
				changes := e.modificationsToChanges(file.Modifications, file.Path)
				allChanges = append(allChanges, changes...)
			}
		}
	}

	if len(allChanges) == 0 {
		return nil // No changes to save
	}

	// Apply all changes
	return e.serializer.ApplyChanges(ws, allChanges)
}

// MoveSymbol implements symbol moving between packages
func (e *DefaultEngine) MoveSymbol(ws *types.Workspace, req types.MoveSymbolRequest) (*types.RefactoringPlan, error) {
	operation := &MoveSymbolOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameSymbol implements symbol renaming
func (e *DefaultEngine) RenameSymbol(ws *types.Workspace, req types.RenameSymbolRequest) (*types.RefactoringPlan, error) {
	operation := &RenameSymbolOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rename operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenamePackage implements package renaming
func (e *DefaultEngine) RenamePackage(ws *types.Workspace, req types.RenamePackageRequest) (*types.RefactoringPlan, error) {
	operation := &RenamePackageOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rename package operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename package plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameInterfaceMethod implements interface method renaming
func (e *DefaultEngine) RenameInterfaceMethod(ws *types.Workspace, req types.RenameInterfaceMethodRequest) (*types.RefactoringPlan, error) {
	operation := &RenameInterfaceMethodOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rename interface method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename interface method plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RenameMethod implements renaming methods on specific types (structs or interfaces)
func (e *DefaultEngine) RenameMethod(ws *types.Workspace, req types.RenameMethodRequest) (*types.RefactoringPlan, error) {
	operation := &RenameMethodOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rename method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rename method plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractMethod implements method extraction from code blocks
func (e *DefaultEngine) ExtractMethod(ws *types.Workspace, req types.ExtractMethodRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractMethodOperation{
		SourceFile:    req.SourceFile,
		StartLine:     req.StartLine,
		EndLine:       req.EndLine,
		NewMethodName: req.NewMethodName,
		TargetStruct:  req.TargetStruct,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("extract method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract method plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractFunction implements function extraction from code blocks
func (e *DefaultEngine) ExtractFunction(ws *types.Workspace, req types.ExtractFunctionRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractFunctionOperation{
		SourceFile:      req.SourceFile,
		StartLine:       req.StartLine,
		EndLine:         req.EndLine,
		NewFunctionName: req.NewFunctionName,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("extract function operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract function plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractInterface implements interface extraction from structs
func (e *DefaultEngine) ExtractInterface(ws *types.Workspace, req types.ExtractInterfaceRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractInterfaceOperation{
		SourceStruct:  req.SourceStruct,
		InterfaceName: req.InterfaceName,
		Methods:       req.Methods,
		TargetPackage: req.TargetPackage,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("extract interface operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract interface plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExtractVariable implements variable extraction from expressions
func (e *DefaultEngine) ExtractVariable(ws *types.Workspace, req types.ExtractVariableRequest) (*types.RefactoringPlan, error) {
	operation := &ExtractVariableOperation{
		SourceFile:   req.SourceFile,
		StartLine:    req.StartLine,
		EndLine:      req.EndLine,
		VariableName: req.VariableName,
		Expression:   req.Expression,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("extract variable operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate extract variable plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineMethod implements method call inlining
func (e *DefaultEngine) InlineMethod(ws *types.Workspace, req types.InlineMethodRequest) (*types.RefactoringPlan, error) {
	operation := &InlineMethodOperation{
		MethodName:   req.MethodName,
		SourceStruct: req.SourceStruct,
		TargetFile:   req.TargetFile,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("inline method operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline method plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineVariable implements variable inlining
func (e *DefaultEngine) InlineVariable(ws *types.Workspace, req types.InlineVariableRequest) (*types.RefactoringPlan, error) {
	operation := &InlineVariableOperation{
		VariableName: req.VariableName,
		SourceFile:   req.SourceFile,
		StartLine:    1,    // Default - could be enhanced to specify line
		EndLine:      1000, // Default - means all occurrences (large number)
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("inline variable operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline variable plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// InlineFunction implements function call inlining
func (e *DefaultEngine) InlineFunction(ws *types.Workspace, req types.InlineFunctionRequest) (*types.RefactoringPlan, error) {
	operation := &InlineFunctionOperation{
		FunctionName: req.FunctionName,
		SourceFile:   req.SourceFile,
		TargetFiles:  req.TargetFiles,
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("inline function operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate inline function plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// BatchRefactor executes multiple refactoring operations as a batch
func (e *DefaultEngine) BatchRefactor(ws *types.Workspace, ops []types.Operation) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    ops,
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	var allIssues []types.Issue
	var allChanges []types.Change
	affectedFiles := make(map[string]bool)
	affectedPackages := make(map[string]bool)
	var affectedSymbols []*types.Symbol

	// Process each operation
	for i, op := range ops {
		// Validate each operation
		if err := op.Validate(ws); err != nil {
			return nil, fmt.Errorf("operation %d validation failed: %w", i, err)
		}

		// Generate plan for this operation
		opPlan, err := op.Execute(ws)
		if err != nil {
			return nil, fmt.Errorf("operation %d execution failed: %w", i, err)
		}

		// Analyze impact
		impact, err := e.analyzer.AnalyzeImpact(op)
		if err != nil {
			return nil, fmt.Errorf("failed to analyze impact for operation %d: %w", i, err)
		}

		// Collect results
		allChanges = append(allChanges, opPlan.Changes...)
		allIssues = append(allIssues, impact.PotentialIssues...)
		affectedSymbols = append(affectedSymbols, impact.AffectedSymbols...)

		for _, file := range impact.AffectedFiles {
			affectedFiles[file] = true
		}
		for _, pkg := range impact.AffectedPackages {
			affectedPackages[pkg] = true
		}

		if !opPlan.Reversible {
			plan.Reversible = false
		}
	}

	// Check for conflicts between operations
	conflicts := e.findOperationConflicts(allChanges)
	if len(conflicts) > 0 {
		for _, conflict := range conflicts {
			issue := types.Issue{
				Type:        types.IssueNameConflict,
				Description: conflict,
				Severity:    types.Error,
			}
			allIssues = append(allIssues, issue)
		}
	}

	// Build final plan
	plan.Changes = allChanges

	for file := range affectedFiles {
		plan.AffectedFiles = append(plan.AffectedFiles, file)
	}

	plan.Impact = &types.ImpactAnalysis{
		AffectedFiles:   plan.AffectedFiles,
		AffectedSymbols: affectedSymbols,
		PotentialIssues: allIssues,
	}

	for pkg := range affectedPackages {
		plan.Impact.AffectedPackages = append(plan.Impact.AffectedPackages, pkg)
	}

	return plan, nil
}

// AnalyzeImpact analyzes the impact of a refactoring operation
func (e *DefaultEngine) AnalyzeImpact(ws *types.Workspace, op types.Operation) (*types.ImpactAnalysis, error) {
	if e.analyzer == nil {
		return nil, fmt.Errorf("workspace not loaded")
	}

	return e.analyzer.AnalyzeImpact(op)
}

// ValidateRefactoring validates a complete refactoring plan
func (e *DefaultEngine) ValidateRefactoring(plan *types.RefactoringPlan) error {
	return e.validator.ValidatePlanWithConfig(plan, e.config)
}

// ExecutePlan applies a refactoring plan to the workspace
func (e *DefaultEngine) ExecutePlan(plan *types.RefactoringPlan) error {
	// Final validation before execution
	if err := e.ValidateRefactoring(plan); err != nil {
		return err // Return the validation error directly to preserve its type
	}

	// Check for critical issues
	for _, issue := range plan.Impact.PotentialIssues {
		if issue.Severity == types.Error {
			return &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("cannot execute plan due to critical issue: %s", issue.Description),
				File:    issue.File,
				Line:    issue.Line,
			}
		}
	}

	// Apply changes
	if len(plan.Changes) > 0 {
		err := e.serializer.ApplyChanges(nil, plan.Changes) // workspace will be inferred from changes
		if err != nil {
			return fmt.Errorf("failed to apply changes: %w", err)
		}
		
		// Validate that the refactored code compiles (if not skipped)
		if !e.shouldSkipCompilation() {
			if err := e.validateCompilation(plan.AffectedFiles); err != nil {
				return fmt.Errorf("refactored code does not compile: %w", err)
			}
		}
	}

	return nil
}

// shouldSkipCompilation returns true if compilation validation should be skipped
func (e *DefaultEngine) shouldSkipCompilation() bool {
	return e.config != nil && e.config.SkipCompilation
}

// validateCompilation checks that the modified files still compile
func (e *DefaultEngine) validateCompilation(affectedFiles []string) error {
	if len(affectedFiles) == 0 {
		return nil
	}
	
	// Get unique directories that need compilation checking
	dirsToCheck := make(map[string]bool)
	for _, file := range affectedFiles {
		dir := filepath.Dir(file)
		dirsToCheck[dir] = true
	}
	
	// Check compilation for each affected directory
	for dir := range dirsToCheck {
		if err := e.checkDirectoryCompilation(dir); err != nil {
			return fmt.Errorf("compilation failed in %s: %w", dir, err)
		}
	}
	
	return nil
}

// checkDirectoryCompilation runs go build on a directory to check compilation
func (e *DefaultEngine) checkDirectoryCompilation(dir string) error {
	// Use go build to check compilation without creating binaries
	cmd := exec.Command("go", "build", "-o", "/dev/null", ".")
	cmd.Dir = dir
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %s", string(output))
	}
	
	return nil
}

// PreviewPlan generates a preview of the changes without applying them
func (e *DefaultEngine) PreviewPlan(plan *types.RefactoringPlan) (string, error) {
	return e.serializer.PreviewChanges(nil, plan.Changes)
}

// Helper methods

func (e *DefaultEngine) modificationsToChanges(modifications []types.Modification, filePath string) []types.Change {
	var changes []types.Change

	for _, mod := range modifications {
		change := types.Change{
			File:        filePath,
			Start:       mod.Start,
			End:         mod.End,
			NewText:     mod.NewText,
			Description: e.getModificationDescription(mod),
		}

		// For modifications, we need to infer the old text
		// This would require reading the current file content
		// For now, leaving it empty - real implementation would fill this in
		change.OldText = ""

		changes = append(changes, change)
	}

	return changes
}

func (e *DefaultEngine) getModificationDescription(mod types.Modification) string {
	switch mod.Type {
	case types.Insert:
		return fmt.Sprintf("Insert text at position %d", mod.Start)
	case types.Delete:
		return fmt.Sprintf("Delete text from %d to %d", mod.Start, mod.End)
	case types.Replace:
		return fmt.Sprintf("Replace text from %d to %d", mod.Start, mod.End)
	default:
		return "Unknown modification"
	}
}

func (e *DefaultEngine) findOperationConflicts(changes []types.Change) []string {
	var conflicts []string

	// Group changes by file
	fileChanges := make(map[string][]types.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Check for overlapping changes in each file
	for file, changes := range fileChanges {
		for i, change1 := range changes {
			for j, change2 := range changes {
				if i >= j {
					continue
				}

				// Check if changes overlap
				if (change1.Start <= change2.Start && change2.Start < change1.End) ||
					(change2.Start <= change1.Start && change1.Start < change2.End) {
					conflicts = append(conflicts, fmt.Sprintf("Overlapping changes in file %s", file))
				}
			}
		}
	}

	return conflicts
}

// Bulk operation implementations

// MovePackage implements moving entire packages
func (e *DefaultEngine) MovePackage(ws *types.Workspace, req types.MovePackageRequest) (*types.RefactoringPlan, error) {
	operation := &MovePackageOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move package operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move package plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MoveDir implements moving directory structures
func (e *DefaultEngine) MoveDir(ws *types.Workspace, req types.MoveDirRequest) (*types.RefactoringPlan, error) {
	operation := &MoveDirOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move directory operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move directory plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MovePackages implements moving multiple packages atomically
func (e *DefaultEngine) MovePackages(ws *types.Workspace, req types.MovePackagesRequest) (*types.RefactoringPlan, error) {
	operation := &MovePackagesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move packages operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move packages plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// CreateFacade implements creating facade packages
func (e *DefaultEngine) CreateFacade(ws *types.Workspace, req types.CreateFacadeRequest) (*types.RefactoringPlan, error) {
	operation := &CreateFacadeOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("create facade operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate create facade plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// GenerateFacades implements auto-generating facades
func (e *DefaultEngine) GenerateFacades(ws *types.Workspace, req types.GenerateFacadesRequest) (*types.RefactoringPlan, error) {
	operation := &GenerateFacadesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("generate facades operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate facades plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// UpdateFacades implements updating existing facades
func (e *DefaultEngine) UpdateFacades(ws *types.Workspace, req types.UpdateFacadesRequest) (*types.RefactoringPlan, error) {
	operation := &UpdateFacadesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("update facades operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate update facades plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// CleanAliases implements cleaning import aliases
func (e *DefaultEngine) CleanAliases(ws *types.Workspace, req types.CleanAliasesRequest) (*types.RefactoringPlan, error) {
	operation := &CleanAliasesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("clean aliases operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate clean aliases plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// StandardizeImports implements standardizing import aliases
func (e *DefaultEngine) StandardizeImports(ws *types.Workspace, req types.StandardizeImportsRequest) (*types.RefactoringPlan, error) {
	operation := &StandardizeImportsOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("standardize imports operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate standardize imports plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ResolveAliasConflicts implements resolving import alias conflicts
func (e *DefaultEngine) ResolveAliasConflicts(ws *types.Workspace, req types.ResolveAliasConflictsRequest) (*types.RefactoringPlan, error) {
	operation := &ResolveAliasConflictsOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("resolve alias conflicts operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate resolve alias conflicts plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ConvertAliases implements converting between aliased and non-aliased imports
func (e *DefaultEngine) ConvertAliases(ws *types.Workspace, req types.ConvertAliasesRequest) (*types.RefactoringPlan, error) {
	operation := &ConvertAliasesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("convert aliases operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate convert aliases plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// MoveByDependencies implements moving symbols based on dependency analysis
func (e *DefaultEngine) MoveByDependencies(ws *types.Workspace, req types.MoveByDependenciesRequest) (*types.RefactoringPlan, error) {
	operation := &MoveByDependenciesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("move by dependencies operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate move by dependencies plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// OrganizeByLayers implements organizing packages by architectural layers
func (e *DefaultEngine) OrganizeByLayers(ws *types.Workspace, req types.OrganizeByLayersRequest) (*types.RefactoringPlan, error) {
	operation := &OrganizeByLayersOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("organize by layers operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate organize by layers plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// FixCycles implements detecting and fixing circular dependencies
func (e *DefaultEngine) FixCycles(ws *types.Workspace, req types.FixCyclesRequest) (*types.RefactoringPlan, error) {
	operation := &FixCyclesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("fix cycles operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate fix cycles plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// AnalyzeDependencies implements analyzing dependency flow
func (e *DefaultEngine) AnalyzeDependencies(ws *types.Workspace, req types.AnalyzeDependenciesRequest) (*types.RefactoringPlan, error) {
	operation := &AnalyzeDependenciesOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("analyze dependencies operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate analyze dependencies plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// BatchOperations implements executing multiple operations atomically
func (e *DefaultEngine) BatchOperations(ws *types.Workspace, req types.BatchOperationRequest) (*types.RefactoringPlan, error) {
	operation := &BatchOperationOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("batch operations validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate batch operations plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// CreatePlan implements creating a refactoring plan
func (e *DefaultEngine) CreatePlan(ws *types.Workspace, req types.PlanOperationRequest) (*types.RefactoringPlan, error) {
	operation := &PlanOperation{Request: req}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("plan operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// ExecutePlanFromFile implements executing a previously created plan
func (e *DefaultEngine) ExecutePlanFromFile(req types.ExecuteOperationRequest) (*types.RefactoringPlan, error) {
	// Note: this method signature differs from the interface ExecutePlan method
	// to avoid confusion with executing an in-memory plan
	operation := &ExecuteOperation{Request: req}

	// Load the plan file first to get workspace info
	// This is a simplified approach - in reality we'd need better workspace management
	ws, err := e.LoadWorkspace(".")
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace for plan execution: %w", err)
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("execute operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to execute plan from file: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}

// RollbackOperations implements rolling back operations
func (e *DefaultEngine) RollbackOperations(req types.RollbackOperationRequest) (*types.RefactoringPlan, error) {
	operation := &RollbackOperation{Request: req}

	// For rollback, we might not need a workspace initially
	// This is a simplified approach - in reality we'd need better state management
	ws, err := e.LoadWorkspace(".")
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace for rollback: %w", err)
	}

	// Validate the operation
	if err := operation.Validate(ws); err != nil {
		return nil, fmt.Errorf("rollback operation validation failed: %w", err)
	}

	// Execute the operation to generate the plan
	plan, err := operation.Execute(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to generate rollback plan: %w", err)
	}

	// Analyze impact
	impact, err := e.analyzer.AnalyzeImpact(operation)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze impact: %w", err)
	}

	plan.Impact = impact
	plan.Operations = []types.Operation{operation}

	return plan, nil
}
