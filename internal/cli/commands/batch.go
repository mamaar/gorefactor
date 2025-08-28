package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// BatchCommand executes multiple operations atomically
func BatchCommand(args []string) {
	operations := []string{}
	rollbackOnFailure := false
	dryRun := false

	// Parse arguments
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--operation":
			if i+1 < len(args) {
				operations = append(operations, args[i+1])
				i += 2
			} else {
				i++
			}
		case "--rollback-on-failure":
			rollbackOnFailure = true
			i++
		case "--dry-run":
			dryRun = true
			i++
		default:
			i++
		}
	}

	if len(operations) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no operations specified for batch execution\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor batch --operation \"move-package src dest\" --operation \"clean-aliases\" [--rollback-on-failure] [--dry-run]\n")
		os.Exit(1)
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create batch operation request
	request := types.BatchOperationRequest{
		Operations:       operations,
		RollbackOnFailure: rollbackOnFailure,
		DryRun:           dryRun,
	}

	// Generate refactoring plan
	plan, err := engine.BatchOperations(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := fmt.Sprintf("Execute %d operations atomically", len(operations))
	if dryRun {
		description = "Preview batch operations"
	}
	ProcessPlan(engine, plan, description)
}

// PlanCommand creates a refactoring plan file
func PlanCommand(args []string) {
	outputFile := ""
	dryRun := false
	operations := []types.PlanStep{}

	// Parse arguments
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--output":
			if i+1 < len(args) {
				outputFile = args[i+1]
				i += 2
			} else {
				i++
			}
		case "--dry-run":
			dryRun = true
			i++
		case "--move-shared":
			if i+2 < len(args) {
				operations = append(operations, types.PlanStep{
					Type: "move-shared",
					Args: map[string]string{
						"from": args[i+1],
						"to":   args[i+2],
					},
				})
				i += 3
			} else {
				i++
			}
		case "--create-facades":
			if i+2 < len(args) {
				operations = append(operations, types.PlanStep{
					Type: "create-facades",
					Args: map[string]string{
						"modules": args[i+1],
						"target":  args[i+2],
					},
				})
				i += 3
			} else {
				i++
			}
		default:
			i++
		}
	}

	if outputFile == "" {
		outputFile = "refactor-plan.json"
	}

	if len(operations) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no operations specified for plan creation\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor plan --move-shared src dest --create-facades modules target --output plan.json [--dry-run]\n")
		os.Exit(1)
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create plan operation request
	request := types.PlanOperationRequest{
		Operations: operations,
		OutputFile: outputFile,
		DryRun:     dryRun,
	}

	// Generate refactoring plan
	plan, err := engine.CreatePlan(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := fmt.Sprintf("Create refactoring plan with %d steps", len(operations))
	ProcessPlan(engine, plan, description)
}

// ExecuteCommand executes a refactoring plan from file
func ExecuteCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: execute requires a plan file\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor execute refactor-plan.json\n")
		os.Exit(1)
	}

	planFile := args[0]

	// Create execute operation request
	request := types.ExecuteOperationRequest{
		PlanFile: planFile,
	}

	// Load workspace (needed for engine operations)
	engine := cli.CreateEngineWithFlags()

	// Generate refactoring plan
	plan, err := engine.ExecutePlanFromFile(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing plan from file: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Execute refactoring plan from %s", planFile))
}

// RollbackCommand rolls back previous operations
func RollbackCommand(args []string) {
	lastBatch := false
	toStep := 0

	// Parse arguments
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--last-batch":
			lastBatch = true
		case "--to-step":
			if i+1 < len(args) {
				var err error
				toStep, err = strconv.Atoi(args[i+1])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid step number: %s\n", args[i+1])
					os.Exit(1)
				}
				i++
			}
		}
	}

	if !lastBatch && toStep <= 0 {
		fmt.Fprintf(os.Stderr, "Error: must specify either --last-batch or --to-step N\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor rollback --last-batch\n")
		fmt.Fprintf(os.Stderr, "       gorefactor rollback --to-step 5\n")
		os.Exit(1)
	}

	// Load workspace (needed for engine operations)
	engine := cli.CreateEngineWithFlags()

	// Create rollback operation request
	request := types.RollbackOperationRequest{
		LastBatch: lastBatch,
		ToStep:    toStep,
	}

	// Generate refactoring plan
	plan, err := engine.RollbackOperations(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating rollback plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := "Rollback operations"
	if lastBatch {
		description = "Rollback last batch operation"
	} else {
		description = fmt.Sprintf("Rollback to step %d", toStep)
	}
	ProcessPlan(engine, plan, description)
}

// ChangeCommand handles change operations like signature changes
func ChangeCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: change requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor change <signature> [arguments...]\n")
		os.Exit(1)
	}

	changeType := args[0]
	remainingArgs := args[1:]

	switch changeType {
	case "signature":
		ChangeSignatureCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown change type: %s\n", changeType)
		fmt.Fprintf(os.Stderr, "Valid types: signature\n")
		os.Exit(1)
	}
}

// ChangeSignatureCommand changes function signatures
func ChangeSignatureCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: change signature requires at least 3 arguments: <function-name> <file> <new-params>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor change signature myFunc main.go 'param1:string,param2:int' [returns]\n")
		os.Exit(1)
	}

	functionName := args[0]
	sourceFile := args[1]
	paramsStr := args[2]

	// Parse parameters
	var newParams []refactor.Parameter
	if paramsStr != "" {
		paramPairs := strings.Split(paramsStr, ",")
		for _, pair := range paramPairs {
			parts := strings.Split(strings.TrimSpace(pair), ":")
			if len(parts) == 2 {
				newParams = append(newParams, refactor.Parameter{
					Name: strings.TrimSpace(parts[0]),
					Type: strings.TrimSpace(parts[1]),
				})
			}
		}
	}

	// Parse return types
	var newReturns []string
	if len(args) > 3 {
		returnStr := args[3]
		if returnStr != "" {
			newReturns = strings.Split(returnStr, ",")
			for i, ret := range newReturns {
				newReturns[i] = strings.TrimSpace(ret)
			}
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Determine scope
	scope := types.WorkspaceScope
	if *cli.GlobalFlags.PackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.ChangeSignatureOperation{
		FunctionName: functionName,
		SourceFile:   sourceFile,
		NewParams:    newParams,
		NewReturns:   newReturns,
		Scope:        scope,
	}

	ExecuteOperation(engine, workspace, operation)
}