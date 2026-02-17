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

	// Find source package
	var sourcePackage *types.Package
	for _, pkg := range ws.Packages {
		if pkg.Path == op.Request.SourcePackage ||
			strings.HasSuffix(pkg.Path, string(filepath.Separator)+op.Request.SourcePackage) {
			sourcePackage = pkg
			break
		}
	}
	if sourcePackage == nil {
		return nil, fmt.Errorf("source package %s not found", op.Request.SourcePackage)
	}

	sourceImportPath := packagePathToImportPath(ws, sourcePackage.Path)
	targetImportPath := packagePathToImportPath(ws, op.Request.TargetPackage)
	targetPkgName := filepath.Base(op.Request.TargetPackage)

	// Generate file move changes for each file in the source package
	for _, file := range sourcePackage.Files {
		if len(file.OriginalContent) == 0 {
			continue
		}
		content := string(file.OriginalContent)
		// Replace package declaration
		newContent := strings.Replace(content, "package "+sourcePackage.Name, "package "+targetPkgName, 1)
		targetFilePath := filepath.Join(op.Request.TargetPackage, filepath.Base(file.Path))

		// Create file at target location
		plan.Changes = append(plan.Changes, types.Change{
			File:        targetFilePath,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     newContent,
			Description: fmt.Sprintf("Move file %s to %s", file.Path, targetFilePath),
		})
		// Clear source file
		plan.Changes = append(plan.Changes, types.Change{
			File:        file.Path,
			Start:       0,
			End:         len(file.OriginalContent),
			OldText:     content,
			NewText:     "",
			Description: fmt.Sprintf("Remove file %s (moved to %s)", file.Path, targetFilePath),
		})
		plan.AffectedFiles = append(plan.AffectedFiles, file.Path, targetFilePath)
	}

	// Update import paths in all dependent files
	if op.Request.UpdateImports && sourceImportPath != "" && targetImportPath != "" {
		quoted := `"` + sourceImportPath + `"`
		for _, pkg := range ws.Packages {
			for _, file := range pkg.Files {
				if len(file.OriginalContent) == 0 {
					continue
				}
				content := string(file.OriginalContent)
				if !strings.Contains(content, quoted) {
					continue
				}
				newContent := strings.ReplaceAll(content, quoted, `"`+targetImportPath+`"`)
				if newContent != content {
					plan.Changes = append(plan.Changes, types.Change{
						File:        file.Path,
						Start:       0,
						End:         len(file.OriginalContent),
						OldText:     content,
						NewText:     newContent,
						Description: fmt.Sprintf("Update import path from %s to %s", sourceImportPath, targetImportPath),
					})
					if !contains(plan.AffectedFiles, file.Path) {
						plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
					}
				}
			}
		}
	}

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

	for _, mapping := range op.Request.Packages {
		subReq := types.MovePackageRequest{
			SourcePackage: mapping.SourcePackage,
			TargetPackage: mapping.TargetPackage,
			CreateTarget:  op.Request.CreateTargets,
			UpdateImports: op.Request.UpdateImports,
		}
		subOp := &MovePackageOperation{Request: subReq}
		if err := subOp.Validate(ws); err != nil {
			continue
		}
		subPlan, err := subOp.Execute(ws)
		if err != nil {
			continue
		}
		plan.Changes = append(plan.Changes, subPlan.Changes...)
		for _, f := range subPlan.AffectedFiles {
			if !contains(plan.AffectedFiles, f) {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
			}
		}
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
	facadeContent.WriteString("// This file was generated by gorefactor.\n")
	facadeContent.WriteString(fmt.Sprintf("package %s\n\n", filepath.Base(op.Request.TargetPackage)))

	// Collect unique source package imports
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

	// Add kind-appropriate re-export lines
	for _, export := range op.Request.Exports {
		outputName := export.Alias
		if outputName == "" {
			outputName = export.SymbolName
		}
		pkgAlias := filepath.Base(export.SourcePackage)
		kind := lookupSymbolKind(ws, export.SourcePackage, export.SymbolName)

		facadeContent.WriteString(fmt.Sprintf("// %s is re-exported from %s\n", outputName, export.SourcePackage))
		switch kind {
		case types.FunctionSymbol, types.VariableSymbol:
			facadeContent.WriteString(fmt.Sprintf("var %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName))
		case types.ConstantSymbol:
			facadeContent.WriteString(fmt.Sprintf("const %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName))
		default: // TypeSymbol, InterfaceSymbol, or unknown → type alias
			facadeContent.WriteString(fmt.Sprintf("type %s = %s.%s\n\n", outputName, pkgAlias, export.SymbolName))
		}
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

// lookupSymbolKind returns the SymbolKind for a named symbol within a source package
// identified by its Go import path. Returns TypeSymbol as a fallback.
func lookupSymbolKind(ws *types.Workspace, sourcePackageImportPath, symbolName string) types.SymbolKind {
	fsPath, ok := ws.ImportToPath[sourcePackageImportPath]
	if !ok {
		return types.TypeSymbol
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok || pkg.Symbols == nil {
		return types.TypeSymbol
	}
	sym := pkg.Symbols.FindSymbol(symbolName)
	if sym == nil {
		return types.TypeSymbol
	}
	return sym.Kind
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

	modulesDirAbs := filepath.Join(ws.RootPath, op.Request.ModulesDir)

	for _, pkg := range ws.Packages {
		if !strings.HasPrefix(pkg.Path, modulesDirAbs) {
			continue
		}

		// Collect exported symbols matching ExportTypes filter (empty = all)
		var exports []types.ExportSpec
		if pkg.Symbols != nil {
			for _, sym := range getAllExportedSymbols(pkg) {
				if len(op.Request.ExportTypes) > 0 {
					matched := false
					for _, et := range op.Request.ExportTypes {
						if strings.Contains(strings.ToLower(sym.Name), strings.ToLower(et)) {
							matched = true
							break
						}
					}
					if !matched {
						continue
					}
				}
				exports = append(exports, types.ExportSpec{
					SourcePackage: pkg.ImportPath,
					SymbolName:    sym.Name,
				})
			}
		}

		if len(exports) == 0 {
			continue
		}

		targetPkg := filepath.Join(ws.RootPath, op.Request.TargetDir, pkg.Name)
		subReq := types.CreateFacadeRequest{
			TargetPackage: targetPkg,
			Exports:       exports,
		}
		subOp := &CreateFacadeOperation{Request: subReq}
		if err := subOp.Validate(ws); err != nil {
			continue
		}
		subPlan, err := subOp.Execute(ws)
		if err != nil {
			continue
		}
		plan.Changes = append(plan.Changes, subPlan.Changes...)
		for _, f := range subPlan.AffectedFiles {
			if !contains(plan.AffectedFiles, f) {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
			}
		}
	}

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
	if len(op.Request.FacadePackages) == 0 && !op.Request.AutoDetect {
		return fmt.Errorf("no facade packages specified and auto_detect is false")
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

	facadePackages := op.Request.FacadePackages

	// Auto-detect facade packages if requested
	if op.Request.AutoDetect {
		for _, pkg := range ws.Packages {
			if isFacadePackage(pkg) {
				if !contains(facadePackages, pkg.Path) {
					facadePackages = append(facadePackages, pkg.Path)
				}
			}
		}
	}

	for _, facadePkgPath := range facadePackages {
		fsPkgPath := facadePkgPath
		if !filepath.IsAbs(fsPkgPath) {
			fsPkgPath = filepath.Join(ws.RootPath, fsPkgPath)
		}

		facadePkg, ok := ws.Packages[fsPkgPath]
		if !ok {
			continue
		}

		// Collect source packages re-exported by this facade
		sourceImports := collectFacadeSourceImports(facadePkg)
		if len(sourceImports) == 0 {
			continue
		}

		// Re-generate exports from each source package
		var exports []types.ExportSpec
		for _, importPath := range sourceImports {
			fsPath, ok := ws.ImportToPath[importPath]
			if !ok {
				continue
			}
			srcPkg, ok := ws.Packages[fsPath]
			if !ok || srcPkg.Symbols == nil {
				continue
			}
			for _, sym := range getAllExportedSymbols(srcPkg) {
				exports = append(exports, types.ExportSpec{
					SourcePackage: importPath,
					SymbolName:    sym.Name,
				})
			}
		}

		if len(exports) == 0 {
			continue
		}

		subReq := types.CreateFacadeRequest{
			TargetPackage: fsPkgPath,
			Exports:       exports,
		}
		subOp := &CreateFacadeOperation{Request: subReq}
		subPlan, err := subOp.Execute(ws)
		if err != nil {
			continue
		}
		plan.Changes = append(plan.Changes, subPlan.Changes...)
		for _, f := range subPlan.AffectedFiles {
			if !contains(plan.AffectedFiles, f) {
				plan.AffectedFiles = append(plan.AffectedFiles, f)
			}
		}
	}

	return plan, nil
}

// isFacadePackage returns true if all non-trivial lines in the package files are
// re-export declarations (type/var/const X = pkg.X).
func isFacadePackage(pkg *types.Package) bool {
	hasFiles := false
	for _, file := range pkg.Files {
		if len(file.OriginalContent) == 0 {
			continue
		}
		hasFiles = true
		for _, line := range strings.Split(string(file.OriginalContent), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "", strings.HasPrefix(trimmed, "//"),
				strings.HasPrefix(trimmed, "package "),
				strings.HasPrefix(trimmed, "import"),
				trimmed == ")", trimmed == "(":
				continue
			}
			isReexport := (strings.HasPrefix(trimmed, "type ") || strings.HasPrefix(trimmed, "var ") || strings.HasPrefix(trimmed, "const ")) &&
				strings.Contains(trimmed, " = ")
			if !isReexport {
				return false
			}
		}
	}
	return hasFiles
}

// collectFacadeSourceImports returns the distinct import paths used in the package's files.
func collectFacadeSourceImports(pkg *types.Package) []string {
	seen := make(map[string]bool)
	var imports []string
	for _, file := range pkg.Files {
		if file.AST == nil {
			continue
		}
		for _, imp := range file.AST.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !seen[importPath] {
				seen[importPath] = true
				imports = append(imports, importPath)
			}
		}
	}
	return imports
}
