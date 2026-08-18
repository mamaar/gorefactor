package refactor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// packageAllFiles returns every file in pkg — both regular source files and
// _test.go files, which the parser keeps in separate maps. Move operations
// must treat them uniformly so tests are relocated and rewritten alongside
// the code they cover.
func packageAllFiles(pkg *types.Package) []*types.File {
	all := make([]*types.File, 0, len(pkg.Files)+len(pkg.TestFiles))
	for _, f := range pkg.Files {
		all = append(all, f)
	}
	for _, f := range pkg.TestFiles {
		all = append(all, f)
	}
	return all
}

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

	// The destination is resolved here as well as in Execute so an unresolvable
	// target (one escaping the workspace root, say) is refused before any
	// change is generated rather than surfacing mid-plan.
	targetPath, err := types.ResolveNewPackagePath(ws, op.Request.TargetPackage)
	if err != nil {
		return err
	}
	// Moving onto a package that already exists would merge two packages: files
	// landing alongside each other, same-named ones silently overwritten, and
	// duplicate declarations undetected until the build. Refuse and let the
	// caller pick a target or do the merge deliberately.
	if existing, ok := ws.Packages[targetPath]; ok {
		return fmt.Errorf("target package %s already exists at %s (package %q); "+
			"move_package will not merge into an existing package — choose a target that does not exist yet",
			op.Request.TargetPackage, targetPath, existing.Name)
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

	// Resolve the destination here rather than trusting the caller to have done
	// it. MovePackagesOperation.Execute builds MovePackageOperation values
	// directly, bypassing the engine, so resolving at any outer layer would
	// leave that path unresolved. Request.TargetPackage may be an absolute
	// path, a module import path, or a workspace-relative path; everything
	// below needs the absolute filesystem form.
	targetPackagePath, err := types.ResolveNewPackagePath(ws, op.Request.TargetPackage)
	if err != nil {
		return nil, fmt.Errorf("resolving target package %q: %w", op.Request.TargetPackage, err)
	}

	sourceImportPath := packagePathToImportPath(ws, sourcePackage.Path)
	targetImportPath := packagePathToImportPath(ws, targetPackagePath)
	targetPkgName := filepath.Base(targetPackagePath)
	if targetImportPath == "" {
		return nil, fmt.Errorf("cannot determine the import path for target package %s", targetPackagePath)
	}

	// Generate file move changes for each file in the source package,
	// including its _test.go files (which the parser stores separately in
	// TestFiles — omitting them would leave the tests behind).
	for _, file := range packageAllFiles(sourcePackage) {
		if len(file.OriginalContent) == 0 {
			continue
		}
		content := string(file.OriginalContent)
		// Replace package declaration. For an external test file
		// (package <name>_test) the same prefix replace correctly yields
		// <targetPkgName>_test.
		newContent := strings.Replace(content, "package "+sourcePackage.Name, "package "+targetPkgName, 1)
		// Rewrite the package's own import path within the moved content. This
		// is a no-op for ordinary files (a package never imports itself) but
		// is essential for an external test file (package <name>_test), which
		// imports the package under test and would otherwise land at the
		// target still importing the old path.
		if op.Request.UpdateImports && sourceImportPath != "" && targetImportPath != "" {
			newContent = strings.ReplaceAll(newContent, `"`+sourceImportPath+`"`, `"`+targetImportPath+`"`)
		}
		targetFilePath := filepath.Join(targetPackagePath, filepath.Base(file.Path))

		// Create file at target location
		plan.Changes = append(plan.Changes, types.Change{
			File:        targetFilePath,
			Start:       0,
			End:         0,
			OldText:     "",
			NewText:     newContent,
			Description: fmt.Sprintf("Move file %s to %s", file.Path, targetFilePath),
		})
		// Delete source file (moved to target)
		plan.Changes = append(plan.Changes, types.Change{
			File:        file.Path,
			Delete:      true,
			Description: fmt.Sprintf("Remove file %s (moved to %s)", file.Path, targetFilePath),
		})
		plan.AffectedFiles = append(plan.AffectedFiles, file.Path, targetFilePath)
	}

	// Update import paths in all dependent files
	if op.Request.UpdateImports && sourceImportPath != "" && targetImportPath != "" {
		quoted := `"` + sourceImportPath + `"`
		for _, pkg := range ws.Packages {
			// The source package's own files are already fully rewritten and
			// moved above; touching their old paths here would collide with
			// the delete change.
			if pkg == sourcePackage {
				continue
			}
			for _, file := range packageAllFiles(pkg) {
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

// dirPkgRewrite captures everything the move induces for one relocated
// package: its old→new import path, and — when the target directory's base
// name differs — its old→new package identifier.
type dirPkgRewrite struct {
	oldImportPath string
	newImportPath string
	oldName       string
	newName       string
	renamed       bool
}

func (op *MoveDirOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	sourceDirPath := filepath.Join(ws.RootPath, op.Request.SourceDir)
	targetDirPath := filepath.Join(ws.RootPath, op.Request.TargetDir)

	// Step 1: Find every package whose directory lies within the source dir.
	// The boundary-aware prefix check keeps "internal/foo" from also matching
	// a sibling "internal/foobar".
	var movedPackages []*types.Package
	for _, pkg := range ws.Packages {
		if pkg.Dir == sourceDirPath || strings.HasPrefix(pkg.Dir, sourceDirPath+string(filepath.Separator)) {
			movedPackages = append(movedPackages, pkg)
		}
	}
	if len(movedPackages) == 0 {
		return nil, fmt.Errorf("no packages found in source directory %s", op.Request.SourceDir)
	}

	// Step 2: Build the rewrite for each moved package and record which files
	// are physically moving (so the Step 4 sweep can skip them).
	rewrites := make([]dirPkgRewrite, 0, len(movedPackages))
	rewriteByDir := make(map[string]dirPkgRewrite, len(movedPackages))
	movedFilePaths := make(map[string]bool)

	for _, pkg := range movedPackages {
		rel, err := filepath.Rel(sourceDirPath, pkg.Dir)
		if err != nil {
			return nil, fmt.Errorf("computing target path for package %s: %w", pkg.Dir, err)
		}
		targetPkgDir := filepath.Join(targetDirPath, rel)
		newName := filepath.Base(targetPkgDir)
		rw := dirPkgRewrite{
			oldImportPath: packagePathToImportPath(ws, pkg.Path),
			newImportPath: packagePathToImportPath(ws, targetPkgDir),
			oldName:       pkg.Name,
			newName:       newName,
			// Only rename when the directory's base name changed AND is a
			// legal Go identifier; otherwise keep the original package name.
			renamed: newName != pkg.Name && isValidGoIdentifier(newName),
		}
		rewrites = append(rewrites, rw)
		rewriteByDir[pkg.Dir] = rw
		for _, file := range packageAllFiles(pkg) {
			movedFilePaths[file.Path] = true
		}
	}

	// rewriteContent applies, in a single pass, every moved package's
	// import-path rewrite followed by every renamed package's identifier
	// rewrite. Doing both here (rather than as separate positional changes)
	// is what lets each touched file become exactly one whole-file change,
	// avoiding the overlapping-change rejection in the serializer.
	rewriteContent := func(content string) string {
		for _, rw := range rewrites {
			if rw.oldImportPath != "" && rw.newImportPath != "" && rw.oldImportPath != rw.newImportPath {
				content = strings.ReplaceAll(content, `"`+rw.oldImportPath+`"`, `"`+rw.newImportPath+`"`)
			}
		}
		// Package-identifier rewrites run after the import paths so the
		// "does this file import the package" guard can key off the updated
		// path. Guarding on the import keeps a same-named local variable in an
		// unrelated file from being rewritten.
		for _, rw := range rewrites {
			if !rw.renamed || rw.newImportPath == "" {
				continue
			}
			if !strings.Contains(content, `"`+rw.newImportPath+`"`) {
				continue
			}
			content = strings.ReplaceAll(content, rw.oldName+".", rw.newName+".")
		}
		return content
	}

	// Step 3: Move every file (source + test) of each moved package. Each file
	// is rewritten once and its original deleted.
	for _, pkg := range movedPackages {
		self := rewriteByDir[pkg.Dir]
		for _, file := range packageAllFiles(pkg) {
			if len(file.OriginalContent) == 0 {
				continue
			}
			content := string(file.OriginalContent)
			if self.renamed {
				content = rewritePackageClause(ws, file, content, self.oldName, self.newName)
			}
			content = rewriteContent(content)

			rel := strings.TrimPrefix(file.Path, sourceDirPath+string(filepath.Separator))
			targetFilePath := filepath.Join(targetDirPath, rel)

			plan.Changes = append(plan.Changes, types.Change{
				File:        targetFilePath,
				Start:       0,
				End:         0,
				NewText:     content,
				Description: fmt.Sprintf("Move file from %s to %s", file.Path, targetFilePath),
			})
			plan.Changes = append(plan.Changes, types.Change{
				File:        file.Path,
				Delete:      true,
				Description: fmt.Sprintf("Remove file %s (moved to %s)", file.Path, targetFilePath),
			})
			plan.AffectedFiles = append(plan.AffectedFiles, file.Path, targetFilePath)
		}
	}

	// Step 4: Rewrite imports (and renamed-package references) in every other
	// workspace file that is not itself being moved.
	if op.Request.UpdateImports {
		for _, pkg := range ws.Packages {
			for _, file := range packageAllFiles(pkg) {
				if movedFilePaths[file.Path] || len(file.OriginalContent) == 0 {
					continue
				}
				content := string(file.OriginalContent)
				newContent := rewriteContent(content)
				if newContent == content {
					continue
				}
				plan.Changes = append(plan.Changes, types.Change{
					File:        file.Path,
					Start:       0,
					End:         len(file.OriginalContent),
					OldText:     content,
					NewText:     newContent,
					Description: fmt.Sprintf("Update references after moving %s to %s", op.Request.SourceDir, op.Request.TargetDir),
				})
				if !contains(plan.AffectedFiles, file.Path) {
					plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
				}
			}
		}
	}

	return plan, nil
}

// rewritePackageClause returns content with the package clause identifier
// changed from oldName to newName, located precisely via the file's AST so a
// stray "package oldName" inside a doc comment can't be hit by mistake. An
// external test package (package oldName_test) is renamed to newName_test.
// Returns content unchanged when the file has no usable AST or its package
// name matches neither form. content must be the file's original, unmodified
// source (the AST offsets are relative to it).
func rewritePackageClause(ws *types.Workspace, file *types.File, content, oldName, newName string) string {
	if file.AST == nil || file.AST.Name == nil || ws.FileSet == nil {
		return content
	}
	var replacement string
	switch file.AST.Name.Name {
	case oldName:
		replacement = newName
	case oldName + "_test":
		replacement = newName + "_test"
	default:
		return content
	}
	start := ws.FileSet.Position(file.AST.Name.Pos()).Offset
	end := ws.FileSet.Position(file.AST.Name.End()).Offset
	if start < 0 || end > len(content) || start >= end || content[start:end] != file.AST.Name.Name {
		return content
	}
	return content[:start] + replacement + content[end:]
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
		// A failed mapping must surface. Skipping it silently produces the
		// worst possible outcome for an "atomic" move: a plan that reports
		// success having quietly dropped part of what was asked for.
		if err := subOp.Validate(ws); err != nil {
			return nil, fmt.Errorf("moving %s to %s: %w", mapping.SourcePackage, mapping.TargetPackage, err)
		}
		subPlan, err := subOp.Execute(ws)
		if err != nil {
			return nil, fmt.Errorf("moving %s to %s: %w", mapping.SourcePackage, mapping.TargetPackage, err)
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
	// A facade package is new by definition, so its target can never resolve
	// against the existing package set; it has to be resolved as a path.
	facadeDir, err := types.ResolveNewPackagePath(ws, op.Request.TargetPackage)
	if err != nil {
		return nil, fmt.Errorf("resolving facade target package %q: %w", op.Request.TargetPackage, err)
	}
	facadePkgName := filepath.Base(facadeDir)

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Generate facade package content
	var facadeContent strings.Builder
	facadeContent.WriteString(fmt.Sprintf("// Package %s provides a facade for accessing related functionality.\n", facadePkgName))
	facadeContent.WriteString("// This file was generated by gorefactor.\n")
	facadeContent.WriteString(fmt.Sprintf("package %s\n\n", facadePkgName))

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

	facadeFile := filepath.Join(facadeDir, "facade.go")
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
		for line := range strings.SplitSeq(string(file.OriginalContent), "\n") {
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
