package refactor

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

// planStep plans one batch step with the engine method the step's standalone
// tool uses. The step is a JSON object with a "type" field; ws resolves package
// paths and is the workspace of the steps that plan in the default configuration only.
func (e *DefaultEngine) planStep(ws *types.Workspace, opStr string) (*types.RefactoringPlan, error) {
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
		req := types.RenameSymbolRequest{
			SymbolName: raw["symbol"],
			NewName:    raw["new_name"],
			Scope:      types.WorkspaceScope,
		}
		if pkg := raw["package"]; pkg != "" {
			req.Package = types.ResolvePackagePath(ws, pkg)
			req.Scope = types.PackageScope
		}
		return e.RenameSymbolAcrossConfigs(req)
	case "move_symbol":
		return e.MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
			SymbolName:  raw["symbol"],
			FromPackage: types.ResolvePackagePath(ws, raw["from_package"]),
			ToPackage:   types.ResolvePackagePath(ws, raw["to_package"]),
		})
	case "rename_package":
		resolved := types.ResolvePackagePath(ws, raw["package_path"])
		oldName := raw["old_name"]
		if pkg := ws.Packages[resolved]; oldName == "" && pkg != nil {
			oldName = pkg.Name
		}
		return e.RenamePackage(ws, types.RenamePackageRequest{
			OldPackageName: oldName,
			NewPackageName: raw["new_name"],
			PackagePath:    resolved,
			UpdateImports:  true,
		})
	case "move_package":
		return e.MovePackage(ws, types.MovePackageRequest{
			SourcePackage: types.ResolvePackagePath(ws, raw["source"]),
			TargetPackage: raw["target"],
			UpdateImports: true,
		})
	case "rename_method":
		return e.RenameMethodAcrossConfigs(types.RenameMethodRequest{
			TypeName:              raw["type_name"],
			MethodName:            raw["method_name"],
			NewMethodName:         raw["new_method_name"],
			PackagePath:           resolveOptionalPackage(ws, raw["package_path"]),
			UpdateImplementations: true,
		})
	case "rename_field":
		return e.RenameFieldAcrossConfigs(types.RenameFieldRequest{
			TypeName:     raw["type_name"],
			FieldName:    raw["field_name"],
			NewFieldName: raw["new_field_name"],
			PackagePath:  resolveOptionalPackage(ws, raw["package_path"]),
		})
	default:
		return nil, fmt.Errorf("unknown operation type: %s", opType)
	}
}

func resolveOptionalPackage(ws *types.Workspace, path string) string {
	if path == "" {
		return ""
	}
	return types.ResolvePackagePath(ws, path)
}

// planSteps plans every step against ws and merges the plans. It fails on the
// first step that does not plan, naming it by its position.
func (e *DefaultEngine) planSteps(ws *types.Workspace, steps []string) (*types.RefactoringPlan, error) {
	plans := make([]*types.RefactoringPlan, 0, len(steps))
	for i, step := range steps {
		plan, err := e.planStep(ws, step)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		plans = append(plans, plan)
	}
	return MergeStepPlans(plans)
}

// BatchOperationOperation implements executing multiple operations atomically
type BatchOperationOperation struct {
	Request types.BatchOperationRequest
	engine  *DefaultEngine
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
	plan, err := op.engine.planSteps(ws, op.Request.Operations)
	if err != nil {
		return nil, err
	}
	plan.Operations = []types.Operation{op}
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
	engine  *DefaultEngine
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

	steps := make([]string, 0, len(planFile.Steps))
	for i, step := range planFile.Steps {
		stepArgs := make(map[string]string)
		maps.Copy(stepArgs, step.Args)
		stepArgs["type"] = step.Type
		stepJSON, err := json.Marshal(stepArgs)
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to marshal: %w", i+1, err)
		}
		steps = append(steps, string(stepJSON))
	}

	plan, err := op.engine.planSteps(ws, steps)
	if err != nil {
		return nil, err
	}
	plan.Operations = []types.Operation{op}
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
