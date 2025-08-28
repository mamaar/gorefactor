package refactor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

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
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	if op.Request.DryRun {
		// Just create a plan preview
		for i, operation := range op.Request.Operations {
			plan.Changes = append(plan.Changes, types.Change{
				File:        fmt.Sprintf("batch_operation_%d.md", i+1),
				Start:       0,
				End:         0,
				OldText:     "",
				NewText:     fmt.Sprintf("# Batch Operation %d\n\nCommand: %s\n\nThis operation would be executed atomically as part of a batch.\n", i+1, operation),
				Description: fmt.Sprintf("Batch operation %d: %s", i+1, operation),
			})
			plan.AffectedFiles = append(plan.AffectedFiles, fmt.Sprintf("batch_operation_%d.md", i+1))
		}
	} else {
		// Execute all operations
		// This is a placeholder - in reality, this would parse each operation string,
		// create the appropriate operation objects, and execute them
		
		// Create a batch execution log
		logFile := filepath.Join(ws.RootPath, "batch_execution.log")
		logContent := fmt.Sprintf("Batch Execution Log\n===================\n\nStart Time: %s\n\nOperations:\n", time.Now().Format(time.RFC3339))
		
		for i, operation := range op.Request.Operations {
			logContent += fmt.Sprintf("%d. %s\n", i+1, operation)
		}
		
		logContent += fmt.Sprintf("\nStatus: %s\n", "Ready for execution")
		if op.Request.RollbackOnFailure {
			logContent += "Rollback: Enabled\n"
		}
		
		plan.Changes = append(plan.Changes, types.Change{
			File:        logFile,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     logContent,
			Description: "Create batch execution log",
		})
		
		plan.AffectedFiles = []string{logFile}
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
	Version   string                `json:"version"`
	CreatedAt string                `json:"created_at"`
	Workspace string                `json:"workspace"`
	Steps     []types.PlanStep      `json:"steps"`
	DryRun    bool                  `json:"dry_run"`
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
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Read the plan file
	planData, err := os.ReadFile(op.Request.PlanFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read plan file: %w", err)
	}
	
	var planFile RefactoringPlanFile
	if err := json.Unmarshal(planData, &planFile); err != nil {
		return nil, fmt.Errorf("failed to parse plan file: %w", err)
	}
	
	// Create execution log
	logFile := filepath.Join(ws.RootPath, "plan_execution.log")
	logContent := fmt.Sprintf("Plan Execution Log\n==================\n\nPlan File: %s\n", op.Request.PlanFile)
	logContent += fmt.Sprintf("Plan Version: %s\n", planFile.Version)
	logContent += fmt.Sprintf("Plan Created: %s\n", planFile.CreatedAt)
	logContent += fmt.Sprintf("Execution Time: %s\n\n", time.Now().Format(time.RFC3339))
	
	logContent += "Steps to Execute:\n"
	for i, step := range planFile.Steps {
		logContent += fmt.Sprintf("%d. %s\n", i+1, step.Type)
		for key, value := range step.Args {
			logContent += fmt.Sprintf("   %s: %s\n", key, value)
		}
	}
	
	plan.Changes = append(plan.Changes, types.Change{
		File:        logFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     logContent,
		Description: "Create plan execution log",
	})
	
	plan.AffectedFiles = []string{logFile}
	
	// Execute each step (placeholder implementation)
	for i, step := range planFile.Steps {
		stepLogFile := fmt.Sprintf("step_%d_execution.log", i+1)
		stepContent := fmt.Sprintf("Step %d Execution\n================\n\nType: %s\nStatus: Executed\n", i+1, step.Type)
		
		plan.Changes = append(plan.Changes, types.Change{
			File:        stepLogFile,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     stepContent,
			Description: fmt.Sprintf("Execute step %d: %s", i+1, step.Type),
		})
		
		plan.AffectedFiles = append(plan.AffectedFiles, stepLogFile)
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

func (op *RollbackOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    false, // Rollback operations are typically not reversible
	}

	// Create rollback log
	logFile := filepath.Join(ws.RootPath, "rollback.log")
	logContent := fmt.Sprintf("Rollback Operation Log\n=====================\n\nRollback Time: %s\n", time.Now().Format(time.RFC3339))
	
	if op.Request.LastBatch {
		logContent += "Type: Rollback last batch\n"
		logContent += "Status: Initiated\n\n"
		logContent += "This operation will restore the workspace to the state before the last batch operation.\n"
	} else {
		logContent += fmt.Sprintf("Type: Rollback to step %d\n", op.Request.ToStep)
		logContent += "Status: Initiated\n\n"
		logContent += fmt.Sprintf("This operation will restore the workspace to the state after step %d.\n", op.Request.ToStep)
	}
	
	plan.Changes = append(plan.Changes, types.Change{
		File:        logFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     logContent,
		Description: "Create rollback operation log",
	})
	
	plan.AffectedFiles = []string{logFile}
	
	// In a real implementation, this would:
	// 1. Load the rollback state from backup files
	// 2. Apply reverse operations to undo changes
	// 3. Update workspace state
	
	// Create placeholder restore operations
	if op.Request.LastBatch {
		plan.Changes = append(plan.Changes, types.Change{
			File:        "restore_last_batch.md",
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     "# Restore Last Batch\n\nThis file represents the restoration of the workspace state before the last batch operation.\n",
			Description: "Restore workspace from last batch backup",
		})
		plan.AffectedFiles = append(plan.AffectedFiles, "restore_last_batch.md")
	}

	return plan, nil
}