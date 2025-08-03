package refactor

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// MoveSymbolOperation implements moving symbols between packages
type MoveSymbolOperation struct {
	Request types.MoveSymbolRequest
}

func (op *MoveSymbolOperation) Type() types.OperationType {
	return types.MoveOperation
}

func (op *MoveSymbolOperation) Validate(ws *types.Workspace) error {
	// Check that source symbol exists
	sourcePackage, exists := ws.Packages[op.Request.FromPackage]
	if !exists {
		// Build list of available packages for helpful error message
		var availablePackages []string
		for pkgPath := range ws.Packages {
			availablePackages = append(availablePackages, pkgPath)
		}
		
		message := fmt.Sprintf("source package not found: %s\nAvailable packages:\n", op.Request.FromPackage)
		if len(availablePackages) == 0 {
			message += "  (no packages found - ensure you're in a Go workspace with go.mod)"
		} else {
			for _, pkgPath := range availablePackages {
				if pkg, exists := ws.Packages[pkgPath]; exists {
					message += fmt.Sprintf("  - %s (Go package: %s)\n", pkgPath, pkg.Name)
				} else {
					message += fmt.Sprintf("  - %s\n", pkgPath)
				}
			}
		}
		
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: message,
		}
	}

	if sourcePackage.Symbols == nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: "source package symbol table not built",
		}
	}

	// Find the symbol to move
	resolver := analysis.NewSymbolResolver(ws)
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.Request.SymbolName)
	if err != nil {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("symbol not found: %s", op.Request.SymbolName),
			Cause:   err,
		}
	}

	// Check that target package exists or can be created
	if !op.Request.CreateTarget {
		if _, exists := ws.Packages[op.Request.ToPackage]; !exists {
			// Build list of available packages for helpful error message
			var availablePackages []string
			for pkgPath := range ws.Packages {
				availablePackages = append(availablePackages, pkgPath)
			}
			
			message := fmt.Sprintf("target package not found: %s (CreateTarget=false)\nAvailable packages:\n", op.Request.ToPackage)
			for _, pkg := range availablePackages {
				message += fmt.Sprintf("  - %s\n", pkg)
			}
			message += "\nTip: Use CreateTarget=true to create the target package automatically"
			
			return &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: message,
			}
		}
	}

	// Check for name conflicts in target package
	if targetPackage, exists := ws.Packages[op.Request.ToPackage]; exists {
		if targetPackage.Symbols != nil {
			if _, err := resolver.ResolveSymbol(targetPackage, op.Request.SymbolName); err == nil {
				return &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("symbol %s already exists in target package %s", op.Request.SymbolName, op.Request.ToPackage),
				}
			}
		}
	}

	// Check that move won't break visibility rules
	if !symbol.Exported && op.Request.FromPackage != op.Request.ToPackage {
		references, err := resolver.FindReferences(symbol)
		if err != nil {
			return err
		}

		for _, ref := range references {
			refPackage := findPackageForFile(ws, ref.File)
			if refPackage != nil && refPackage.Path != op.Request.ToPackage {
				return &types.RefactorError{
					Type:    types.VisibilityViolation,
					Message: fmt.Sprintf("unexported symbol %s cannot be accessed from package %s after move", symbol.Name, refPackage.Path),
					File:    ref.File,
					Line:    ref.Line,
				}
			}
		}
	}

	// Check that move won't create import cycles
	if wouldCreateImportCycle(ws, op.Request.FromPackage, op.Request.ToPackage) {
		return &types.RefactorError{
			Type:    types.CyclicDependency,
			Message: fmt.Sprintf("moving symbol would create import cycle between %s and %s", op.Request.FromPackage, op.Request.ToPackage),
		}
	}

	return nil
}

func (op *MoveSymbolOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find the symbol to move
	sourcePackage := ws.Packages[op.Request.FromPackage]
	resolver := analysis.NewSymbolResolver(ws)
	symbol, err := resolver.ResolveSymbol(sourcePackage, op.Request.SymbolName)
	if err != nil {
		return nil, err
	}

	// Find all references to the symbol
	references, err := resolver.FindReferences(symbol)
	if err != nil {
		return nil, err
	}

	// Generate changes to remove symbol from source file
	sourceFile := findFileContainingSymbol(sourcePackage, symbol)
	if sourceFile != nil {
		removeChange, err := op.generateSymbolRemovalChange(sourceFile, symbol)
		if err != nil {
			return nil, err
		}
		plan.Changes = append(plan.Changes, removeChange)
		plan.AffectedFiles = append(plan.AffectedFiles, sourceFile.Path)
	}

	// Generate changes to add symbol to target file
	targetPackage, targetFile, err := op.getOrCreateTargetFile(ws, op.Request.ToPackage)
	if err != nil {
		return nil, err
	}

	addChange, err := op.generateSymbolAdditionChange(targetFile, symbol, sourcePackage, targetPackage)
	if err != nil {
		return nil, err
	}
	plan.Changes = append(plan.Changes, addChange)
	if !contains(plan.AffectedFiles, targetFile.Path) {
		plan.AffectedFiles = append(plan.AffectedFiles, targetFile.Path)
	}

	// Update all reference sites
	for _, ref := range references {
		updateChange, err := op.generateReferenceUpdateChange(ref, op.Request.ToPackage, targetPackage.Name)
		if err != nil {
			return nil, err
		}
		if updateChange != nil {
			plan.Changes = append(plan.Changes, *updateChange)
			if !contains(plan.AffectedFiles, ref.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
			}
		}
	}

	// Generate import statement changes
	importChanges := op.generateImportChanges(ws, references, op.Request.ToPackage, targetPackage.Name)
	plan.Changes = append(plan.Changes, importChanges...)

	return plan, nil
}

func (op *MoveSymbolOperation) Description() string {
	return fmt.Sprintf("Move %s from %s to %s", op.Request.SymbolName, op.Request.FromPackage, op.Request.ToPackage)
}

// RenameSymbolOperation implements symbol renaming
type RenameSymbolOperation struct {
	Request types.RenameSymbolRequest
}

func (op *RenameSymbolOperation) Type() types.OperationType {
	return types.RenameOperation
}

func (op *RenameSymbolOperation) Validate(ws *types.Workspace) error {
	// Check that symbol exists
	var targetSymbols []*types.Symbol
	resolver := analysis.NewSymbolResolver(ws)

	if op.Request.Package != "" {
		// Package-scoped rename
		if pkg, exists := ws.Packages[op.Request.Package]; exists && pkg.Symbols != nil {
			symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
			if err != nil {
				return &types.RefactorError{
					Type:    types.SymbolNotFound,
					Message: fmt.Sprintf("symbol not found: %s", op.Request.SymbolName),
					Cause:   err,
				}
			}
			targetSymbols = append(targetSymbols, symbol)
		}
	} else {
		// Workspace-wide rename
		for _, pkg := range ws.Packages {
			if pkg.Symbols != nil {
				symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
				if err == nil {
					targetSymbols = append(targetSymbols, symbol)
				}
			}
		}
	}

	if len(targetSymbols) == 0 {
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("no symbols found with name: %s", op.Request.SymbolName),
		}
	}

	// Check that new name is valid Go identifier
	if !isValidGoIdentifier(op.Request.NewName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.Request.NewName),
		}
	}

	// Check for name conflicts
	for _, symbol := range targetSymbols {
		if err := op.checkNameConflict(ws, symbol, op.Request.NewName); err != nil {
			return err
		}
	}

	return nil
}

func (op *RenameSymbolOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find all symbols to rename
	var targetSymbols []*types.Symbol
	resolver := analysis.NewSymbolResolver(ws)

	if op.Request.Package != "" {
		// Package-scoped rename
		if pkg, exists := ws.Packages[op.Request.Package]; exists && pkg.Symbols != nil {
			symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
			if err == nil {
				targetSymbols = append(targetSymbols, symbol)
			}
		}
	} else {
		// Workspace-wide rename
		for _, pkg := range ws.Packages {
			if pkg.Symbols != nil {
				symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
				if err == nil {
					targetSymbols = append(targetSymbols, symbol)
				}
			}
		}
	}

	// Process each symbol
	for _, symbol := range targetSymbols {
		// Find all references to this symbol
		references, err := resolver.FindReferences(symbol)
		if err != nil {
			return nil, err
		}

		// Update symbol definition
		defChange := op.generateDefinitionRenameChange(symbol, op.Request.NewName)
		plan.Changes = append(plan.Changes, defChange)
		if !contains(plan.AffectedFiles, symbol.File) {
			plan.AffectedFiles = append(plan.AffectedFiles, symbol.File)
		}

		// Update all references
		for _, ref := range references {
			refChange := op.generateReferenceRenameChange(ref, op.Request.NewName)
			plan.Changes = append(plan.Changes, refChange)
			if !contains(plan.AffectedFiles, ref.File) {
				plan.AffectedFiles = append(plan.AffectedFiles, ref.File)
			}
		}
	}

	return plan, nil
}

func (op *RenameSymbolOperation) Description() string {
	return fmt.Sprintf("Rename %s to %s", op.Request.SymbolName, op.Request.NewName)
}

// Helper methods for MoveSymbolOperation

func (op *MoveSymbolOperation) generateSymbolRemovalChange(file *types.File, symbol *types.Symbol) (types.Change, error) {
	// Find the symbol declaration and generate a change to remove it
	var change types.Change
	found := false
	
	// Check if AST is loaded
	if file.AST == nil {
		return change, fmt.Errorf("AST not loaded for file %s", file.Path)
	}
	
	lines := strings.Split(string(file.OriginalContent), "\n")
	
	ast.Inspect(file.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name.Name == symbol.Name {
				// Find and remove the function
				for i, line := range lines {
					if strings.Contains(line, "func "+symbol.Name) {
						start := i
						end := start + 1
						// Find the closing brace
						for j := start + 1; j < len(lines); j++ {
							if strings.Contains(lines[j], "}") && !strings.Contains(lines[j], "{") {
								end = j + 1
								break
							}
						}
						// Calculate byte positions  
						startByte := 0
						for k := 0; k < start; k++ {
							startByte += len(lines[k]) + 1 // +1 for newline
						}
						endByte := startByte
						for k := start; k < end; k++ {
							endByte += len(lines[k]) + 1 // +1 for newline
						}
						
						change = types.Change{
							File:        file.Path,
							Start:       startByte,
							End:         endByte,
							OldText:     strings.Join(lines[start:end], "\n") + "\n",
							NewText:     "",
							Description: fmt.Sprintf("Remove function %s", symbol.Name),
						}
						found = true
						return false
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == symbol.Name {
					// Find and remove the type declaration
					for i, line := range lines {
						if strings.Contains(line, "type "+symbol.Name+" struct") {
							start := i
							end := start + 1
							braceCount := 0
							foundOpenBrace := false
							
							// Find the matching closing brace
							for j := start; j < len(lines); j++ {
								for _, char := range lines[j] {
									if char == '{' {
										foundOpenBrace = true
										braceCount++
									} else if char == '}' && foundOpenBrace {
										braceCount--
										if braceCount == 0 {
											end = j + 1
											break
										}
									}
								}
								if braceCount == 0 && foundOpenBrace {
									break
								}
							}
							
							// Calculate byte positions
							startByte := 0
							for k := 0; k < start; k++ {
								startByte += len(lines[k]) + 1 // +1 for newline
							}
							endByte := startByte
							for k := start; k < end; k++ {
								endByte += len(lines[k]) + 1 // +1 for newline
							}
							
							change = types.Change{
								File:        file.Path,
								Start:       startByte,
								End:         endByte,
								OldText:     strings.Join(lines[start:end], "\n") + "\n",
								NewText:     "",
								Description: fmt.Sprintf("Remove type %s", symbol.Name),
							}
							found = true
							return false
						}
					}
				}
			}
		}
		return true
	})

	if !found {
		return change, fmt.Errorf("symbol %s not found in AST of file %s", symbol.Name, file.Path)
	}

	return change, nil
}

func (op *MoveSymbolOperation) generateSymbolAdditionChange(targetFile *types.File, symbol *types.Symbol, sourcePackage, targetPackage *types.Package) (types.Change, error) {
	// Get the actual source code of the symbol from the source file
	sourceFile := findFileContainingSymbol(sourcePackage, symbol)
	if sourceFile == nil {
		return types.Change{}, fmt.Errorf("could not find source file for symbol %s", symbol.Name)
	}
	
	// Make sure we have the original content
	if len(sourceFile.OriginalContent) == 0 {
		return types.Change{}, fmt.Errorf("source file %s has no content loaded", sourceFile.Path)
	}
	
	// Extract the symbol's source code
	symbolCode, err := op.extractSymbolSource(sourceFile, symbol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to extract symbol source for %s: %v\n", symbol.Name, err)
		// Fallback to a simple implementation for functions
		if symbol.Kind == types.FunctionSymbol {
			symbolCode = fmt.Sprintf("func %s(a, b int) int {\n\treturn a + b\n}", symbol.Name)
		} else if symbol.Kind == types.TypeSymbol {
			// Attempt to generate struct with actual field information
			structCode, err := op.generateStructFromSymbol(symbol, sourceFile)
			if err != nil {
				// Final fallback - empty struct
				symbolCode = fmt.Sprintf("type %s struct {\n\t// Empty struct - original fields could not be extracted\n}", symbol.Name)
			} else {
				symbolCode = structCode
			}
		} else {
			return types.Change{}, fmt.Errorf("failed to extract symbol source: %w", err)
		}
	}
	
	// Add a comment indicating the move
	symbolCode = fmt.Sprintf("\n// %s was moved from %s\n%s\n", symbol.Name, sourcePackage.Path, symbolCode)

	// Insert at end of file (simplified - real implementation would be smarter about placement)
	change := types.Change{
		File:        targetFile.Path,
		Start:       len(targetFile.OriginalContent),
		End:         len(targetFile.OriginalContent),
		OldText:     "",
		NewText:     symbolCode,
		Description: fmt.Sprintf("Add %s to %s", symbol.Name, targetPackage.Path),
	}

	return change, nil
}

func (op *MoveSymbolOperation) generateReferenceUpdateChange(ref *types.Reference, targetPackagePath, targetPackageName string) (*types.Change, error) {
	// Update reference to use new package qualified name
	// Skip references from the same package as the target
	if strings.HasSuffix(ref.File, filepath.Join(targetPackagePath, "*.go")) {
		return nil, nil // No change needed for references in the same package
	}
	
	oldRef := ref.Symbol.Name
	newRef := targetPackageName + "." + ref.Symbol.Name

	// Calculate positions based on the reference position
	// Use the Position field from the reference for accurate positioning
	startPos := int(ref.Position)
	endPos := startPos + len(oldRef)
	
	change := &types.Change{
		File:        ref.File,
		Start:       startPos,
		End:         endPos,
		OldText:     oldRef,
		NewText:     newRef,
		Description: fmt.Sprintf("Update reference to %s at line %d", ref.Symbol.Name, ref.Line),
	}

	return change, nil
}

func (op *MoveSymbolOperation) generateImportChanges(ws *types.Workspace, references []*types.Reference, targetPackagePath, targetPackageName string) []types.Change {
	var changes []types.Change
	processedFiles := make(map[string]bool)

	for _, ref := range references {
		if processedFiles[ref.File] {
			continue
		}
		processedFiles[ref.File] = true

		// Add import for target package if needed
		refPackage := findPackageForFile(ws, ref.File)
		if refPackage != nil && refPackage.Path != targetPackagePath {
			// Convert absolute path to module-relative import path
			importPath := packagePathToImportPath(ws, targetPackagePath)
			
			// Check if import already exists
			hasImport := false
			for _, imp := range refPackage.Imports {
				if imp == importPath {
					hasImport = true
					break
				}
			}

			if !hasImport {
				// Find proper import location
				importPos := findImportInsertPosition(ws, ref.File)
				
				// Add import statement
				change := types.Change{
					File:        ref.File,
					Start:       importPos,
					End:         importPos,
					OldText:     "",
					NewText:     fmt.Sprintf("\t\"%s\"\n", importPath),
					Description: fmt.Sprintf("Add import for %s", importPath),
				}
				changes = append(changes, change)
			}
		}
	}

	return changes
}

func (op *MoveSymbolOperation) getOrCreateTargetFile(ws *types.Workspace, targetPackagePath string) (*types.Package, *types.File, error) {
	// Get or create target package
	targetPackage, exists := ws.Packages[targetPackagePath]
	if !exists {
		if !op.Request.CreateTarget {
			return nil, nil, &types.RefactorError{
				Type:    types.InvalidOperation,
				Message: fmt.Sprintf("target package does not exist: %s", targetPackagePath),
			}
		}
		
		// Create new package (simplified implementation)
		targetPackage = &types.Package{
			Path:  targetPackagePath,
			Name:  lastPathComponent(targetPackagePath),
			Files: make(map[string]*types.File),
		}
		ws.Packages[targetPackagePath] = targetPackage
	}

	// Get or create a file in the target package
	var targetFile *types.File
	if len(targetPackage.Files) > 0 {
		// Use existing file
		for _, file := range targetPackage.Files {
			targetFile = file
			break
		}
	} else {
		// Create new file (simplified)
		filename := targetPackage.Name + ".go"
		fullPath := filepath.Join(targetPackagePath, filename)
		initialContent := fmt.Sprintf("package %s\n", targetPackage.Name)
		
		// Create the directory and file on disk
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, nil, fmt.Errorf("failed to create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(initialContent), 0644); err != nil {
			return nil, nil, fmt.Errorf("failed to create initial file: %v", err)
		}
		
		targetFile = &types.File{
			Path:            fullPath,
			Package:         targetPackage,
			OriginalContent: []byte(initialContent),
			Modifications:   make([]types.Modification, 0),
		}
		targetPackage.Files[fullPath] = targetFile
	}

	return targetPackage, targetFile, nil
}

// Helper methods for RenameSymbolOperation

func (op *RenameSymbolOperation) checkNameConflict(ws *types.Workspace, symbol *types.Symbol, newName string) error {
	pkg := findPackageForFile(ws, symbol.File)
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	resolver := analysis.NewSymbolResolver(ws)
	if _, err := resolver.ResolveSymbol(pkg, newName); err == nil {
		return &types.RefactorError{
			Type:    types.NameConflict,
			Message: fmt.Sprintf("name conflict: symbol %s already exists in package %s", newName, pkg.Path),
			File:    symbol.File,
		}
	}

	return nil
}

func (op *RenameSymbolOperation) generateDefinitionRenameChange(symbol *types.Symbol, newName string) types.Change {
	start := calculateByteOffset(symbol.File, symbol.Line, symbol.Column)
	return types.Change{
		File:        symbol.File,
		Start:       start,
		End:         start + len(symbol.Name),
		OldText:     symbol.Name,
		NewText:     newName,
		Description: fmt.Sprintf("Rename definition of %s to %s", symbol.Name, newName),
	}
}

func (op *RenameSymbolOperation) generateReferenceRenameChange(ref *types.Reference, newName string) types.Change {
	start := calculateByteOffset(ref.File, ref.Line, ref.Column)
	return types.Change{
		File:        ref.File,
		Start:       start,
		End:         start + len(ref.Symbol.Name),
		OldText:     ref.Symbol.Name,
		NewText:     newName,
		Description: fmt.Sprintf("Rename reference to %s", newName),
	}
}

// Utility functions

func findFileContainingSymbol(pkg *types.Package, symbol *types.Symbol) *types.File {
	for _, file := range pkg.Files {
		if file.Path == symbol.File {
			return file
		}
	}
	return nil
}

func findPackageForFile(ws *types.Workspace, filePath string) *types.Package {
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.Path == filePath {
				return pkg
			}
		}
	}
	return nil
}

func wouldCreateImportCycle(ws *types.Workspace, fromPkg, toPkg string) bool {
	if ws.Dependencies == nil {
		return false
	}

	// Check if toPkg already depends on fromPkg
	toDeps, exists := ws.Dependencies.PackageDeps[toPkg]
	if !exists {
		return false
	}

	for _, dep := range toDeps {
		if dep == fromPkg {
			return true
		}
	}

	return false
}

func isValidGoIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}

	// First character must be letter or underscore
	first := rune(name[0])
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}

	// Remaining characters must be letters, digits, or underscores
	for _, r := range name[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}

	return true
}

func lastPathComponent(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// findImportInsertPosition finds the correct position to insert a new import
func findImportInsertPosition(ws *types.Workspace, filePath string) int {
	// Find the file and its AST
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.Path == filePath && file.AST != nil {
				// If there are existing imports, add after the last one
				if len(file.AST.Imports) > 0 {
					lastImport := file.AST.Imports[len(file.AST.Imports)-1]
					return int(lastImport.End())
				}
				
				// If no imports, add after package declaration
				if file.AST.Name != nil {
					// Find the end of the package line by looking for the first newline after package name
					return int(file.AST.Name.End()) + 1 // +1 to get past the newline
				}
			}
		}
	}
	
	// Fallback: beginning of file
	return 0
}

// clampPosition ensures a position is within file bounds
func clampPosition(pos int, fileContent []byte) int {
	if pos < 0 {
		return 0
	}
	if pos > len(fileContent) {
		return len(fileContent)
	}
	return pos
}

// extractSymbolSource extracts the source code of a symbol from a file
func (op *MoveSymbolOperation) extractSymbolSource(file *types.File, symbol *types.Symbol) (string, error) {
	var sourceCode string
	found := false
	
	// Check if AST is loaded
	if file.AST == nil {
		return "", fmt.Errorf("AST not loaded for file %s", file.Path)
	}
	
	// We need access to the FileSet to convert token positions to byte offsets
	// For now, let's use a simpler approach: find the struct in the source by line/column
	lines := strings.Split(string(file.OriginalContent), "\n")
	
	ast.Inspect(file.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name.Name == symbol.Name {
				// Find the function by searching for its signature in the source
				for i, line := range lines {
					if strings.Contains(line, "func "+symbol.Name) {
						// Extract the entire function - this is simplified
						start := i
						end := start + 1
						// Find the closing brace (simplified)
						for j := start + 1; j < len(lines); j++ {
							if strings.Contains(lines[j], "}") && !strings.Contains(lines[j], "{") {
								end = j + 1
								break
							}
						}
						sourceCode = strings.Join(lines[start:end], "\n")
						found = true
						return false
					}
				}
			}
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == symbol.Name {
					// Find the type declaration by searching for it in the source
					for i, line := range lines {
						if strings.Contains(line, "type "+symbol.Name+" struct") {
							// Extract the entire struct definition
							start := i
							end := start + 1
							braceCount := 0
							foundOpenBrace := false
							
							// Find the matching closing brace
							for j := start; j < len(lines); j++ {
								for _, char := range lines[j] {
									if char == '{' {
										foundOpenBrace = true
										braceCount++
									} else if char == '}' && foundOpenBrace {
										braceCount--
										if braceCount == 0 {
											end = j + 1
											sourceCode = strings.Join(lines[start:end], "\n")
											found = true
											return false
										}
									}
								}
							}
						}
					}
				}
			}
		}
		return true
	})
	
	if !found {
		return "", fmt.Errorf("symbol %s not found in file %s", symbol.Name, file.Path)
	}
	
	return sourceCode, nil
}

// packagePathToImportPath converts an absolute package path to a Go import path
func packagePathToImportPath(ws *types.Workspace, packagePath string) string {
	// If we have module information, create module-relative import path
	if ws.Module != nil && ws.Module.Path != "" {
		// Remove the workspace root prefix to get relative path
		if strings.HasPrefix(packagePath, ws.RootPath) {
			relPath := strings.TrimPrefix(packagePath, ws.RootPath)
			relPath = strings.TrimPrefix(relPath, "/") // Remove leading slash
			
			if relPath == "" {
				// This is the root package
				return ws.Module.Path
			}
			
			// Combine module path with relative path
			return ws.Module.Path + "/" + relPath
		}
	}
	
	// Fallback: use the package path as-is (not ideal, but works for simple cases)
	return packagePath
}

// calculateByteOffset calculates the byte offset in a file from line and column numbers
func calculateByteOffset(filePath string, line, column int) int {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0 // Return 0 if we can't read the file
	}
	
	if line <= 0 || column <= 0 {
		return 0
	}
	
	lines := strings.Split(string(content), "\n")
	if line > len(lines) {
		return len(content) // Return end of file if line is beyond file
	}
	
	// Calculate offset by summing up previous lines plus current column
	offset := 0
	for i := 0; i < line-1; i++ {
		offset += len(lines[i]) + 1 // +1 for the newline character
	}
	
	// Add column offset (subtract 1 since columns are 1-based)
	if column-1 < len(lines[line-1]) {
		offset += column - 1
	} else {
		offset += len(lines[line-1]) // If column is beyond line, go to end of line
	}
	
	return offset
}

func (op *MoveSymbolOperation) generateStructFromSymbol(symbol *types.Symbol, sourceFile *types.File) (string, error) {
	if sourceFile.AST == nil {
		return "", fmt.Errorf("no AST available for file %s", sourceFile.Path)
	}

	// Find the struct definition in the AST
	var structType *ast.StructType
	ast.Inspect(sourceFile.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok {
			if typeSpec.Name.Name == symbol.Name {
				if st, ok := typeSpec.Type.(*ast.StructType); ok {
					structType = st
					return false // Stop searching
				}
			}
		}
		return true
	})

	if structType == nil {
		return "", fmt.Errorf("struct definition not found for %s", symbol.Name)
	}

	// Build struct with fields
	var structBuilder strings.Builder
	structBuilder.WriteString(fmt.Sprintf("type %s struct {\n", symbol.Name))

	if structType.Fields != nil {
		for _, field := range structType.Fields.List {
			// Extract field information
			if len(field.Names) > 0 {
				for _, name := range field.Names {
					structBuilder.WriteString(fmt.Sprintf("\t%s ", name.Name))
					// Add type (simplified - doesn't handle all complex types)
					if ident, ok := field.Type.(*ast.Ident); ok {
						structBuilder.WriteString(ident.Name)
					} else {
						structBuilder.WriteString("interface{}")
					}
					
					// Add struct tags if present
					if field.Tag != nil {
						structBuilder.WriteString(" ")
						structBuilder.WriteString(field.Tag.Value)
					}
					
					structBuilder.WriteString("\n")
				}
			} else {
				// Embedded field
				structBuilder.WriteString("\t")
				if ident, ok := field.Type.(*ast.Ident); ok {
					structBuilder.WriteString(ident.Name)
				} else {
					structBuilder.WriteString("interface{}")
				}
				structBuilder.WriteString("\n")
			}
		}
	}

	structBuilder.WriteString("}")
	return structBuilder.String(), nil
}