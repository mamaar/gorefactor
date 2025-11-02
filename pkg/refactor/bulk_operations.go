package refactor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// MovePackageOperation implements moving entire packages
type MovePackageOperation struct {
	Request types.MovePackageRequest
}

func (op *MovePackageOperation) Type() types.OperationType {
	return types.MovePackageOperation
}

func (op *MovePackageOperation) Description() string {
	return fmt.Sprintf("Move package %s to %s", op.Request.SourcePackage, op.Request.TargetPackage)
}

func (op *MovePackageOperation) Validate(ws *types.Workspace) error {
	if op.Request.SourcePackage == "" {
		return fmt.Errorf("source package cannot be empty")
	}
	if op.Request.TargetPackage == "" {
		return fmt.Errorf("target package cannot be empty")
	}

	// Check if source package exists
	found := false
	for pkgPath := range ws.Packages {
		if strings.HasSuffix(pkgPath, op.Request.SourcePackage) || pkgPath == op.Request.SourcePackage {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("source package %s not found in workspace", op.Request.SourcePackage)
	}

	return nil
}

func (op *MovePackageOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// For now, create a placeholder implementation
	// This would need to:
	// 1. Find all symbols in the source package
	// 2. Create move operations for each symbol
	// 3. Update imports in all dependent files
	// 4. Create target package directory structure

	plan.Changes = append(plan.Changes, types.Change{
		File:        fmt.Sprintf("%s/README.md", op.Request.TargetPackage),
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     fmt.Sprintf("# Moved from %s\n\nThis package was moved from %s using gorefactor.\n", op.Request.SourcePackage, op.Request.SourcePackage),
		Description: fmt.Sprintf("Create placeholder file for package move from %s to %s", op.Request.SourcePackage, op.Request.TargetPackage),
	})

	plan.AffectedFiles = []string{fmt.Sprintf("%s/README.md", op.Request.TargetPackage)}

	return plan, nil
}

// MoveDirOperation implements moving directory structures
type MoveDirOperation struct {
	Request types.MoveDirRequest
}

func (op *MoveDirOperation) Type() types.OperationType {
	return types.MoveDirOperation
}

func (op *MoveDirOperation) Description() string {
	return fmt.Sprintf("Move directory %s to %s", op.Request.SourceDir, op.Request.TargetDir)
}

func (op *MoveDirOperation) Validate(ws *types.Workspace) error {
	if op.Request.SourceDir == "" {
		return fmt.Errorf("source directory cannot be empty")
	}
	if op.Request.TargetDir == "" {
		return fmt.Errorf("target directory cannot be empty")
	}

	// Check if source directory exists in workspace
	sourcePath := filepath.Join(ws.RootPath, op.Request.SourceDir)
	if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
		return fmt.Errorf("source directory %s does not exist", sourcePath)
	}

	return nil
}

func (op *MoveDirOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Step 1: Find all packages in source directory
	sourcePackages := make(map[string]*types.Package)
	sourceDirPath := filepath.Join(ws.RootPath, op.Request.SourceDir)
	
	for packagePath, pkg := range ws.Packages {
		// Check if the package's directory is within the source directory
		if strings.HasPrefix(pkg.Dir, sourceDirPath) {
			sourcePackages[packagePath] = pkg
		}
	}

	if len(sourcePackages) == 0 {
		return nil, fmt.Errorf("no packages found in source directory %s", op.Request.SourceDir)
	}

	// Step 2: Generate file move changes for each package
	for _, pkg := range sourcePackages {
		// Move each file in the package
		for _, file := range pkg.Files {
			if len(file.OriginalContent) == 0 {
				continue // Skip empty files
			}
			
			// Calculate target file path by replacing source dir with target dir in the file path
			sourceDirPath := filepath.Join(ws.RootPath, op.Request.SourceDir)
			targetDirPath := filepath.Join(ws.RootPath, op.Request.TargetDir)
			
			if !strings.HasPrefix(file.Path, sourceDirPath) {
				continue // Skip files not in source directory
			}
			
			// Get the relative path from source directory
			relativePath := strings.TrimPrefix(file.Path, sourceDirPath+string(filepath.Separator))
			targetFilePath := filepath.Join(targetDirPath, relativePath)
			
			// Create file move changes
			// 1. Create file in target location
			plan.Changes = append(plan.Changes, types.Change{
				File:        targetFilePath,
				Start:       0,
				End:         0,
				OldText:     "",
				NewText:     string(file.OriginalContent),
				Description: fmt.Sprintf("Move file from %s to %s", file.Path, targetFilePath),
			})
			
			// 2. Remove file from source location
			plan.Changes = append(plan.Changes, types.Change{
				File:        file.Path,
				Start:       0,
				End:         len(file.OriginalContent),
				OldText:     string(file.OriginalContent),
				NewText:     "",
				Description: fmt.Sprintf("Remove file %s (moved to %s)", file.Path, targetFilePath),
			})
			
			plan.AffectedFiles = append(plan.AffectedFiles, file.Path, targetFilePath)
		}
	}

	// Step 3: Update import paths in all other files
	if op.Request.UpdateImports {
		for packagePath, pkg := range ws.Packages {
			// Skip packages we're moving
			if strings.HasPrefix(packagePath, op.Request.SourceDir) {
				continue
			}
			
			// Check each file for imports that need updating
			for _, file := range pkg.Files {
				changes := op.generateImportPathUpdates(file, ws)
				plan.Changes = append(plan.Changes, changes...)
				for _, change := range changes {
					if !contains(plan.AffectedFiles, change.File) {
						plan.AffectedFiles = append(plan.AffectedFiles, change.File)
					}
				}
			}
		}
	}

	return plan, nil
}

// generateImportPathUpdates finds and updates import statements that reference the moved directory
func (op *MoveDirOperation) generateImportPathUpdates(file *types.File, ws *types.Workspace) []types.Change {
	var changes []types.Change
	
	if len(file.OriginalContent) == 0 {
		return changes
	}
	
	content := string(file.OriginalContent)
	lines := strings.Split(content, "\n")
	
	// Find import statements that need updating
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		
		// Look for import statements containing the source directory
		if strings.Contains(trimmed, fmt.Sprintf(`"%s/`, op.Request.SourceDir)) ||
		   strings.Contains(trimmed, fmt.Sprintf(`"%s"`, op.Request.SourceDir)) {
			
			// Calculate line position in bytes
			lineStart := 0
			for j := 0; j < i; j++ {
				lineStart += len(lines[j]) + 1 // +1 for newline
			}
			
			// Replace the source directory with target directory in the import path
			newLine := strings.ReplaceAll(line, 
				fmt.Sprintf(`"%s/`, op.Request.SourceDir),
				fmt.Sprintf(`"%s/`, op.Request.TargetDir))
			newLine = strings.ReplaceAll(newLine,
				fmt.Sprintf(`"%s"`, op.Request.SourceDir),
				fmt.Sprintf(`"%s"`, op.Request.TargetDir))
			
			if newLine != line {
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       lineStart,
					End:         lineStart + len(line),
					OldText:     line,
					NewText:     newLine,
					Description: fmt.Sprintf("Update import path from %s to %s", op.Request.SourceDir, op.Request.TargetDir),
				})
			}
		}
	}
	
	return changes
}


// MovePackagesOperation implements moving multiple packages atomically
type MovePackagesOperation struct {
	Request types.MovePackagesRequest
}

func (op *MovePackagesOperation) Type() types.OperationType {
	return types.MovePackagesOperation
}

func (op *MovePackagesOperation) Description() string {
	return fmt.Sprintf("Move %d packages to %s", len(op.Request.Packages), op.Request.TargetDir)
}

func (op *MovePackagesOperation) Validate(ws *types.Workspace) error {
	if len(op.Request.Packages) == 0 {
		return fmt.Errorf("no packages specified for move operation")
	}
	if op.Request.TargetDir == "" {
		return fmt.Errorf("target directory cannot be empty")
	}

	// Validate each package mapping
	for i, mapping := range op.Request.Packages {
		if mapping.SourcePackage == "" {
			return fmt.Errorf("source package at index %d cannot be empty", i)
		}
		if mapping.TargetPackage == "" {
			return fmt.Errorf("target package at index %d cannot be empty", i)
		}

		// Check if source package exists
		found := false
		for pkgPath := range ws.Packages {
			if strings.HasSuffix(pkgPath, mapping.SourcePackage) || pkgPath == mapping.SourcePackage {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("source package %s not found in workspace", mapping.SourcePackage)
		}
	}

	return nil
}

func (op *MovePackagesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Placeholder implementation - this would orchestrate individual package moves
	for _, mapping := range op.Request.Packages {
		plan.Changes = append(plan.Changes, types.Change{
			File:        fmt.Sprintf("%s/README.md", mapping.TargetPackage),
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     fmt.Sprintf("# Moved from %s\n", mapping.SourcePackage),
			Description: fmt.Sprintf("Create placeholder for package move from %s to %s", mapping.SourcePackage, mapping.TargetPackage),
		})

		plan.AffectedFiles = append(plan.AffectedFiles, fmt.Sprintf("%s/README.md", mapping.TargetPackage))
	}

	return plan, nil
}

// CreateFacadeOperation implements creating facade packages
type CreateFacadeOperation struct {
	Request types.CreateFacadeRequest
}

func (op *CreateFacadeOperation) Type() types.OperationType {
	return types.CreateFacadeOperation
}

func (op *CreateFacadeOperation) Description() string {
	return fmt.Sprintf("Create facade package %s with %d exports", op.Request.TargetPackage, len(op.Request.Exports))
}

func (op *CreateFacadeOperation) Validate(ws *types.Workspace) error {
	if op.Request.TargetPackage == "" {
		return fmt.Errorf("target package cannot be empty")
	}
	if len(op.Request.Exports) == 0 {
		return fmt.Errorf("no exports specified for facade")
	}

	// Validate each export spec
	for i, export := range op.Request.Exports {
		if export.SourcePackage == "" {
			return fmt.Errorf("source package at export index %d cannot be empty", i)
		}
		if export.SymbolName == "" {
			return fmt.Errorf("symbol name at export index %d cannot be empty", i)
		}
	}

	return nil
}

func (op *CreateFacadeOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Generate facade package content
	var facadeContent strings.Builder
	facadeContent.WriteString(fmt.Sprintf("// Package %s provides a facade for accessing related functionality.\n", filepath.Base(op.Request.TargetPackage)))
	facadeContent.WriteString(fmt.Sprintf("// This file was generated by gorefactor.\n"))
	facadeContent.WriteString(fmt.Sprintf("package %s\n\n", filepath.Base(op.Request.TargetPackage)))

	// Add imports
	imports := make(map[string]bool)
	for _, export := range op.Request.Exports {
		imports[export.SourcePackage] = true
	}

	if len(imports) > 0 {
		facadeContent.WriteString("import (\n")
		for imp := range imports {
			facadeContent.WriteString(fmt.Sprintf("\t\"%s\"\n", imp))
		}
		facadeContent.WriteString(")\n\n")
	}

	// Add type aliases/re-exports
	for _, export := range op.Request.Exports {
		alias := export.Alias
		if alias == "" {
			alias = export.SymbolName
		}
		
		// This is a simplified approach - in reality we'd need to determine
		// the symbol type (type, const, var, func) and generate appropriate re-exports
		facadeContent.WriteString(fmt.Sprintf("// %s is re-exported from %s\n", alias, export.SourcePackage))
		facadeContent.WriteString(fmt.Sprintf("type %s = %s.%s\n\n", alias, filepath.Base(export.SourcePackage), export.SymbolName))
	}

	facadeFile := filepath.Join(op.Request.TargetPackage, "facade.go")
	plan.Changes = append(plan.Changes, types.Change{
		File:        facadeFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     facadeContent.String(),
		Description: fmt.Sprintf("Create facade package %s", op.Request.TargetPackage),
	})

	plan.AffectedFiles = []string{facadeFile}

	return plan, nil
}

// GenerateFacadesOperation implements auto-generating facades
type GenerateFacadesOperation struct {
	Request types.GenerateFacadesRequest
}

func (op *GenerateFacadesOperation) Type() types.OperationType {
	return types.GenerateFacadesOperation
}

func (op *GenerateFacadesOperation) Description() string {
	return fmt.Sprintf("Generate facades for modules in %s", op.Request.ModulesDir)
}

func (op *GenerateFacadesOperation) Validate(ws *types.Workspace) error {
	if op.Request.ModulesDir == "" {
		return fmt.Errorf("modules directory cannot be empty")
	}
	if op.Request.TargetDir == "" {
		return fmt.Errorf("target directory cannot be empty")
	}

	return nil
}

func (op *GenerateFacadesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Placeholder implementation - would scan modules directory and generate facades
	// For now, just create a sample facade
	facadeContent := fmt.Sprintf(`// Package generated provides facades for modules in %s
// This file was generated by gorefactor.
package generated

// TODO: Auto-generate facades for modules in %s
// Export types: %s
`,
		op.Request.ModulesDir,
		op.Request.ModulesDir,
		strings.Join(op.Request.ExportTypes, ", "))

	facadeFile := filepath.Join(op.Request.TargetDir, "generated.go")
	plan.Changes = append(plan.Changes, types.Change{
		File:        facadeFile,
		Start:       0,
		End:         0,
		OldText:     "",
		NewText:     facadeContent,
		Description: fmt.Sprintf("Generate facades for modules in %s", op.Request.ModulesDir),
	})

	plan.AffectedFiles = []string{facadeFile}

	return plan, nil
}

// UpdateFacadesOperation implements updating existing facades
type UpdateFacadesOperation struct {
	Request types.UpdateFacadesRequest
}

func (op *UpdateFacadesOperation) Type() types.OperationType {
	return types.UpdateFacadesOperation
}

func (op *UpdateFacadesOperation) Description() string {
	return fmt.Sprintf("Update facades in %s", strings.Join(op.Request.FacadePackages, ", "))
}

func (op *UpdateFacadesOperation) Validate(ws *types.Workspace) error {
	if len(op.Request.FacadePackages) == 0 {
		return fmt.Errorf("no facade packages specified")
	}

	return nil
}

func (op *UpdateFacadesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Placeholder implementation - would analyze and update existing facades
	for _, facadePkg := range op.Request.FacadePackages {
		updateFile := filepath.Join(facadePkg, "updated.md")
		plan.Changes = append(plan.Changes, types.Change{
			File:        updateFile,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     fmt.Sprintf("# Updated facade in %s\n\nThis facade was updated by gorefactor.\n", facadePkg),
			Description: fmt.Sprintf("Update facade in %s", facadePkg),
		})

		plan.AffectedFiles = append(plan.AffectedFiles, updateFile)
	}

	return plan, nil
}