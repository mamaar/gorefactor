package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/types"
)

// MoveCommand handles symbol and package movement operations
func MoveCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: move requires 3 arguments: <symbol> <from-package> <to-package>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor move MyFunction pkg/old pkg/new\n")
		os.Exit(1)
	}

	symbolName := args[0]
	fromPackage := args[1]
	toPackage := args[2]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages
	if *cli.GlobalFlags.Verbose {
		fmt.Fprintf(os.Stderr, "Workspace root: %s\n", workspace.RootPath)
		fmt.Fprintf(os.Stderr, "Loaded packages:\n")
		for path, pkg := range workspace.Packages {
			fmt.Fprintf(os.Stderr, "  %s -> %s (%s)\n", path, pkg.Name, pkg.Dir)
			if pkg.Name == "main" {
				fmt.Fprintf(os.Stderr, "    Files in main package:\n")
				for filename := range pkg.Files {
					fmt.Fprintf(os.Stderr, "      %s\n", filename)
				}
			}
		}
	}

	// Resolve package paths to actual workspace keys
	resolvedFromPackage := ResolvePackagePath(workspace, fromPackage)
	resolvedToPackage := ResolvePackagePath(workspace, toPackage)

	// Create move request
	request := types.MoveSymbolRequest{
		SymbolName:   symbolName,
		FromPackage:  resolvedFromPackage,
		ToPackage:    resolvedToPackage,
		CreateTarget: *cli.GlobalFlags.CreateTarget,
	}

	// Generate refactoring plan
	plan, err := engine.MoveSymbol(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Move %s from %s to %s", symbolName, fromPackage, toPackage))
}

// MovePackageCommand handles moving entire packages
func MovePackageCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: move-package requires 2 arguments: <source-package> <target-package>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor move-package internal/shared/command pkg/command\n")
		os.Exit(1)
	}

	sourcePackage := args[0]
	targetPackage := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create move package request
	request := types.MovePackageRequest{
		SourcePackage: sourcePackage,
		TargetPackage: targetPackage,
		CreateTarget:  *cli.GlobalFlags.CreateTarget,
		UpdateImports: true,
	}

	// Generate refactoring plan
	plan, err := engine.MovePackage(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Move package %s to %s", sourcePackage, targetPackage))
}

// MoveDirCommand handles moving entire directories
func MoveDirCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: move-dir requires 2 arguments: <source-dir> <target-dir>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor move-dir internal/shared pkg/infrastructure\n")
		os.Exit(1)
	}

	sourceDir := args[0]
	targetDir := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create move dir request
	request := types.MoveDirRequest{
		SourceDir:         sourceDir,
		TargetDir:         targetDir,
		PreserveStructure: true,
		UpdateImports:     true,
	}

	// Generate refactoring plan
	plan, err := engine.MoveDir(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Move directory %s to %s", sourceDir, targetDir))
}

// MovePackagesCommand handles moving multiple packages
func MovePackagesCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: move-packages requires at least 2 arguments: <package1,package2,...> <target-dir>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor move-packages internal/shared/command,internal/shared/events pkg/infrastructure/\n")
		os.Exit(1)
	}

	packagesStr := args[0]
	targetDir := args[1]

	// Parse packages
	packageNames := strings.Split(packagesStr, ",")
	var packageMappings []types.PackageMapping
	
	for _, pkg := range packageNames {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		
		// Extract package name from path for target
		parts := strings.Split(pkg, "/")
		packageName := parts[len(parts)-1]
		targetPackage := filepath.Join(targetDir, packageName)
		
		packageMappings = append(packageMappings, types.PackageMapping{
			SourcePackage: pkg,
			TargetPackage: targetPackage,
		})
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create move packages request
	request := types.MovePackagesRequest{
		Packages:      packageMappings,
		TargetDir:     targetDir,
		CreateTargets: *cli.GlobalFlags.CreateTarget,
		UpdateImports: true,
	}

	// Generate refactoring plan
	plan, err := engine.MovePackages(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Move packages to %s", targetDir))
}

// CreateFacadeCommand creates facade packages for modular architectures
func CreateFacadeCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: create-facade requires at least 3 arguments: <facade-package> --from <exports...>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor create-facade pkg/commission --from modules/commission/models.Commission --from modules/commission/commands.CreateCommand\n")
		os.Exit(1)
	}

	facadePackage := args[0]
	
	// Parse --from arguments
	var exports []types.ExportSpec
	for i := 1; i < len(args); i++ {
		if args[i] == "--from" && i+1 < len(args) {
			exportStr := args[i+1]
			parts := strings.Split(exportStr, ".")
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Error: invalid export format '%s'. Use format: package.Symbol\n", exportStr)
				os.Exit(1)
			}
			
			exports = append(exports, types.ExportSpec{
				SourcePackage: parts[0],
				SymbolName:    parts[1],
			})
			i++ // skip the export argument
		}
	}

	if len(exports) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no exports specified. Use --from package.Symbol format\n")
		os.Exit(1)
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create facade request
	request := types.CreateFacadeRequest{
		TargetPackage: facadePackage,
		Exports:       exports,
	}

	// Generate refactoring plan
	plan, err := engine.CreateFacade(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Create facade package %s", facadePackage))
}

// GenerateFacadesCommand generates facades for all modules
func GenerateFacadesCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: generate-facades requires 2 arguments: <modules-dir> <target-dir>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor generate-facades modules/ pkg/\n")
		os.Exit(1)
	}

	modulesDir := args[0]
	targetDir := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create generate facades request
	request := types.GenerateFacadesRequest{
		ModulesDir:  modulesDir,
		TargetDir:   targetDir,
		ExportTypes: []string{"commands", "models", "events"}, // default export types
	}

	// Generate refactoring plan
	plan, err := engine.GenerateFacades(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Generate facades for modules in %s", modulesDir))
}

// UpdateFacadesCommand updates existing facade packages
func UpdateFacadesCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: update-facades requires 1 argument: <facade-dir>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor update-facades pkg/\n")
		os.Exit(1)
	}

	facadeDir := args[0]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create update facades request
	request := types.UpdateFacadesRequest{
		FacadePackages: []string{facadeDir},
		AutoDetect:     true,
	}

	// Generate refactoring plan
	plan, err := engine.UpdateFacades(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Update facades in %s", facadeDir))
}

// CleanAliasesCommand removes unnecessary import aliases
func CleanAliasesCommand(args []string) {
	workspaceDir := "."
	if len(args) > 0 {
		workspaceDir = args[0]
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create clean aliases request
	request := types.CleanAliasesRequest{
		Workspace:         workspaceDir,
		PreserveConflicts: true,
	}

	// Generate refactoring plan
	plan, err := engine.CleanAliases(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, "Clean import aliases")
}

// StandardizeImportsCommand standardizes import aliases
func StandardizeImportsCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: standardize-imports requires at least 1 argument: --alias pattern=alias\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor standardize-imports --alias events=myproject/pkg/events --alias command=myproject/pkg/command\n")
		os.Exit(1)
	}

	// Parse alias rules
	var rules []types.AliasRule
	for i := 0; i < len(args); i++ {
		if args[i] == "--alias" && i+1 < len(args) {
			ruleStr := args[i+1]
			parts := strings.Split(ruleStr, "=")
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Error: invalid alias rule format '%s'. Use format: alias=package\n", ruleStr)
				os.Exit(1)
			}
			
			rules = append(rules, types.AliasRule{
				Alias:          parts[0],
				PackagePattern: parts[1],
			})
			i++ // skip the rule argument
		}
	}

	if len(rules) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no alias rules specified. Use --alias alias=package format\n")
		os.Exit(1)
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create standardize imports request
	request := types.StandardizeImportsRequest{
		Workspace: *cli.GlobalFlags.Workspace,
		Rules:     rules,
	}

	// Generate refactoring plan
	plan, err := engine.StandardizeImports(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, "Standardize import aliases")
}

// ResolveAliasConflictsCommand resolves conflicting import aliases
func ResolveAliasConflictsCommand(args []string) {
	workspaceDir := "."
	if len(args) > 0 {
		workspaceDir = args[0]
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create resolve alias conflicts request
	request := types.ResolveAliasConflictsRequest{
		Workspace: workspaceDir,
		Strategy:  types.UseFullNames,
	}

	// Generate refactoring plan
	plan, err := engine.ResolveAliasConflicts(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, "Resolve import alias conflicts")
}

// ConvertAliasesCommand converts between aliases and full names
func ConvertAliasesCommand(args []string) {
	toFullNames := false
	fromFullNames := false
	workspaceDir := "."

	// Parse arguments
	for _, arg := range args {
		switch arg {
		case "--to-full-names":
			toFullNames = true
		case "--from-full-names":
			fromFullNames = true
		case "--workspace":
			if len(args) > 1 {
				workspaceDir = args[len(args)-1]
			}
		}
	}

	if !toFullNames && !fromFullNames {
		toFullNames = true // default behavior
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(workspaceDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create convert aliases request
	request := types.ConvertAliasesRequest{
		Workspace:     workspaceDir,
		ToFullNames:   toFullNames,
		FromFullNames: fromFullNames,
	}

	// Generate refactoring plan
	plan, err := engine.ConvertAliases(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	var description string
	if toFullNames {
		description = "Convert aliases to full package names"
	} else {
		description = "Convert full package names to aliases"
	}
	ProcessPlan(engine, plan, description)
}