package refactor

import (
	"fmt"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// BatchOperation implements executing multiple operations as a single transaction
type BatchOperation struct {
	Operations []types.Operation
	Name       string
	Atomic     bool // If true, all operations must succeed or none are applied
}

func (op *BatchOperation) Type() types.OperationType {
	return types.BatchOperation
}

func (op *BatchOperation) Validate(ws *types.Workspace) error {
	if op.Name == "" {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "batch operation name cannot be empty",
		}
	}
	if len(op.Operations) == 0 {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "batch operation must contain at least one operation",
		}
	}

	// Validate each individual operation
	for i, operation := range op.Operations {
		if err := operation.Validate(ws); err != nil {
			return &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("operation %d validation failed: %v", i, err),
			}
		}
	}

	return nil
}

func (op *BatchOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	var allChanges []types.Change
	var allAffectedFiles []string
	var allAffectedPackages []string
	var executedOperations []types.Operation
	var executionResults []*types.RefactoringPlan

	// Execute each operation and collect changes
	var firstError error
	for i, operation := range op.Operations {
		plan, err := operation.Execute(ws)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			if op.Atomic {
				// Rollback - in a real implementation, this would undo changes
				return nil, &types.RefactorError{
					Type:    types.InvalidOperation,
					Message: fmt.Sprintf("batch operation failed at step %d, rolling back: %v", i, err),
				}
			}
			// Non-atomic: continue with other operations but track the error
			continue
		}

		executionResults = append(executionResults, plan)
		allChanges = append(allChanges, plan.Changes...)
		allAffectedFiles = append(allAffectedFiles, plan.AffectedFiles...)
		if plan.Impact != nil {
			allAffectedPackages = append(allAffectedPackages, plan.Impact.AffectedPackages...)
		}
		executedOperations = append(executedOperations, operation)
	}

	// Check for conflicts between operations
	conflicts := op.detectConflicts(allChanges)
	if len(conflicts) > 0 {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("batch operation contains conflicting changes: %s", strings.Join(conflicts, ", ")),
		}
	}

	// Remove duplicate files and packages
	uniqueFiles := removeDuplicateStrings(allAffectedFiles)
	uniquePackages := removeDuplicateStrings(allAffectedPackages)

	// Aggregate all issues from individual operations
	var allIssues []types.Issue
	for _, result := range executionResults {
		if result.Impact != nil {
			allIssues = append(allIssues, result.Impact.PotentialIssues...)
		}
	}

	// Sort changes by file and position to ensure consistent application
	sortedChanges := op.sortChanges(allChanges)

	// If no operations succeeded and we have an error, return the error
	if len(executedOperations) == 0 && firstError != nil {
		return nil, firstError
	}

	return &types.RefactoringPlan{
		Operations:    executedOperations,
		Changes:       sortedChanges,
		AffectedFiles: uniqueFiles,
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    uniqueFiles,
			AffectedPackages: uniquePackages,
			PotentialIssues:  allIssues,
		},
		Reversible: op.allOperationsReversible(executedOperations),
	}, firstError
}

func (op *BatchOperation) Description() string {
	operationTypes := make([]string, len(op.Operations))
	for i, operation := range op.Operations {
		operationTypes[i] = op.operationTypeToString(operation.Type())
	}
	return fmt.Sprintf("Batch operation '%s' with %d operations: [%s]",
		op.Name, len(op.Operations), strings.Join(operationTypes, " "))
}

func (op *BatchOperation) detectConflicts(changes []types.Change) []string {
	var conflicts []string

	// Group changes by file
	fileChanges := make(map[string][]types.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Check for overlapping changes within each file
	for file, changes := range fileChanges {
		for i := range changes {
			for j := i + 1; j < len(changes); j++ {
				if op.changesOverlap(changes[i], changes[j]) {
					conflicts = append(conflicts, fmt.Sprintf("overlapping changes in %s at positions %d-%d and %d-%d",
						file, changes[i].Start, changes[i].End, changes[j].Start, changes[j].End))
				}
			}
		}
	}

	return conflicts
}

func (op *BatchOperation) changesOverlap(a, b types.Change) bool {
	if a.File != b.File {
		return false
	}
	// Check if ranges overlap: [a.Start, a.End) and [b.Start, b.End)
	return a.End > b.Start && b.End > a.Start
}

func (op *BatchOperation) sortChanges(changes []types.Change) []types.Change {
	// Sort changes by file path first, then by start position in reverse order
	// (reverse order ensures that later changes don't affect earlier positions)

	sorted := make([]types.Change, len(changes))
	copy(sorted, changes)

	// Simple bubble sort for demonstration - a real implementation would use a more efficient sort
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			// Sort by file first
			if sorted[i].File > sorted[j].File {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			} else if sorted[i].File == sorted[j].File {
				// Same file: sort by position in reverse order
				if sorted[i].Start < sorted[j].Start {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
	}

	return sorted
}

func (op *BatchOperation) allOperationsReversible(operations []types.Operation) bool {
	// Check if all operations in the batch are reversible
	// This would require examining each operation's reversibility
	// For now, assume batch operations are not reversible due to complexity
	return false
}

func (op *BatchOperation) operationTypeToString(opType types.OperationType) string {
	switch opType {
	case types.MoveOperation:
		return "MoveOperation"
	case types.RenameOperation:
		return "RenameOperation"
	case types.ExtractOperation:
		return "ExtractOperation"
	case types.InlineOperation:
		return "InlineOperation"
	case types.BatchOperation:
		return "BatchOperation"
	default:
		return "UnknownOperation"
	}
}

// Helper functions for batch operations

func removeDuplicateStrings(strs []string) []string {
	keys := make(map[string]bool)
	var result []string
	for _, str := range strs {
		if !keys[str] {
			keys[str] = true
			result = append(result, str)
		}
	}
	return result
}

// BatchOperationBuilder provides a fluent interface for building batch operations
type BatchOperationBuilder struct {
	operations []types.Operation
	name       string
	atomic     bool
}

func NewBatchOperation(name string) *BatchOperationBuilder {
	return &BatchOperationBuilder{
		operations: make([]types.Operation, 0),
		name:       name,
		atomic:     false,
	}
}

func (b *BatchOperationBuilder) AddOperation(op types.Operation) *BatchOperationBuilder {
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddMoveOperation(symbolName, fromPackage, toPackage string) *BatchOperationBuilder {
	op := &MoveSymbolOperation{
		Request: types.MoveSymbolRequest{
			SymbolName:  symbolName,
			FromPackage: fromPackage,
			ToPackage:   toPackage,
		},
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddRenameOperation(symbolName, newName, packagePath string, scope types.RenameScope) *BatchOperationBuilder {
	op := &RenameSymbolOperation{
		Request: types.RenameSymbolRequest{
			SymbolName: symbolName,
			NewName:    newName,
			Package:    packagePath,
			Scope:      scope,
		},
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddExtractMethodOperation(sourceFile string, startLine, endLine int, methodName, targetStruct string) *BatchOperationBuilder {
	op := &ExtractMethodOperation{
		SourceFile:    sourceFile,
		StartLine:     startLine,
		EndLine:       endLine,
		NewMethodName: methodName,
		TargetStruct:  targetStruct,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddExtractInterfaceOperation(sourceStruct, interfaceName string, methods []string, targetPackage string) *BatchOperationBuilder {
	op := &ExtractInterfaceOperation{
		SourceStruct:  sourceStruct,
		InterfaceName: interfaceName,
		Methods:       methods,
		TargetPackage: targetPackage,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddExtractVariableOperation(sourceFile string, startLine, endLine int, variableName, expression string) *BatchOperationBuilder {
	op := &ExtractVariableOperation{
		SourceFile:   sourceFile,
		StartLine:    startLine,
		EndLine:      endLine,
		VariableName: variableName,
		Expression:   expression,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddInlineMethodOperation(methodName, sourceStruct, targetFile string) *BatchOperationBuilder {
	op := &InlineMethodOperation{
		MethodName:   methodName,
		SourceStruct: sourceStruct,
		TargetFile:   targetFile,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddInlineVariableOperation(variableName, sourceFile string, startLine, endLine int) *BatchOperationBuilder {
	op := &InlineVariableOperation{
		VariableName: variableName,
		SourceFile:   sourceFile,
		StartLine:    startLine,
		EndLine:      endLine,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddInlineFunctionOperation(functionName, sourceFile string, targetFiles []string) *BatchOperationBuilder {
	op := &InlineFunctionOperation{
		FunctionName: functionName,
		SourceFile:   sourceFile,
		TargetFiles:  targetFiles,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) AddInlineConstantOperation(constantName, sourceFile string, scope types.RenameScope) *BatchOperationBuilder {
	op := &InlineConstantOperation{
		ConstantName: constantName,
		SourceFile:   sourceFile,
		Scope:        scope,
	}
	b.operations = append(b.operations, op)
	return b
}

func (b *BatchOperationBuilder) SetAtomic(atomic bool) *BatchOperationBuilder {
	b.atomic = atomic
	return b
}

func (b *BatchOperationBuilder) Build() *BatchOperation {
	return &BatchOperation{
		Operations: b.operations,
		Name:       b.name,
		Atomic:     b.atomic,
	}
}

// Pre-defined batch operation patterns

// CreateRefactoringWorkflow creates common refactoring workflows
func CreateExtractAndRenameWorkflow(structName, methodName, newMethodName string, sourceFile string, startLine, endLine int) *BatchOperation {
	return NewBatchOperation("Extract and Rename Method").
		AddExtractMethodOperation(sourceFile, startLine, endLine, methodName, structName).
		AddRenameOperation(methodName, newMethodName, "", types.PackageScope).
		SetAtomic(true).
		Build()
}

func CreateMoveAndUpdateWorkflow(symbolName, fromPackage, toPackage string) *BatchOperation {
	return NewBatchOperation("Move Symbol with Updates").
		AddMoveOperation(symbolName, fromPackage, toPackage).
		SetAtomic(true).
		Build()
}

func CreateInterfaceExtractionWorkflow(sourceStruct, interfaceName string, methods []string, targetPackage string) *BatchOperation {
	builder := NewBatchOperation("Extract Interface and Update References").
		AddExtractInterfaceOperation(sourceStruct, interfaceName, methods, targetPackage).
		SetAtomic(true)

	// Could add additional operations to update references to use the interface
	// This would require more sophisticated analysis

	return builder.Build()
}
