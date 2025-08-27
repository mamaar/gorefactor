package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// CleanAliasesOperation implements cleaning import aliases
type CleanAliasesOperation struct {
	Request types.CleanAliasesRequest
}

func (op *CleanAliasesOperation) Type() types.OperationType {
	return types.CleanAliasesOperation
}

func (op *CleanAliasesOperation) Description() string {
	return fmt.Sprintf("Clean import aliases in workspace %s", op.Request.Workspace)
}

func (op *CleanAliasesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	return nil
}

func (op *CleanAliasesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Analyze all files for import aliases
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			changes := op.cleanFileAliases(file)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *CleanAliasesOperation) cleanFileAliases(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Walk through import declarations
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if importSpec, ok := n.(*ast.ImportSpec); ok && importSpec.Name != nil {
			// Found an aliased import
			alias := importSpec.Name.Name
			importPath := strings.Trim(importSpec.Path.Value, `"`)

			// Skip dot imports and blank imports
			if alias == "." || alias == "_" {
				return true
			}

			// For now, create a placeholder change - in reality we'd need to:
			// 1. Check if removing alias causes conflicts
			// 2. Update all references to use full package name
			// 3. Remove the alias from import statement

			if !op.Request.PreserveConflicts || !op.wouldCauseConflict(alias, importPath, file) {
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Name.Pos()),
					End:         int(importSpec.Name.End()),
					OldText:     alias,
					NewText:     "",
					Description: fmt.Sprintf("Remove import alias '%s' for package %s", alias, importPath),
				})
			}
		}
		return true
	})

	return changes
}

func (op *CleanAliasesOperation) wouldCauseConflict(alias, importPath string, file *types.File) bool {
	// Placeholder implementation - would check for naming conflicts
	// This would need to analyze the symbol usage in the file
	return false
}

// StandardizeImportsOperation implements standardizing import aliases
type StandardizeImportsOperation struct {
	Request types.StandardizeImportsRequest
}

func (op *StandardizeImportsOperation) Type() types.OperationType {
	return types.StandardizeImportsOperation
}

func (op *StandardizeImportsOperation) Description() string {
	return fmt.Sprintf("Standardize imports in workspace %s with %d rules", op.Request.Workspace, len(op.Request.Rules))
}

func (op *StandardizeImportsOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	if len(op.Request.Rules) == 0 {
		return fmt.Errorf("no alias rules specified")
	}

	for i, rule := range op.Request.Rules {
		if rule.PackagePattern == "" {
			return fmt.Errorf("package pattern at rule index %d cannot be empty", i)
		}
		if rule.Alias == "" {
			return fmt.Errorf("alias at rule index %d cannot be empty", i)
		}
	}

	return nil
}

func (op *StandardizeImportsOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Apply standardization rules to all files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			changes := op.standardizeFileImports(file)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *StandardizeImportsOperation) standardizeFileImports(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Match import paths against rules and standardize aliases
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if importSpec, ok := n.(*ast.ImportSpec); ok {
			importPath := strings.Trim(importSpec.Path.Value, `"`)

			// Find matching rule
			for _, rule := range op.Request.Rules {
				if op.matchesPattern(importPath, rule.PackagePattern) {
					currentAlias := ""
					if importSpec.Name != nil {
						currentAlias = importSpec.Name.Name
					}

					if currentAlias != rule.Alias {
						// Need to update alias
						if importSpec.Name != nil {
							// Update existing alias
							changes = append(changes, types.Change{
								File:        file.Path,
								Start:       int(importSpec.Name.Pos()),
								End:         int(importSpec.Name.End()),
								OldText:     currentAlias,
								NewText:     rule.Alias,
								Description: fmt.Sprintf("Standardize import alias for %s from '%s' to '%s'", importPath, currentAlias, rule.Alias),
							})
						} else {
							// Add new alias
							changes = append(changes, types.Change{
								File:        file.Path,
								Start:       int(importSpec.Path.Pos()),
								End:         int(importSpec.Path.Pos()),
								OldText:     "",
								NewText:     rule.Alias + " ",
								Description: fmt.Sprintf("Add import alias '%s' for package %s", rule.Alias, importPath),
							})
						}
					}
					break
				}
			}
		}
		return true
	})

	return changes
}

func (op *StandardizeImportsOperation) matchesPattern(importPath, pattern string) bool {
	// Simple pattern matching - could be enhanced with glob patterns
	return strings.Contains(importPath, pattern) || importPath == pattern
}

// ResolveAliasConflictsOperation implements resolving import alias conflicts
type ResolveAliasConflictsOperation struct {
	Request types.ResolveAliasConflictsRequest
}

func (op *ResolveAliasConflictsOperation) Type() types.OperationType {
	return types.ResolveAliasConflictsOperation
}

func (op *ResolveAliasConflictsOperation) Description() string {
	return fmt.Sprintf("Resolve alias conflicts in workspace %s", op.Request.Workspace)
}

func (op *ResolveAliasConflictsOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	return nil
}

func (op *ResolveAliasConflictsOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Detect and resolve conflicts in each file
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			conflicts := op.detectConflicts(file)
			changes := op.resolveConflicts(file, conflicts)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *ResolveAliasConflictsOperation) detectConflicts(file *types.File) map[string][]string {
	conflicts := make(map[string][]string) // alias -> []importPaths

	if file.AST == nil {
		return conflicts
	}

	// Find all aliases and group by alias name
	aliases := make(map[string][]string)
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if importSpec, ok := n.(*ast.ImportSpec); ok && importSpec.Name != nil {
			alias := importSpec.Name.Name
			if alias != "." && alias != "_" {
				importPath := strings.Trim(importSpec.Path.Value, `"`)
				aliases[alias] = append(aliases[alias], importPath)
			}
		}
		return true
	})

	// Find conflicts (same alias, different import paths)
	for alias, paths := range aliases {
		if len(paths) > 1 {
			conflicts[alias] = paths
		}
	}

	return conflicts
}

func (op *ResolveAliasConflictsOperation) resolveConflicts(file *types.File, conflicts map[string][]string) []types.Change {
	var changes []types.Change

	for alias, paths := range conflicts {
		switch op.Request.Strategy {
		case types.UseFullNames:
			// Remove all aliases and use full package names
			for _, importPath := range paths {
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       0, // Placeholder - would need to find actual position
					End:         0,
					OldText:     alias,
					NewText:     "",
					Description: fmt.Sprintf("Remove conflicting alias '%s' for %s, use full package name", alias, importPath),
				})
			}

		case types.UseShortestUnique:
			// Generate unique short aliases
			for i, importPath := range paths {
				parts := strings.Split(importPath, "/")
				newAlias := fmt.Sprintf("%s%d", parts[len(parts)-1], i+1)
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       0, // Placeholder
					End:         0,
					OldText:     alias,
					NewText:     newAlias,
					Description: fmt.Sprintf("Resolve alias conflict: change '%s' to '%s' for %s", alias, newAlias, importPath),
				})
			}

		case types.UseCustomAlias:
			// Would need additional logic to generate custom aliases
			// For now, fall back to shortest unique
			for i, importPath := range paths {
				parts := strings.Split(importPath, "/")
				newAlias := fmt.Sprintf("%s%d", parts[len(parts)-1], i+1)
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       0, // Placeholder
					End:         0,
					OldText:     alias,
					NewText:     newAlias,
					Description: fmt.Sprintf("Resolve alias conflict: change '%s' to '%s' for %s", alias, newAlias, importPath),
				})
			}
		}
	}

	return changes
}

// ConvertAliasesOperation implements converting between aliased and non-aliased imports
type ConvertAliasesOperation struct {
	Request types.ConvertAliasesRequest
}

func (op *ConvertAliasesOperation) Type() types.OperationType {
	return types.ConvertAliasesOperation
}

func (op *ConvertAliasesOperation) Description() string {
	if op.Request.ToFullNames {
		return fmt.Sprintf("Convert aliases to full package names in workspace %s", op.Request.Workspace)
	} else {
		return fmt.Sprintf("Convert full package names to aliases in workspace %s", op.Request.Workspace)
	}
}

func (op *ConvertAliasesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	if !op.Request.ToFullNames && !op.Request.FromFullNames {
		return fmt.Errorf("must specify either ToFullNames or FromFullNames")
	}
	return nil
}

func (op *ConvertAliasesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Convert aliases in all files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			var changes []types.Change
			if op.Request.ToFullNames {
				changes = op.convertToFullNames(file)
			} else {
				changes = op.convertToAliases(file)
			}
			
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *ConvertAliasesOperation) convertToFullNames(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Find all aliased imports and convert references to full names
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if importSpec, ok := n.(*ast.ImportSpec); ok && importSpec.Name != nil {
			alias := importSpec.Name.Name
			if alias != "." && alias != "_" {
				importPath := strings.Trim(importSpec.Path.Value, `"`)
				packageName := filepath.Base(importPath)

				// Remove the alias from import
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Name.Pos()),
					End:         int(importSpec.Name.End() + token.Pos(len(" "))), // Include space after alias
					OldText:     alias + " ",
					NewText:     "",
					Description: fmt.Sprintf("Remove alias '%s' for package %s", alias, importPath),
				})

				// Would need additional logic to find and replace all references
				// from alias.Symbol to packageName.Symbol
				// This is a complex operation that requires symbol resolution
			}
		}
		return true
	})

	return changes
}

func (op *ConvertAliasesOperation) convertToAliases(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Find all non-aliased imports and add appropriate aliases
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if importSpec, ok := n.(*ast.ImportSpec); ok && importSpec.Name == nil {
			importPath := strings.Trim(importSpec.Path.Value, `"`)
			packageName := filepath.Base(importPath)
			
			// Generate a reasonable alias
			alias := op.generateAlias(importPath)
			
			if alias != packageName {
				// Add alias to import
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Path.Pos()),
					End:         int(importSpec.Path.Pos()),
					OldText:     "",
					NewText:     alias + " ",
					Description: fmt.Sprintf("Add alias '%s' for package %s", alias, importPath),
				})
			}
		}
		return true
	})

	return changes
}

func (op *ConvertAliasesOperation) generateAlias(importPath string) string {
	// Simple alias generation - could be enhanced with more sophisticated rules
	parts := strings.Split(importPath, "/")
	packageName := parts[len(parts)-1]
	
	// For common patterns, generate reasonable aliases
	if strings.Contains(importPath, "github.com") && len(parts) >= 3 {
		return strings.ToLower(parts[len(parts)-1][:3])  // First 3 characters
	}
	
	return packageName
}