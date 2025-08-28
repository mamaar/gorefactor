package commands

import (
	"fmt"
	"go/ast"
	"os"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// RenameCommand handles symbol renaming operations
func RenameCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: rename requires at least 2 arguments: <symbol> <new-name> [package]\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor rename OldName NewName [pkg/optional]\n")
		fmt.Fprintf(os.Stderr, "       gorefactor rename TypeName.MethodName TypeName.NewMethodName [pkg/optional]\n")
		os.Exit(1)
	}

	symbolName := args[0]
	newName := args[1]
	packagePath := ""

	if len(args) > 2 {
		packagePath = args[2]
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Check if this is a qualified method name (TypeName.MethodName)
	if strings.Contains(symbolName, ".") && strings.Contains(newName, ".") {
		// Parse qualified method names
		oldParts := strings.SplitN(symbolName, ".", 2)
		newParts := strings.SplitN(newName, ".", 2)
		
		if len(oldParts) != 2 || len(newParts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid qualified method name format. Use TypeName.MethodName\n")
			os.Exit(1)
		}
		
		oldTypeName, oldMethodName := oldParts[0], oldParts[1]
		newTypeName, newMethodName := newParts[0], newParts[1]
		
		if oldTypeName != newTypeName {
			fmt.Fprintf(os.Stderr, "Error: cannot rename method to different type. Type names must match: %s != %s\n", oldTypeName, newTypeName)
			os.Exit(1)
		}

		// Handle method rename
		HandleMethodRename(engine, workspace, oldTypeName, oldMethodName, newMethodName, packagePath)
		return
	}

	// Check if this is an interface method rename by searching for the symbol first
	if interfaceMethod := FindInterfaceMethodSymbol(workspace, symbolName, packagePath); interfaceMethod != nil {
		// Handle interface method rename specially
		HandleInterfaceMethodRename(engine, workspace, interfaceMethod, symbolName, newName, packagePath)
		return
	}

	// Create rename request for regular symbols
	request := types.RenameSymbolRequest{
		SymbolName: symbolName,
		NewName:    newName,
		Package:    packagePath,
	}

	if *cli.GlobalFlags.PackageOnly && packagePath == "" {
		fmt.Fprintf(os.Stderr, "Error: --package-only requires a package to be specified\n")
		os.Exit(1)
	}

	// Generate refactoring plan
	plan, err := engine.RenameSymbol(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	var description string
	if packagePath != "" {
		description = fmt.Sprintf("Rename %s to %s in package %s", symbolName, newName, packagePath)
	} else {
		description = fmt.Sprintf("Rename %s to %s across workspace", symbolName, newName)
	}
	ProcessPlan(engine, plan, description)
}

// HandleMethodRename handles the renaming of methods on specific types
func HandleMethodRename(engine refactor.RefactorEngine, workspace *types.Workspace, typeName, oldMethodName, newMethodName, packagePath string) {
	// Create method rename request
	request := types.RenameMethodRequest{
		TypeName:              typeName,
		MethodName:            oldMethodName,
		NewMethodName:         newMethodName,
		PackagePath:           packagePath,
		UpdateImplementations: *cli.GlobalFlags.RenameImplementations,
	}

	// Generate refactoring plan
	plan, err := engine.RenameMethod(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating method refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	var description string
	if *cli.GlobalFlags.RenameImplementations {
		description = fmt.Sprintf("Rename method %s.%s to %s (including implementations)", typeName, oldMethodName, newMethodName)
	} else {
		description = fmt.Sprintf("Rename method %s.%s to %s", typeName, oldMethodName, newMethodName)
	}
	
	if packagePath != "" {
		description += fmt.Sprintf(" in package %s", packagePath)
	}
	
	ProcessPlan(engine, plan, description)
}

// HandleInterfaceMethodRename handles the special case of renaming interface methods
func HandleInterfaceMethodRename(engine refactor.RefactorEngine, workspace *types.Workspace, methodSymbol *types.Symbol, oldName, newName, packagePath string) {
	// Find the interface that contains this method
	interfaceSymbol := FindInterfaceContainingMethod(workspace, methodSymbol)
	if interfaceSymbol == nil {
		fmt.Fprintf(os.Stderr, "Error: could not find interface containing method %s\n", oldName)
		os.Exit(1)
	}

	// Create interface method rename request
	request := types.RenameInterfaceMethodRequest{
		InterfaceName:         interfaceSymbol.Name,
		MethodName:            oldName,
		NewMethodName:         newName,
		PackagePath:           packagePath,
		UpdateImplementations: *cli.GlobalFlags.RenameImplementations,
	}

	// Generate refactoring plan
	plan, err := engine.RenameInterfaceMethod(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating interface method refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	var description string
	if *cli.GlobalFlags.RenameImplementations {
		description = fmt.Sprintf("Rename interface method %s.%s to %s (including implementations)", interfaceSymbol.Name, oldName, newName)
	} else {
		description = fmt.Sprintf("Rename interface method %s.%s to %s", interfaceSymbol.Name, oldName, newName)
	}
	
	if packagePath != "" {
		description += fmt.Sprintf(" in package %s", packagePath)
	}
	
	ProcessPlan(engine, plan, description)
}

// FindInterfaceMethodSymbol searches for an interface method symbol
func FindInterfaceMethodSymbol(workspace *types.Workspace, symbolName, packagePath string) *types.Symbol {
	// Search for interfaces that contain this method
	var targetPackages []*types.Package
	
	if packagePath != "" {
		if pkg, exists := workspace.Packages[packagePath]; exists {
			targetPackages = []*types.Package{pkg}
		}
	} else {
		// Search all packages
		for _, pkg := range workspace.Packages {
			targetPackages = append(targetPackages, pkg)
		}
	}

	for _, pkg := range targetPackages {
		if pkg.Symbols == nil {
			continue
		}
		
		// Look for interfaces that contain this method
		for _, symbol := range pkg.Symbols.Types {
			if symbol.Kind == types.InterfaceSymbol {
				if interfaceMethod := FindMethodInInterface(workspace, symbol, symbolName); interfaceMethod != nil {
					return interfaceMethod
				}
			}
		}
	}
	
	return nil
}

// FindMethodInInterface searches for a method within an interface type
func FindMethodInInterface(workspace *types.Workspace, interfaceSymbol *types.Symbol, methodName string) *types.Symbol {
	pkg := workspace.Packages[interfaceSymbol.Package]
	if pkg == nil {
		return nil
	}

	// Find the file containing the interface
	var interfaceFile *types.File
	for _, file := range pkg.Files {
		if file.Path == interfaceSymbol.File {
			interfaceFile = file
			break
		}
	}

	if interfaceFile == nil || interfaceFile.AST == nil {
		return nil
	}

	// Find the method in the interface by parsing its AST
	var methodSymbol *types.Symbol
	ast.Inspect(interfaceFile.AST, func(n ast.Node) bool {
		if typeSpec, ok := n.(*ast.TypeSpec); ok && typeSpec.Name.Name == interfaceSymbol.Name {
			if interfaceType, ok := typeSpec.Type.(*ast.InterfaceType); ok {
				for _, field := range interfaceType.Methods.List {
					if len(field.Names) > 0 && field.Names[0].Name == methodName {
						methodSymbol = &types.Symbol{
							Name:     field.Names[0].Name,
							Kind:     types.MethodSymbol,
							Package:  interfaceSymbol.Package,
							File:     interfaceFile.Path,
							Position: field.Names[0].Pos(),
							Exported: IsExported(field.Names[0].Name),
						}
						return false
					}
				}
			}
		}
		return true
	})

	return methodSymbol
}

// FindInterfaceContainingMethod finds the interface symbol that contains the given method
func FindInterfaceContainingMethod(workspace *types.Workspace, methodSymbol *types.Symbol) *types.Symbol {
	pkg := workspace.Packages[methodSymbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	// Look for interfaces in the same package
	for _, symbol := range pkg.Symbols.Types {
		if symbol.Kind == types.InterfaceSymbol {
			if FindMethodInInterface(workspace, symbol, methodSymbol.Name) != nil {
				return symbol
			}
		}
	}
	
	return nil
}

// IsExported checks if a symbol name is exported (starts with uppercase)
func IsExported(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}