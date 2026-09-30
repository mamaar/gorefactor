package refactor

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

// batchStep is one parsed batch step: the packages it refers to and how to plan it.
type batchStep struct {
	typ string
	// packages are the resolved packages the step refers to; an empty entry
	// stands for "any package" (the step is workspace-wide).
	packages []string
	// renamesPackage is set for steps that rename or move a package.
	renamesPackage bool
	plan           func() (*types.RefactoringPlan, error)
}

// parseStep parses one batch step, a JSON object with a "type" field. ws
// resolves package paths and is the workspace of the steps that plan in the
// default configuration only.
func (e *DefaultEngine) parseStep(ws *types.Workspace, opStr string) (*batchStep, error) {
	var raw map[string]string
	if err := json.Unmarshal([]byte(opStr), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse operation JSON: %w", err)
	}

	opType, ok := raw["type"]
	if !ok {
		return nil, fmt.Errorf("operation JSON missing 'type' field")
	}

	step := &batchStep{typ: opType}
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
		step.packages = []string{req.Package}
		step.plan = func() (*types.RefactoringPlan, error) { return e.RenameSymbolAcrossConfigs(req) }
	case "move_symbol":
		req := types.MoveSymbolRequest{
			SymbolName:  raw["symbol"],
			FromPackage: types.ResolvePackagePath(ws, raw["from_package"]),
			ToPackage:   types.ResolvePackagePath(ws, raw["to_package"]),
		}
		step.packages = []string{resolveOptionalPackage(ws, raw["from_package"]), resolveOptionalPackage(ws, raw["to_package"])}
		step.plan = func() (*types.RefactoringPlan, error) { return e.MoveSymbolAcrossConfigs(req) }
	case "rename_package":
		resolved := types.ResolvePackagePath(ws, raw["package_path"])
		oldName := raw["old_name"]
		if pkg := ws.Packages[resolved]; oldName == "" && pkg != nil {
			oldName = pkg.Name
		}
		req := types.RenamePackageRequest{
			OldPackageName: oldName,
			NewPackageName: raw["new_name"],
			PackagePath:    resolved,
			UpdateImports:  true,
		}
		step.packages = []string{resolveOptionalPackage(ws, raw["package_path"])}
		step.renamesPackage = true
		step.plan = func() (*types.RefactoringPlan, error) { return e.RenamePackage(ws, req) }
	case "move_package":
		req := types.MovePackageRequest{
			SourcePackage: types.ResolvePackagePath(ws, raw["source"]),
			TargetPackage: raw["target"],
			UpdateImports: true,
		}
		step.packages = []string{resolveOptionalPackage(ws, raw["source"]), resolveOptionalPackage(ws, raw["target"])}
		step.renamesPackage = true
		step.plan = func() (*types.RefactoringPlan, error) { return e.MovePackage(ws, req) }
	case "rename_method":
		req := types.RenameMethodRequest{
			TypeName:              raw["type_name"],
			MethodName:            raw["method_name"],
			NewMethodName:         raw["new_method_name"],
			PackagePath:           resolveOptionalPackage(ws, raw["package_path"]),
			UpdateImplementations: true,
		}
		step.packages = []string{req.PackagePath}
		step.plan = func() (*types.RefactoringPlan, error) { return e.RenameMethodAcrossConfigs(req) }
	case "rename_field":
		req := types.RenameFieldRequest{
			TypeName:     raw["type_name"],
			FieldName:    raw["field_name"],
			NewFieldName: raw["new_field_name"],
			PackagePath:  resolveOptionalPackage(ws, raw["package_path"]),
		}
		step.packages = []string{req.PackagePath}
		step.plan = func() (*types.RefactoringPlan, error) { return e.RenameFieldAcrossConfigs(req) }
	default:
		return nil, fmt.Errorf("unknown operation type: %s", opType)
	}
	return step, nil
}

// findPackageStepConflict refuses a batch in which a step that renames or moves
// a package shares a package with another step, or any step is workspace-wide.
// Every step is planned against the unmodified workspace, so such steps can
// invalidate each other without their edits overlapping.
func findPackageStepConflict(steps []*batchStep) error {
	for i, a := range steps {
		if !a.renamesPackage {
			continue
		}
		for j, b := range steps {
			if i == j || (b.renamesPackage && j < i) {
				continue
			}
			for _, pa := range a.packages {
				for _, pb := range b.packages {
					if pa != "" && pb != "" && pa != pb {
						continue
					}
					lo, hi, first, second := i, j, a, b
					if j < i {
						lo, hi, first, second = j, i, b, a
					}
					what := fmt.Sprintf("both refer to package %q", pa)
					if pa == "" || pb == "" {
						what = "cannot be shown to touch different packages, as one names no package"
					}
					return &types.RefactorError{
						Type: types.InvalidOperation,
						Message: fmt.Sprintf(
							"step %d (%s) and step %d (%s) %s; a package rename or move invalidates other steps planned against the unmodified workspace, so run them as separate batch_operations calls",
							lo+1, first.typ, hi+1, second.typ, what),
					}
				}
			}
		}
	}
	return nil
}

func resolveOptionalPackage(ws *types.Workspace, path string) string {
	if path == "" {
		return ""
	}
	return types.ResolvePackagePath(ws, path)
}

// planSteps parses every step, refuses package-step conflicts, plans each step
// against ws and merges the plans. It fails on the first step that does not
// parse or plan, naming it by its position.
func (e *DefaultEngine) planSteps(ws *types.Workspace, steps []string) (*types.RefactoringPlan, error) {
	parsed := make([]*batchStep, 0, len(steps))
	for i, raw := range steps {
		step, err := e.parseStep(ws, raw)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		parsed = append(parsed, step)
	}
	if err := findPackageStepConflict(parsed); err != nil {
		return nil, err
	}
	plans := make([]*types.RefactoringPlan, 0, len(parsed))
	for i, step := range parsed {
		plan, err := step.plan()
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
