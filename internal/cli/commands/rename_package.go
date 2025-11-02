package commands

import (
	"fmt"
	"os"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/types"
)

// RenamePackageCommand handles package renaming operations
func RenamePackageCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: rename-package requires 3 arguments: <old-name> <new-name> <package-path>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor rename-package oldpkg newpkg pkg/path/to/package\n")
		fmt.Fprintf(os.Stderr, "Example: gorefactor rename-package auth authentication internal/auth\n")
		os.Exit(1)
	}

	oldPackageName := args[0]
	newPackageName := args[1]
	packagePath := args[2]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Check if the package exists
	if _, exists := workspace.Packages[packagePath]; !exists {
		fmt.Fprintf(os.Stderr, "Error: package not found: %s\n", packagePath)
		os.Exit(1)
	}

	// Create rename package request
	request := types.RenamePackageRequest{
		OldPackageName: oldPackageName,
		NewPackageName: newPackageName,
		PackagePath:    packagePath,
		UpdateImports:  true, // Always update imports by default
	}

	// Generate refactoring plan
	plan, err := engine.RenamePackage(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	description := fmt.Sprintf("Rename package %s to %s in %s", oldPackageName, newPackageName, packagePath)
	ProcessPlan(engine, plan, description)
}