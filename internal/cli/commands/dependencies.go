package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/types"
)

// MoveByDependenciesCommand moves symbols based on dependency analysis
func MoveByDependenciesCommand(args []string) {
	moveSharedTo := ""
	keepInternal := []string{}
	analyzeOnly := false
	workspaceDir := "."

	// Parse arguments
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--move-shared-to":
			if i+1 < len(args) {
				moveSharedTo = args[i+1]
				i++
			}
		case "--keep-internal":
			if i+1 < len(args) {
				keepInternal = strings.Split(args[i+1], ",")
				i++
			}
		case "--analyze-only":
			analyzeOnly = true
		case "--workspace":
			if i+1 < len(args) {
				workspaceDir = args[i+1]
				i++
			}
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create move by dependencies request
	request := types.MoveByDependenciesRequest{
		Workspace:    workspaceDir,
		MoveSharedTo: moveSharedTo,
		KeepInternal: keepInternal,
		AnalyzeOnly:  analyzeOnly,
	}

	// Generate refactoring plan
	plan, err := engine.MoveByDependencies(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := "Move symbols based on dependency analysis"
	if analyzeOnly {
		description = "Analyze dependencies and generate report"
	}
	ProcessPlan(engine, plan, description)
}

// OrganizeByLayersCommand organizes packages by architectural layers
func OrganizeByLayersCommand(args []string) {
	domainLayer := ""
	infrastructureLayer := ""
	applicationLayer := ""
	reorderImports := false
	workspaceDir := "."

	// Parse arguments
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--domain":
			if i+1 < len(args) {
				domainLayer = args[i+1]
				i++
			}
		case "--infrastructure":
			if i+1 < len(args) {
				infrastructureLayer = args[i+1]
				i++
			}
		case "--application":
			if i+1 < len(args) {
				applicationLayer = args[i+1]
				i++
			}
		case "--reorder-imports":
			reorderImports = true
		case "--workspace":
			if i+1 < len(args) {
				workspaceDir = args[i+1]
				i++
			}
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create organize by layers request
	request := types.OrganizeByLayersRequest{
		Workspace:           workspaceDir,
		DomainLayer:         domainLayer,
		InfrastructureLayer: infrastructureLayer,
		ApplicationLayer:    applicationLayer,
		ReorderImports:      reorderImports,
	}

	// Generate refactoring plan
	plan, err := engine.OrganizeByLayers(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, "Organize packages by architectural layers")
}

// FixCyclesCommand detects and fixes circular dependencies
func FixCyclesCommand(args []string) {
	autoFix := false
	outputReport := ""
	workspaceDir := "."

	// Parse arguments
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--auto-fix":
			autoFix = true
		case "--output-report":
			if i+1 < len(args) {
				outputReport = args[i+1]
				i++
			}
		case "--workspace":
			if i+1 < len(args) {
				workspaceDir = args[i+1]
				i++
			}
		default:
			// If no flags, treat as workspace directory
			if !strings.HasPrefix(args[i], "--") {
				workspaceDir = args[i]
			}
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create fix cycles request
	request := types.FixCyclesRequest{
		Workspace:    workspaceDir,
		AutoFix:      autoFix,
		OutputReport: outputReport,
	}

	// Generate refactoring plan
	plan, err := engine.FixCycles(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := "Detect circular dependencies"
	if autoFix {
		description = "Detect and fix circular dependencies"
	}
	ProcessPlan(engine, plan, description)
}

// AnalyzeDependenciesCommand analyzes dependency flow and suggests improvements
func AnalyzeDependenciesCommand(args []string) {
	detectBackwardsDeps := false
	suggestMoves := false
	outputFile := ""
	workspaceDir := "."

	// Parse arguments
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--detect-backwards-deps":
			detectBackwardsDeps = true
		case "--suggest-moves":
			suggestMoves = true
		case "--output":
			if i+1 < len(args) {
				outputFile = args[i+1]
				i++
			}
		case "--workspace":
			if i+1 < len(args) {
				workspaceDir = args[i+1]
				i++
			}
		default:
			// If no flags, treat as workspace directory
			if !strings.HasPrefix(args[i], "--") {
				workspaceDir = args[i]
			}
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create analyze dependencies request
	request := types.AnalyzeDependenciesRequest{
		Workspace:           workspaceDir,
		DetectBackwardsDeps: detectBackwardsDeps,
		SuggestMoves:       suggestMoves,
		OutputFile:         outputFile,
	}

	// Generate refactoring plan
	plan, err := engine.AnalyzeDependencies(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, "Analyze dependency flow and suggest improvements")
}