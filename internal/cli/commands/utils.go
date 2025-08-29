package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ProcessPlan handles the common logic for processing refactoring plans
func ProcessPlan(engine refactor.RefactorEngine, plan *types.RefactoringPlan, description string) {
	// Check for issues
	hasErrors := false
	hasWarnings := false

	if plan.Impact != nil && len(plan.Impact.PotentialIssues) > 0 {
		for _, issue := range plan.Impact.PotentialIssues {
			switch issue.Severity {
			case types.Error:
				hasErrors = true
			case types.Warning:
				hasWarnings = true
			}
		}
	}

	// Display plan information
	if *cli.GlobalFlags.Json {
		OutputJSON(plan)
		if hasErrors {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("Refactoring Plan: %s\n", description)
	fmt.Printf("=================\n")

	// Show affected files
	if len(plan.AffectedFiles) > 0 {
		fmt.Printf("\nAffected Files (%d):\n", len(plan.AffectedFiles))
		for _, file := range plan.AffectedFiles {
			fmt.Printf("  - %s\n", file)
		}
	}

	// Show changes summary
	if len(plan.Changes) > 0 {
		fmt.Printf("\nChanges to Apply (%d):\n", len(plan.Changes))
		if *cli.GlobalFlags.Verbose {
			for i, change := range plan.Changes {
				fmt.Printf("  %d. %s\n", i+1, change.Description)
				fmt.Printf("     File: %s [%d:%d]\n", change.File, change.Start, change.End)
			}
		}
	}

	// Show issues
	if plan.Impact != nil && len(plan.Impact.PotentialIssues) > 0 {
		fmt.Printf("\nIssues Found:\n")
		for _, issue := range plan.Impact.PotentialIssues {
			prefix := "  "
			switch issue.Severity {
			case types.Error:
				prefix = "  ERROR: "
			case types.Warning:
				prefix = "  WARN:  "
			case types.Info:
				prefix = "  INFO:  "
			}
			fmt.Printf("%s%s\n", prefix, issue.Description)
			if issue.File != "" {
				fmt.Printf("        at %s:%d\n", issue.File, issue.Line)
			}
		}
	}

	// Check if we should proceed
	if hasErrors {
		fmt.Fprintf(os.Stderr, "\nCannot proceed: errors found in refactoring plan\n")
		os.Exit(1)
	}

	if hasWarnings && !*cli.GlobalFlags.Force {
		fmt.Fprintf(os.Stderr, "\nWarnings found. Use --force to proceed anyway\n")
		os.Exit(1)
	}

	// Preview or execute
	if *cli.GlobalFlags.DryRun {
		fmt.Println("\nDry run mode - no changes will be applied")

		// Show preview
		preview, err := engine.PreviewPlan(plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating preview: %v\n", err)
			os.Exit(1)
		}

		if *cli.GlobalFlags.Verbose {
			fmt.Printf("\nDetailed Preview:\n%s\n", preview)
		}
	} else {
		// Create backups if requested
		if *cli.GlobalFlags.Backup {
			fmt.Println("\nCreating backup files...")
			serializer := refactor.NewSerializer()
			backups := make(map[string]string)

			for _, file := range plan.AffectedFiles {
				backupPath, err := serializer.BackupFile(file)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error creating backup for %s: %v\n", file, err)
					// Restore any backups we've already created
					for original, backup := range backups {
						_ = serializer.RestoreFromBackup(original, backup)
					}
					os.Exit(1)
				}
				backups[file] = backupPath
				if *cli.GlobalFlags.Verbose {
					fmt.Printf("  Backed up %s to %s\n", file, backupPath)
				}
			}
		}

		// Execute the plan
		fmt.Println("\nApplying changes...")
		err := engine.ExecutePlan(plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error executing plan: %v\n", err)

			// If it's a validation error, show the issues
			if valErr, ok := err.(*types.ValidationError); ok {
				fmt.Fprintf(os.Stderr, "\nValidation Issues:\n")
				for i, issue := range valErr.Issues {
					fmt.Fprintf(os.Stderr, "  %d. %s: %s\n", i+1, issue.Severity.String(), issue.Description)
					if issue.File != "" {
						fmt.Fprintf(os.Stderr, "     File: %s", issue.File)
						if issue.Line > 0 {
							fmt.Fprintf(os.Stderr, ":%d", issue.Line)
						}
						fmt.Fprintf(os.Stderr, "\n")
					}
				}
			}
			os.Exit(1)
		}

		fmt.Printf("\nRefactoring completed successfully!\n")
		fmt.Printf("Modified %d files\n", len(plan.AffectedFiles))
	}
}

// ExecuteOperation is a helper function to execute operations
func ExecuteOperation(engine refactor.RefactorEngine, workspace *types.Workspace, operation types.Operation) {
	// Validate the operation
	if err := operation.Validate(workspace); err != nil {
		fmt.Fprintf(os.Stderr, "Validation error: %v\n", err)
		os.Exit(1)
	}

	// Execute the operation to get the plan
	plan, err := operation.Execute(workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing operation: %v\n", err)
		os.Exit(1)
	}

	// Show preview if in dry-run mode
	if *cli.GlobalFlags.DryRun {
		fmt.Println("=== DRY RUN MODE ===")
		fmt.Printf("Operation: %s\n", operation.Description())
		fmt.Printf("Affected files: %d\n", len(plan.AffectedFiles))

		if *cli.GlobalFlags.Verbose {
			for _, file := range plan.AffectedFiles {
				fmt.Printf("  - %s\n", file)
			}
		}

		fmt.Printf("Changes: %d\n", len(plan.Changes))
		if *cli.GlobalFlags.Verbose {
			for i, change := range plan.Changes {
				fmt.Printf("  %d. %s: %s\n", i+1, change.File, change.Description)
			}
		}
		return
	}

	// Validate the plan
	if err := engine.ValidateRefactoring(plan); err != nil {
		fmt.Fprintf(os.Stderr, "Plan validation error: %v\n", err)
		os.Exit(1)
	}

	// Execute the plan
	fmt.Printf("Executing: %s\n", operation.Description())
	err = engine.ExecutePlan(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing plan: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully completed operation.\n")
	fmt.Printf("Modified %d files.\n", len(plan.AffectedFiles))
}

// FindSymbolInPackage finds a symbol in a package
func FindSymbolInPackage(pkg *types.Package, symbolName string) *types.Symbol {
	if pkg.Symbols == nil {
		return nil
	}

	// Check functions
	if symbol, exists := pkg.Symbols.Functions[symbolName]; exists {
		return symbol
	}

	// Check types
	if symbol, exists := pkg.Symbols.Types[symbolName]; exists {
		return symbol
	}

	// Check variables
	if symbol, exists := pkg.Symbols.Variables[symbolName]; exists {
		return symbol
	}

	// Check constants
	if symbol, exists := pkg.Symbols.Constants[symbolName]; exists {
		return symbol
	}

	// Check methods (need to search all receiver types)
	for _, methods := range pkg.Symbols.Methods {
		for _, method := range methods {
			if method.Name == symbolName {
				return method
			}
		}
	}

	return nil
}

// GetSymbolKindName returns a human-readable name for a symbol kind
func GetSymbolKindName(kind types.SymbolKind) string {
	switch kind {
	case types.FunctionSymbol:
		return "Function"
	case types.MethodSymbol:
		return "Method"
	case types.TypeSymbol:
		return "Type"
	case types.VariableSymbol:
		return "Variable"
	case types.ConstantSymbol:
		return "Constant"
	case types.InterfaceSymbol:
		return "Interface"
	case types.StructFieldSymbol:
		return "Struct Field"
	case types.PackageSymbol:
		return "Package"
	default:
		return "Unknown"
	}
}

// OutputJSON outputs data as JSON
func OutputJSON(data interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// ResolvePackagePath resolves a user-provided package reference to an actual workspace package key
func ResolvePackagePath(workspace *types.Workspace, userPath string) string {
	// Strategy 1: Try exact match (for absolute paths)
	if _, exists := workspace.Packages[userPath]; exists {
		return userPath
	}

	// Strategy 2: Try to find by Go package name
	for pkgPath, pkg := range workspace.Packages {
		if pkg.Name == userPath {
			return pkgPath
		}
	}

	// Strategy 3: Try relative to workspace root
	absPath := filepath.Join(workspace.RootPath, userPath)
	if _, exists := workspace.Packages[absPath]; exists {
		return absPath
	}

	// Strategy 4: Try as "." for current directory
	if userPath == "." {
		if _, exists := workspace.Packages[workspace.RootPath]; exists {
			return workspace.RootPath
		}
	}

	// If nothing matches, return the user input (will trigger helpful error message)
	return userPath
}