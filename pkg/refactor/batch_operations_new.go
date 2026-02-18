package refactor

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

// parseOperationString parses a JSON operation string into a types.Operation.
// The JSON object must contain a "type" field to identify the operation kind.
func parseOperationString(opStr string) (types.Operation, error) {
	var raw map[string]string
	if err := json.Unmarshal([]byte(opStr), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse operation JSON: %w", err)
	}

	opType, ok := raw["type"]
	if !ok {
		return nil, fmt.Errorf("operation JSON missing 'type' field")
	}

	switch opType {
	case "rename_symbol":
		return &RenameSymbolOperation{
			Request: types.RenameSymbolRequest{
				SymbolName: raw["symbol"],
				NewName:    raw["new_name"],
				Package:    raw["package"],
			},
		}, nil
	case "move_symbol":
		return &MoveSymbolOperation{
			Request: types.MoveSymbolRequest{
				SymbolName:  raw["symbol"],
				FromPackage: raw["from_package"],
				ToPackage:   raw["to_package"],
			},
		}, nil
	case "rename_package":
		return &RenamePackageOperation{
			Request: types.RenamePackageRequest{
				OldPackageName: raw["old_name"],
				NewPackageName: raw["new_name"],
				PackagePath:    raw["package_path"],
				UpdateImports:  true,
			},
		}, nil
	case "move_package":
		return &MovePackageOperation{
			Request: types.MovePackageRequest{
				SourcePackage: raw["source"],
				TargetPackage: raw["target"],
				UpdateImports: true,
			},
		}, nil
	case "rename_method":
		return &RenameInterfaceMethodOperation{
			Request: types.RenameInterfaceMethodRequest{
				InterfaceName:         raw["type_name"],
				MethodName:            raw["method_name"],
				NewMethodName:         raw["new_method_name"],
				PackagePath:           raw["package_path"],
				UpdateImplementations: true,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown operation type: %s", opType)
	}
}

// BatchOperationOperation implements executing multiple operations atomically
type BatchOperationOperation struct {
	Request types.BatchOperationRequest
}

func (op *BatchOperationOperation) Type() types.OperationType {
	return types.BatchOperations
}

func (op *BatchOperationOperation) Description() string {
	return fmt.Sprintf("Execute %d operations atomically", len(op.Request.Operations))
}

func (op *BatchOperationOperation) Validate(ws *types.Workspace) error {
	if len(op.Request.Operations) == 0 {
		return fmt.Errorf("no operations specified for batch execution")
	}
	return nil
}

func (op *BatchOperationOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Parse all operation strings first so we fail fast on bad input.
	operations := make([]types.Operation, 0, len(op.Request.Operations))
	for i, opStr := range op.Request.Operations {
		parsed, err := parseOperationString(opStr)
		if err != nil {
			return nil, fmt.Errorf("operation %d: %w", i+1, err)
		}
		operations = append(operations, parsed)
	}

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	if op.Request.DryRun {
		// Validate each and return descriptions only.
		for i, parsed := range operations {
			if err := parsed.Validate(ws); err != nil {
				return nil, fmt.Errorf("operation %d validation failed: %w", i+1, err)
			}
			plan.Changes = append(plan.Changes, types.Change{
				Description: fmt.Sprintf("Step %d: %s", i+1, parsed.Description()),
			})
		}
		return plan, nil
	}

	// Execute each operation sequentially, collecting all changes.
	fileSet := make(map[string]bool)
	for i, parsed := range operations {
		if err := parsed.Validate(ws); err != nil {
			return nil, fmt.Errorf("operation %d validation failed: %w", i+1, err)
		}
		result, err := parsed.Execute(ws)
		if err != nil {
			if op.Request.RollbackOnFailure {
				return nil, fmt.Errorf("operation %d failed (no changes applied): %w", i+1, err)
			}
			return nil, fmt.Errorf("operation %d failed: %w", i+1, err)
		}
		plan.Changes = append(plan.Changes, result.Changes...)
		for _, f := range result.AffectedFiles {
			if !fileSet[f] {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
				fileSet[f] = true
			}
		}
	}

	return plan, nil
}

// PlanOperation implements creating a refactoring plan
type PlanOperation struct {
	Request types.PlanOperationRequest
}

func (op *PlanOperation) Type() types.OperationType {
	return types.PlanOperation
}

func (op *PlanOperation) Description() string {
	return fmt.Sprintf("Create refactoring plan with %d steps", len(op.Request.Operations))
}

func (op *PlanOperation) Validate(ws *types.Workspace) error {
	if len(op.Request.Operations) == 0 {
		return fmt.Errorf("no operations specified for plan creation")
	}
	if op.Request.OutputFile == "" {
		return fmt.Errorf("output file must be specified for plan creation")
	}
	return nil
}

func (op *PlanOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Create the refactoring plan file
	planData := RefactoringPlanFile{
		Version:   "1.0",
		CreatedAt: time.Now().Format(time.RFC3339),
		Workspace: ws.RootPath,
		Steps:     op.Request.Operations,
		DryRun:    op.Request.DryRun,
	}

	jsonData, err := json.MarshalIndent(planData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plan to JSON: %w", err)
	}

	plan.Changes = append(plan.Changes, types.Change{
		File:        op.Request.OutputFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     string(jsonData),
		Description: "Create refactoring plan file",
	})

	plan.AffectedFiles = []string{op.Request.OutputFile}

	return plan, nil
}

type RefactoringPlanFile struct {
	Version   string           `json:"version"`
	CreatedAt string           `json:"created_at"`
	Workspace string           `json:"workspace"`
	Steps     []types.PlanStep `json:"steps"`
	DryRun    bool             `json:"dry_run"`
}

// ExecuteOperation implements executing a previously created plan
type ExecuteOperation struct {
	Request types.ExecuteOperationRequest
}

func (op *ExecuteOperation) Type() types.OperationType {
	return types.ExecuteOperation
}

func (op *ExecuteOperation) Description() string {
	return fmt.Sprintf("Execute refactoring plan from %s", op.Request.PlanFile)
}

func (op *ExecuteOperation) Validate(ws *types.Workspace) error {
	if op.Request.PlanFile == "" {
		return fmt.Errorf("plan file cannot be empty")
	}

	// Check if plan file exists
	if _, err := os.Stat(op.Request.PlanFile); os.IsNotExist(err) {
		return fmt.Errorf("plan file does not exist: %s", op.Request.PlanFile)
	}

	return nil
}

func (op *ExecuteOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	// Read the plan file
	planData, err := os.ReadFile(op.Request.PlanFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read plan file: %w", err)
	}

	var planFile RefactoringPlanFile
	if err := json.Unmarshal(planData, &planFile); err != nil {
		return nil, fmt.Errorf("failed to parse plan file: %w", err)
	}

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Convert each PlanStep to an operation, validate, and execute.
	fileSet := make(map[string]bool)
	for i, step := range planFile.Steps {
		// Marshal step args back to JSON for parseOperationString
		stepArgs := make(map[string]string)
		maps.Copy(stepArgs, step.Args)
		stepArgs["type"] = step.Type
		stepJSON, err := json.Marshal(stepArgs)
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to marshal: %w", i+1, err)
		}

		parsed, err := parseOperationString(string(stepJSON))
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}

		if err := parsed.Validate(ws); err != nil {
			return nil, fmt.Errorf("step %d validation failed: %w", i+1, err)
		}

		result, err := parsed.Execute(ws)
		if err != nil {
			return nil, fmt.Errorf("step %d execution failed: %w", i+1, err)
		}

		plan.Changes = append(plan.Changes, result.Changes...)
		for _, f := range result.AffectedFiles {
			if !fileSet[f] {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
				fileSet[f] = true
			}
		}
	}

	return plan, nil
}

// RollbackOperation implements rolling back operations
type RollbackOperation struct {
	Request types.RollbackOperationRequest
}

func (op *RollbackOperation) Type() types.OperationType {
	return types.RollbackOperation
}

func (op *RollbackOperation) Description() string {
	if op.Request.LastBatch {
		return "Rollback last batch operation"
	}
	return fmt.Sprintf("Rollback to step %d", op.Request.ToStep)
}

func (op *RollbackOperation) Validate(ws *types.Workspace) error {
	if !op.Request.LastBatch && op.Request.ToStep <= 0 {
		return fmt.Errorf("must specify either last batch rollback or valid step number")
	}
	return nil
}

func (op *RollbackOperation) Execute(_ *types.Workspace) (*types.RefactoringPlan, error) {
	return nil, fmt.Errorf("rollback is not yet supported; use version control (git) to revert changes instead")
}
