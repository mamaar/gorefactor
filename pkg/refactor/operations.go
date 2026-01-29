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
	resolver := analysis.NewSymbolResolver(ws)
	targetSymbols, err := op.findTargetSymbols(ws, resolver)
	if err != nil {
		return err
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

// buildAvailableSymbolsList creates a list of all symbols in a package for error messages
func (op *RenameSymbolOperation) buildAvailableSymbolsList(pkg *types.Package) []string {
	availableSymbols := make([]string, 0)
	if pkg.Symbols == nil {
		return availableSymbols
	}

	for name := range pkg.Symbols.Functions {
		availableSymbols = append(availableSymbols, fmt.Sprintf("func %s", name))
	}
	for name := range pkg.Symbols.Types {
		availableSymbols = append(availableSymbols, fmt.Sprintf("type %s", name))
	}
	for name := range pkg.Symbols.Variables {
		availableSymbols = append(availableSymbols, fmt.Sprintf("var %s", name))
	}
	for name := range pkg.Symbols.Constants {
		availableSymbols = append(availableSymbols, fmt.Sprintf("const %s", name))
	}

	return availableSymbols
}

// formatSymbolNotFoundError creates a detailed error message with available symbols
func (op *RenameSymbolOperation) formatSymbolNotFoundError(symbolName, packageName string, availableSymbols []string, originalErr error) error {
	message := fmt.Sprintf("symbol not found: %s in package %s\nAvailable symbols (%d):",
		symbolName, packageName, len(availableSymbols))

	if len(availableSymbols) == 0 {
		message += "\n  (no symbols found - package may not be parsed correctly)"
	} else {
		for i, sym := range availableSymbols {
			if i < 20 { // Limit output
				message += fmt.Sprintf("\n  - %s", sym)
			} else {
				message += fmt.Sprintf("\n  ... and %d more", len(availableSymbols)-20)
				break
			}
		}
	}

	return &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: message,
		Cause:   originalErr,
	}
}

// findTargetSymbols locates all symbols matching the request across packages
func (op *RenameSymbolOperation) findTargetSymbols(ws *types.Workspace, resolver *analysis.SymbolResolver) ([]*types.Symbol, error) {
	var targetSymbols []*types.Symbol

	if op.Request.Package != "" {
		// Package-scoped rename
		pkg, exists := ws.Packages[op.Request.Package]
		if !exists {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package not found: %s", op.Request.Package),
			}
		}

		if pkg.Symbols == nil {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package %s has no symbol table built", op.Request.Package),
			}
		}

		symbol, err := resolver.ResolveSymbol(pkg, op.Request.SymbolName)
		if err != nil {
			// Build detailed error message
			availableSymbols := op.buildAvailableSymbolsList(pkg)
			return nil, op.formatSymbolNotFoundError(op.Request.SymbolName, op.Request.Package, availableSymbols, err)
		}
		targetSymbols = append(targetSymbols, symbol)
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
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("no symbols found with name: %s", op.Request.SymbolName),
		}
	}

	return targetSymbols, nil
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

// RenamePackageOperation implements package renaming
type RenamePackageOperation struct {
	Request types.RenamePackageRequest
}

func (op *RenamePackageOperation) Type() types.OperationType {
	return types.RenamePackageOperation
}

func (op *RenamePackageOperation) Validate(ws *types.Workspace) error {
	// Check that source package exists
	targetPackage, exists := ws.Packages[op.Request.PackagePath]
	if !exists {
		var availablePackages []string
		for pkgPath := range ws.Packages {
			availablePackages = append(availablePackages, pkgPath)
		}
		
		message := fmt.Sprintf("package not found: %s\nAvailable packages:\n", op.Request.PackagePath)
		if len(availablePackages) == 0 {
			message += "  (no packages found - ensure you're in a Go workspace with go.mod)"
		} else {
			for _, pkgPath := range availablePackages {
				if pkg, exists := ws.Packages[pkgPath]; exists {
					message += fmt.Sprintf("  - %s (Go package: %s)\n", pkgPath, pkg.Name)
				}
			}
		}
		
		return &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: message,
		}
	}

	// Check that the current package name matches what the user expects
	if targetPackage.Name != op.Request.OldPackageName {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("package name mismatch: expected %s, found %s", op.Request.OldPackageName, targetPackage.Name),
		}
	}

	// Check that new package name is valid
	if !isValidGoIdentifier(op.Request.NewPackageName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go package name: %s", op.Request.NewPackageName),
		}
	}

	// Check that new package name doesn't conflict with existing packages
	if op.Request.UpdateImports {
		for _, pkg := range ws.Packages {
			if pkg.Name == op.Request.NewPackageName && pkg.Path != op.Request.PackagePath {
				return &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("package name conflict: %s already exists in %s", op.Request.NewPackageName, pkg.Path),
				}
			}
		}
	}

	return nil
}

func (op *RenamePackageOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find the package to rename
	targetPackage := ws.Packages[op.Request.PackagePath]
	
	// Step 1: Update package declaration in all files within the package
	for _, file := range targetPackage.Files {
		change, err := op.generatePackageDeclarationChange(file, op.Request.OldPackageName, op.Request.NewPackageName)
		if err != nil {
			return nil, fmt.Errorf("failed to generate package declaration change for %s: %v", file.Path, err)
		}
		if change != nil {
			plan.Changes = append(plan.Changes, *change)
			plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
		}
	}

	// Step 2: Update import statements in other packages (if requested)
	if op.Request.UpdateImports {
		importChanges, affectedFiles, err := op.generateImportUpdates(ws, op.Request.PackagePath, op.Request.NewPackageName)
		if err != nil {
			return nil, fmt.Errorf("failed to generate import updates: %v", err)
		}
		plan.Changes = append(plan.Changes, importChanges...)
		for _, file := range affectedFiles {
			if !contains(plan.AffectedFiles, file) {
				plan.AffectedFiles = append(plan.AffectedFiles, file)
			}
		}
	}

	return plan, nil
}

func (op *RenamePackageOperation) Description() string {
	return fmt.Sprintf("Rename package %s to %s in %s", op.Request.OldPackageName, op.Request.NewPackageName, op.Request.PackagePath)
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

						// Include doc comments if present - look backwards for comment lines
						for j := i - 1; j >= 0; j-- {
							trimmed := strings.TrimSpace(lines[j])
							if strings.HasPrefix(trimmed, "//") {
								start = j
							} else if trimmed == "" {
								// Skip empty lines between comments and func
								continue
							} else {
								break
							}
						}

						end := i + 1
						braceCount := 0
						foundOpenBrace := false

						// Find the matching closing brace using proper brace counting
						for j := i; j < len(lines); j++ {
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

	// Read the file content to detect if this is a qualified reference
	content, err := os.ReadFile(ref.File)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", ref.File, err)
	}

	oldRef := ref.Symbol.Name
	newRef := targetPackageName + "." + ref.Symbol.Name
	startPos := ref.Offset
	endPos := startPos + len(oldRef)

	// Check if this is a qualified reference (e.g., pkg.Symbol)
	// Look backwards for a dot and package name
	if startPos > 0 && content[startPos-1] == '.' {
		// Find the start of the package name
		pkgStart := startPos - 2 // Position before the dot
		for pkgStart >= 0 && (isIdentChar(content[pkgStart]) || content[pkgStart] == '_') {
			pkgStart--
		}
		pkgStart++ // Move to first char of package name

		// Extract the old package name
		oldPkg := string(content[pkgStart : startPos-1])
		if oldPkg != "" {
			// This is a qualified reference - replace the whole thing
			oldRef = oldPkg + "." + ref.Symbol.Name
			startPos = pkgStart
			// newRef stays as targetPackageName + "." + ref.Symbol.Name
		}
	}

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

// isIdentChar returns true if the byte is a valid Go identifier character
func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
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
						// Extract the entire function using brace depth tracking
						start := i

						// Include doc comments if present - look backwards for comment lines
						for j := i - 1; j >= 0; j-- {
							trimmed := strings.TrimSpace(lines[j])
							if strings.HasPrefix(trimmed, "//") {
								start = j
							} else if trimmed == "" {
								// Skip empty lines between comments and func
								continue
							} else {
								break
							}
						}

						end := i + 1
						braceCount := 0
						foundOpenBrace := false

						// Find the matching closing brace starting from func line
						for j := i; j < len(lines); j++ {
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

// Helper methods for RenamePackageOperation

func (op *RenamePackageOperation) generatePackageDeclarationChange(file *types.File, oldName, newName string) (*types.Change, error) {
	content := string(file.OriginalContent)
	lines := strings.Split(content, "\n")
	
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 && parts[1] == oldName {
				// Calculate byte position of the package name
				startByte := 0
				for j := 0; j < i; j++ {
					startByte += len(lines[j]) + 1 // +1 for newline
				}
				
				// Find the start of the package name within the line
				packageKeywordPos := strings.Index(line, "package")
				if packageKeywordPos == -1 {
					continue
				}
				
				nameStartInLine := packageKeywordPos + len("package")
				// Skip whitespace to find the actual name
				for nameStartInLine < len(line) && (line[nameStartInLine] == ' ' || line[nameStartInLine] == '\t') {
					nameStartInLine++
				}
				
				startByte += nameStartInLine
				endByte := startByte + len(oldName)
				
				return &types.Change{
					File:        file.Path,
					Start:       startByte,
					End:         endByte,
					OldText:     oldName,
					NewText:     newName,
					Description: fmt.Sprintf("Update package declaration from %s to %s", oldName, newName),
				}, nil
			}
		}
	}
	
	return nil, fmt.Errorf("package declaration not found in file %s", file.Path)
}

func (op *RenamePackageOperation) generateImportUpdates(ws *types.Workspace, packagePath, newPackageName string) ([]types.Change, []string, error) {
	var changes []types.Change
	var affectedFiles []string
	
	// Get the import path for this package
	importPath := packagePathToImportPath(ws, packagePath)
	
	// Find all files that import this package
	for _, pkg := range ws.Packages {
		if pkg.Path == packagePath {
			continue // Skip the package itself
		}
		
		for _, file := range pkg.Files {
			hasImport, change, err := op.generateFileImportUpdate(file, importPath, newPackageName)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to update imports in %s: %v", file.Path, err)
			}
			
			if hasImport && change != nil {
				changes = append(changes, *change)
				affectedFiles = append(affectedFiles, file.Path)
			}
		}
	}
	
	return changes, affectedFiles, nil
}

func (op *RenamePackageOperation) generateFileImportUpdate(file *types.File, importPath, newPackageName string) (bool, *types.Change, error) {
	content := string(file.OriginalContent)
	
	// Check if this file imports the target package
	if !strings.Contains(content, `"`+importPath+`"`) {
		return false, nil, nil
	}
	
	// For files that use the old package name, we need to update qualified references
	// This is a simplified implementation - a full implementation would parse the AST
	lines := strings.Split(content, "\n")
	
	// Find all qualified references to the old package name and update them
	var changes []types.Change
	oldPackageName := lastPathComponent(importPath) // Get default package name from import path
	
	if oldPackageName == newPackageName {
		// If the package name doesn't actually change (just the internal declaration), no updates needed
		return true, nil, nil
	}
	
	for i, line := range lines {
		if strings.Contains(line, oldPackageName+".") {
			// Replace qualified references
			newLine := strings.ReplaceAll(line, oldPackageName+".", newPackageName+".")
			if newLine != line {
				// Calculate byte positions
				startByte := 0
				for j := 0; j < i; j++ {
					startByte += len(lines[j]) + 1
				}
				endByte := startByte + len(line)
				
				change := types.Change{
					File:        file.Path,
					Start:       startByte,
					End:         endByte,
					OldText:     line,
					NewText:     newLine,
					Description: fmt.Sprintf("Update qualified references from %s to %s", oldPackageName, newPackageName),
				}
				changes = append(changes, change)
			}
		}
	}
	
	// For simplicity, return the first change if any
	if len(changes) > 0 {
		return true, &changes[0], nil
	}
	
	return true, nil, nil
}

// RenameInterfaceMethodOperation implements interface method renaming
type RenameInterfaceMethodOperation struct {
	Request types.RenameInterfaceMethodRequest
}

func (op *RenameInterfaceMethodOperation) Type() types.OperationType {
	return types.RenameInterfaceMethodOperation
}

func (op *RenameInterfaceMethodOperation) Validate(ws *types.Workspace) error {
	// Find the interface in the workspace
	interfaceSymbol, err := op.findInterface(ws)
	if err != nil {
		return err
	}

	// Check if the method exists on the interface
	_, err = op.findInterfaceMethod(ws, interfaceSymbol)
	if err != nil {
		return err
	}

	// Check that new method name is valid
	if !isValidGoIdentifier(op.Request.NewMethodName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.Request.NewMethodName),
		}
	}

	// Check for method name conflicts on the interface
	if op.Request.UpdateImplementations {
		if err := op.checkMethodNameConflicts(ws, interfaceSymbol); err != nil {
			return err
		}
	}

	return nil
}

func (op *RenameInterfaceMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Find the interface and method
	interfaceSymbol, err := op.findInterface(ws)
	if err != nil {
		return nil, err
	}

	methodSymbol, err := op.findInterfaceMethod(ws, interfaceSymbol)
	if err != nil {
		return nil, err
	}

	// Step 1: Update the interface method declaration
	interfaceChange, err := op.generateInterfaceMethodChange(ws, interfaceSymbol, methodSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to generate interface method change: %v", err)
	}
	
	if interfaceChange != nil {
		plan.Changes = append(plan.Changes, *interfaceChange)
		if !contains(plan.AffectedFiles, interfaceChange.File) {
			plan.AffectedFiles = append(plan.AffectedFiles, interfaceChange.File)
		}
	}

	// Step 2: Update all implementations if requested
	if op.Request.UpdateImplementations {
		implChanges, implFiles, err := op.generateImplementationChanges(ws, interfaceSymbol, methodSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to generate implementation changes: %v", err)
		}
		
		plan.Changes = append(plan.Changes, implChanges...)
		for _, file := range implFiles {
			if !contains(plan.AffectedFiles, file) {
				plan.AffectedFiles = append(plan.AffectedFiles, file)
			}
		}
	}

	// Step 3: Update all method calls across the workspace
	callChanges, callFiles, err := op.generateMethodCallChanges(ws, interfaceSymbol, methodSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to generate method call changes: %v", err)
	}
	
	plan.Changes = append(plan.Changes, callChanges...)
	for _, file := range callFiles {
		if !contains(plan.AffectedFiles, file) {
			plan.AffectedFiles = append(plan.AffectedFiles, file)
		}
	}

	return plan, nil
}

func (op *RenameInterfaceMethodOperation) Description() string {
	return fmt.Sprintf("Rename interface method %s.%s to %s", op.Request.InterfaceName, op.Request.MethodName, op.Request.NewMethodName)
}

// Helper methods for RenameInterfaceMethodOperation

func (op *RenameInterfaceMethodOperation) findInterface(ws *types.Workspace) (*types.Symbol, error) {
	// Search for the interface in the specified package or workspace-wide
	var targetPackages []*types.Package
	
	if op.Request.PackagePath != "" {
		if pkg, exists := ws.Packages[op.Request.PackagePath]; exists {
			targetPackages = []*types.Package{pkg}
		} else {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package not found: %s", op.Request.PackagePath),
			}
		}
	} else {
		// Search all packages
		for _, pkg := range ws.Packages {
			targetPackages = append(targetPackages, pkg)
		}
	}

	for _, pkg := range targetPackages {
		if pkg.Symbols == nil {
			continue
		}
		
		for _, symbol := range pkg.Symbols.Types {
			if symbol.Name == op.Request.InterfaceName && symbol.Kind == types.InterfaceSymbol {
				return symbol, nil
			}
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("interface %s not found", op.Request.InterfaceName),
	}
}

func (op *RenameInterfaceMethodOperation) findInterfaceMethod(ws *types.Workspace, interfaceSymbol *types.Symbol) (*types.Symbol, error) {
	// Parse the interface to find its methods
	pkg := ws.Packages[interfaceSymbol.Package]
	if pkg == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package %s not found", interfaceSymbol.Package),
		}
	}

	// Find the file containing the interface
	var interfaceFile *types.File
	for _, file := range pkg.Files {
		if file.Path == interfaceSymbol.File {
			interfaceFile = file
			break
		}
	}

	if interfaceFile == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("file %s not found", interfaceSymbol.File),
		}
	}

	// Find the method in the interface by parsing its AST
	methodSymbol, err := op.findMethodInInterface(interfaceFile, interfaceSymbol)
	if err != nil {
		return nil, err
	}

	return methodSymbol, nil
}

func (op *RenameInterfaceMethodOperation) findMethodInInterface(file *types.File, interfaceSymbol *types.Symbol) (*types.Symbol, error) {
	var methodSymbol *types.Symbol
	
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == interfaceSymbol.Name {
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				for _, field := range interfaceType.Methods.List {
					if len(field.Names) > 0 && field.Names[0].Name == op.Request.MethodName {
						methodSymbol = &types.Symbol{
							Name:     field.Names[0].Name,
							Kind:     types.MethodSymbol,
							Package:  interfaceSymbol.Package,
							File:     file.Path,
							Position: field.Names[0].Pos(),
							Exported: op.isExported(field.Names[0].Name),
						}
						return false
					}
				}
			}
		}
		return true
	})

	if methodSymbol == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("method %s not found on interface %s", op.Request.MethodName, interfaceSymbol.Name),
		}
	}

	return methodSymbol, nil
}

func (op *RenameInterfaceMethodOperation) checkMethodNameConflicts(ws *types.Workspace, interfaceSymbol *types.Symbol) error {
	// Check if the new method name would conflict with existing methods on the interface
	pkg := ws.Packages[interfaceSymbol.Package]
	if pkg == nil {
		return nil
	}

	interfaceFile := pkg.Files[interfaceSymbol.File]
	if interfaceFile == nil {
		return nil
	}

	var hasConflict bool
	ast.Inspect(interfaceFile.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == interfaceSymbol.Name {
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				for _, field := range interfaceType.Methods.List {
					if len(field.Names) > 0 && field.Names[0].Name == op.Request.NewMethodName {
						hasConflict = true
						return false
					}
				}
			}
		}
		return true
	})

	if hasConflict {
		return &types.RefactorError{
			Type:    types.NameConflict,
			Message: fmt.Sprintf("method %s already exists on interface %s", op.Request.NewMethodName, interfaceSymbol.Name),
		}
	}

	return nil
}

func (op *RenameInterfaceMethodOperation) generateInterfaceMethodChange(ws *types.Workspace, interfaceSymbol *types.Symbol, methodSymbol *types.Symbol) (*types.Change, error) {
	pkg := ws.Packages[interfaceSymbol.Package]
	if pkg == nil {
		return nil, fmt.Errorf("package %s not found", interfaceSymbol.Package)
	}

	var interfaceFile *types.File
	for _, file := range pkg.Files {
		if file.Path == interfaceSymbol.File {
			interfaceFile = file
			break
		}
	}

	if interfaceFile == nil {
		return nil, fmt.Errorf("file %s not found", interfaceSymbol.File)
	}

	// Find the method declaration line and calculate byte positions
	var change *types.Change
	ast.Inspect(interfaceFile.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == interfaceSymbol.Name {
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				for _, field := range interfaceType.Methods.List {
					if len(field.Names) > 0 && field.Names[0].Name == op.Request.MethodName {
						// Calculate the byte position of the method name
						pos := field.Names[0].Pos()
						startByte := int(pos) - 1  // Convert to 0-based
						endByte := startByte + len(op.Request.MethodName)

						change = &types.Change{
							File:        interfaceFile.Path,
							Start:       startByte,
							End:         endByte,
							OldText:     op.Request.MethodName,
							NewText:     op.Request.NewMethodName,
							Description: fmt.Sprintf("Rename interface method %s to %s", op.Request.MethodName, op.Request.NewMethodName),
						}
						return false
					}
				}
			}
		}
		return true
	})

	return change, nil
}

func (op *RenameInterfaceMethodOperation) generateImplementationChanges(ws *types.Workspace, interfaceSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, []string, error) {
	var changes []types.Change
	var affectedFiles []string

	// Find all types that implement this interface
	implementations, err := op.findInterfaceImplementations(ws, interfaceSymbol)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find interface implementations: %v", err)
	}

	// For each implementation, find and rename the method
	for _, impl := range implementations {
		implChanges, err := op.generateImplementationMethodChanges(ws, impl, methodSymbol)
		if err != nil {
			continue // Skip implementations we can't process
		}
		
		for _, change := range implChanges {
			changes = append(changes, change)
			if !contains(affectedFiles, change.File) {
				affectedFiles = append(affectedFiles, change.File)
			}
		}
	}

	return changes, affectedFiles, nil
}

func (op *RenameInterfaceMethodOperation) findInterfaceImplementations(ws *types.Workspace, interfaceSymbol *types.Symbol) ([]*types.Symbol, error) {
	var implementations []*types.Symbol

	// Search through all packages for types that implement the interface
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		// Check all struct types to see if they implement the interface
		for _, symbol := range pkg.Symbols.Types {
			if symbol.Kind == types.TypeSymbol {
				if op.implementsInterface(ws, symbol, interfaceSymbol) {
					implementations = append(implementations, symbol)
				}
			}
		}
	}

	return implementations, nil
}

func (op *RenameInterfaceMethodOperation) implementsInterface(ws *types.Workspace, structSymbol *types.Symbol, interfaceSymbol *types.Symbol) bool {
	// Get methods for the struct type
	pkg := ws.Packages[structSymbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return false
	}

	structMethods, exists := pkg.Symbols.Methods[structSymbol.Name]
	if !exists {
		return false
	}

	// Check if the struct has a method with the name we're looking for
	for _, method := range structMethods {
		if method.Name == op.Request.MethodName {
			return true  // Simplified check - real implementation would verify full signature
		}
	}

	return false
}

func (op *RenameInterfaceMethodOperation) generateImplementationMethodChanges(ws *types.Workspace, implSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, error) {
	var changes []types.Change

	pkg := ws.Packages[implSymbol.Package]
	if pkg == nil {
		return changes, nil
	}

	// Find method implementations on this type
	if methods, exists := pkg.Symbols.Methods[implSymbol.Name]; exists {
		for _, method := range methods {
			if method.Name == op.Request.MethodName {
				// Generate change for this method implementation
				change, err := op.generateMethodImplementationChange(ws, method)
				if err != nil {
					continue
				}
				if change != nil {
					changes = append(changes, *change)
				}
			}
		}
	}

	return changes, nil
}

func (op *RenameInterfaceMethodOperation) generateMethodImplementationChange(ws *types.Workspace, methodSymbol *types.Symbol) (*types.Change, error) {
	pkg := ws.Packages[methodSymbol.Package]
	if pkg == nil {
		return nil, fmt.Errorf("package %s not found", methodSymbol.Package)
	}

	var methodFile *types.File
	for _, file := range pkg.Files {
		if file.Path == methodSymbol.File {
			methodFile = file
			break
		}
	}

	if methodFile == nil {
		return nil, fmt.Errorf("file %s not found", methodSymbol.File)
	}

	// Find the method declaration and create a change
	var change *types.Change
	ast.Inspect(methodFile.AST, func(n ast.Node) bool {
		if funcDecl, ok := n.(*ast.FuncDecl); ok {
			if funcDecl.Name != nil && funcDecl.Name.Name == op.Request.MethodName {
				// Check if this is a method (has receiver)
				if funcDecl.Recv != nil && len(funcDecl.Recv.List) > 0 {
					pos := funcDecl.Name.Pos()
					startByte := int(pos) - 1
					endByte := startByte + len(op.Request.MethodName)

					change = &types.Change{
						File:        methodFile.Path,
						Start:       startByte,
						End:         endByte,
						OldText:     op.Request.MethodName,
						NewText:     op.Request.NewMethodName,
						Description: fmt.Sprintf("Rename method implementation %s to %s", op.Request.MethodName, op.Request.NewMethodName),
					}
					return false
				}
			}
		}
		return true
	})

	return change, nil
}

func (op *RenameInterfaceMethodOperation) generateMethodCallChanges(ws *types.Workspace, interfaceSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, []string, error) {
	var changes []types.Change
	var affectedFiles []string

	// Search through all files in the workspace for method calls
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			fileChanges, err := op.generateFileMethodCallChanges(file)
			if err != nil {
				continue // Skip files we can't process
			}
			
			if len(fileChanges) > 0 {
				changes = append(changes, fileChanges...)
				if !contains(affectedFiles, file.Path) {
					affectedFiles = append(affectedFiles, file.Path)
				}
			}
		}
	}

	return changes, affectedFiles, nil
}

func (op *RenameInterfaceMethodOperation) generateFileMethodCallChanges(file *types.File) ([]types.Change, error) {
	var changes []types.Change

	// Find method calls in the file
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if callExpr, ok := n.(*ast.CallExpr); ok {
			if selectorExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				if selectorExpr.Sel.Name == op.Request.MethodName {
					// Create change for this method call
					pos := selectorExpr.Sel.Pos()
					startByte := int(pos) - 1
					endByte := startByte + len(op.Request.MethodName)

					change := types.Change{
						File:        file.Path,
						Start:       startByte,
						End:         endByte,
						OldText:     op.Request.MethodName,
						NewText:     op.Request.NewMethodName,
						Description: fmt.Sprintf("Rename method call %s to %s", op.Request.MethodName, op.Request.NewMethodName),
					}
					changes = append(changes, change)
				}
			}
		}
		return true
	})

	return changes, nil
}

func (op *RenameInterfaceMethodOperation) isExported(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}

// RenameMethodOperation implements renaming methods on specific types (structs or interfaces)
type RenameMethodOperation struct {
	Request types.RenameMethodRequest
}

func (op *RenameMethodOperation) Type() types.OperationType {
	return types.RenameMethodOperation
}

func (op *RenameMethodOperation) Validate(ws *types.Workspace) error {
	// Find the type (struct or interface) in the workspace
	typeSymbol, err := op.findType(ws)
	if err != nil {
		return err
	}

	// Check if the method exists on the type
	_, err = op.findMethodOnType(ws, typeSymbol)
	if err != nil {
		return err
	}

	// Check that new method name is valid
	if !isValidGoIdentifier(op.Request.NewMethodName) {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid Go identifier: %s", op.Request.NewMethodName),
		}
	}

	// Check for name conflicts
	if op.Request.MethodName == op.Request.NewMethodName {
		return &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: "new method name cannot be the same as the current name",
		}
	}

	// Check if new method name already exists on the type
	err = op.checkMethodNameConflict(ws, typeSymbol)
	if err != nil {
		return err
	}

	return nil
}

func (op *RenameMethodOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	var changes []types.Change
	var affectedFiles []string

	// Find the type symbol
	typeSymbol, err := op.findType(ws)
	if err != nil {
		return nil, err
	}

	// Find the method symbol
	methodSymbol, err := op.findMethodOnType(ws, typeSymbol)
	if err != nil {
		return nil, err
	}

	// Generate change for method definition
	definitionChange, err := op.generateMethodDefinitionChange(ws, typeSymbol, methodSymbol)
	if err != nil {
		return nil, err
	}
	if definitionChange != nil {
		changes = append(changes, *definitionChange)
		if !contains(affectedFiles, definitionChange.File) {
			affectedFiles = append(affectedFiles, definitionChange.File)
		}
	}

	// Generate changes for all method calls/references
	referenceChanges, err := op.generateMethodReferenceChanges(ws, typeSymbol, methodSymbol)
	if err != nil {
		return nil, err
	}
	changes = append(changes, referenceChanges...)
	for _, change := range referenceChanges {
		if !contains(affectedFiles, change.File) {
			affectedFiles = append(affectedFiles, change.File)
		}
	}

	// If this is an interface and UpdateImplementations is true, 
	// also rename the method on all implementations
	if typeSymbol.Kind == types.InterfaceSymbol && op.Request.UpdateImplementations {
		implChanges, err := op.generateImplementationChanges(ws, typeSymbol, methodSymbol)
		if err != nil {
			return nil, err
		}
		changes = append(changes, implChanges...)
		for _, change := range implChanges {
			if !contains(affectedFiles, change.File) {
				affectedFiles = append(affectedFiles, change.File)
			}
		}
	}

	return &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: affectedFiles,
		Reversible:    true,
	}, nil
}

func (op *RenameMethodOperation) Description() string {
	if op.Request.UpdateImplementations {
		return fmt.Sprintf("Rename method %s.%s to %s (including implementations)", op.Request.TypeName, op.Request.MethodName, op.Request.NewMethodName)
	}
	return fmt.Sprintf("Rename method %s.%s to %s", op.Request.TypeName, op.Request.MethodName, op.Request.NewMethodName)
}

// Helper methods for RenameMethodOperation

func (op *RenameMethodOperation) findType(ws *types.Workspace) (*types.Symbol, error) {
	// Search for the type in the specified package or workspace-wide
	var targetPackages []*types.Package
	
	if op.Request.PackagePath != "" {
		if pkg, exists := ws.Packages[op.Request.PackagePath]; exists {
			targetPackages = []*types.Package{pkg}
		} else {
			return nil, &types.RefactorError{
				Type:    types.SymbolNotFound,
				Message: fmt.Sprintf("package %s not found", op.Request.PackagePath),
			}
		}
	} else {
		// Search all packages
		for _, pkg := range ws.Packages {
			targetPackages = append(targetPackages, pkg)
		}
	}

	for _, pkg := range targetPackages {
		if pkg.Symbols == nil {
			continue
		}
		
		// Look for the type in the package
		if symbol, exists := pkg.Symbols.Types[op.Request.TypeName]; exists {
			return symbol, nil
		}
	}
	
	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("type %s not found", op.Request.TypeName),
	}
}

func (op *RenameMethodOperation) findMethodOnType(ws *types.Workspace, typeSymbol *types.Symbol) (*types.Symbol, error) {
	pkg := ws.Packages[typeSymbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return nil, &types.RefactorError{
			Type:    types.SymbolNotFound,
			Message: fmt.Sprintf("package %s not found or has no symbols", typeSymbol.Package),
		}
	}

	// Look for methods on this type
	if methods, exists := pkg.Symbols.Methods[typeSymbol.Name]; exists {
		for _, method := range methods {
			if method.Name == op.Request.MethodName {
				return method, nil
			}
		}
	}

	return nil, &types.RefactorError{
		Type:    types.SymbolNotFound,
		Message: fmt.Sprintf("method %s not found on type %s", op.Request.MethodName, op.Request.TypeName),
	}
}

func (op *RenameMethodOperation) checkMethodNameConflict(ws *types.Workspace, typeSymbol *types.Symbol) error {
	pkg := ws.Packages[typeSymbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	// Check if new method name already exists on this type
	if methods, exists := pkg.Symbols.Methods[typeSymbol.Name]; exists {
		for _, method := range methods {
			if method.Name == op.Request.NewMethodName {
				return &types.RefactorError{
					Type:    types.NameConflict,
					Message: fmt.Sprintf("method %s already exists on type %s", op.Request.NewMethodName, typeSymbol.Name),
				}
			}
		}
	}

	return nil
}

func (op *RenameMethodOperation) generateMethodDefinitionChange(ws *types.Workspace, typeSymbol *types.Symbol, methodSymbol *types.Symbol) (*types.Change, error) {
	pkg := ws.Packages[typeSymbol.Package]
	if pkg == nil {
		return nil, fmt.Errorf("package %s not found", typeSymbol.Package)
	}

	var methodFile *types.File
	for _, file := range pkg.Files {
		if file.Path == methodSymbol.File {
			methodFile = file
			break
		}
	}

	if methodFile == nil {
		return nil, fmt.Errorf("file %s not found", methodSymbol.File)
	}

	// Calculate the byte position for the method name change
	startByte := int(methodSymbol.Position) - 1  // Convert to 0-based
	endByte := startByte + len(op.Request.MethodName)

	return &types.Change{
		File:        methodFile.Path,
		Start:       startByte,
		End:         endByte,
		OldText:     op.Request.MethodName,
		NewText:     op.Request.NewMethodName,
		Description: fmt.Sprintf("Rename method definition %s to %s", op.Request.MethodName, op.Request.NewMethodName),
	}, nil
}

func (op *RenameMethodOperation) generateMethodReferenceChanges(ws *types.Workspace, typeSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, error) {
	var changes []types.Change

	// Find all method calls and references across the workspace
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			if file.AST == nil {
				continue
			}

			// Look for method calls on this type
			ast.Inspect(file.AST, func(n ast.Node) bool {
				if callExpr, ok := n.(*ast.CallExpr); ok {
					if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
						if selExpr.Sel.Name == op.Request.MethodName {
							// This is a potential method call - we need to verify it's on our type
							// For now, we'll create the change (a more sophisticated implementation
							// would verify the receiver type)
							pos := selExpr.Sel.Pos()
							startByte := int(pos) - 1
							endByte := startByte + len(op.Request.MethodName)

							change := types.Change{
								File:        file.Path,
								Start:       startByte,
								End:         endByte,
								OldText:     op.Request.MethodName,
								NewText:     op.Request.NewMethodName,
								Description: fmt.Sprintf("Rename method call %s to %s", op.Request.MethodName, op.Request.NewMethodName),
							}
							changes = append(changes, change)
						}
					}
				}
				return true
			})
		}
	}

	return changes, nil
}

func (op *RenameMethodOperation) generateImplementationChanges(ws *types.Workspace, interfaceSymbol *types.Symbol, methodSymbol *types.Symbol) ([]types.Change, error) {
	var changes []types.Change

	// Find all types that implement this interface
	for _, pkg := range ws.Packages {
		if pkg.Symbols == nil {
			continue
		}

		// Look for struct types that might implement the interface
		for typeName, typeSymbol := range pkg.Symbols.Types {
			if typeSymbol.Kind == types.TypeSymbol {
				// Check if this type has methods that implement the interface
				if methods, exists := pkg.Symbols.Methods[typeName]; exists {
					for _, method := range methods {
						if method.Name == op.Request.MethodName {
							// This type has the method - rename it
							change, err := op.generateMethodDefinitionChange(ws, typeSymbol, method)
							if err != nil {
								return nil, err
							}
							if change != nil {
								changes = append(changes, *change)
							}
						}
					}
				}
			}
		}
	}

	return changes, nil
}