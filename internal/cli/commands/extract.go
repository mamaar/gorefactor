package commands

import (
	"fmt"
	"go/token"
	"os"
	"strconv"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// ExtractCommand handles various extraction operations
func ExtractCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: extract requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract <method|interface|variable> [arguments...]\n")
		os.Exit(1)
	}

	extractType := args[0]
	remainingArgs := args[1:]

	switch extractType {
	case "method":
		ExtractMethodCommand(remainingArgs)
	case "function":
		ExtractFunctionCommand(remainingArgs)
	case "interface":
		ExtractInterfaceCommand(remainingArgs)
	case "variable":
		ExtractVariableCommand(remainingArgs)
	case "constant":
		ExtractConstantCommand(remainingArgs)
	case "block":
		ExtractBlockCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown extract type: %s\n", extractType)
		fmt.Fprintf(os.Stderr, "Valid types: method, function, interface, variable, constant, block\n")
		os.Exit(1)
	}
}

// ExtractMethodCommand extracts a method from code lines
func ExtractMethodCommand(args []string) {
	if len(args) < 5 {
		fmt.Fprintf(os.Stderr, "Error: extract method requires 5 arguments: <file> <start-line> <end-line> <method-name> <struct-name>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract method main.go 10 15 newMethod MyStruct\n")
		os.Exit(1)
	}

	sourceFile := args[0]
	startLine, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid start line: %s\n", args[1])
		os.Exit(1)
	}
	endLine, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid end line: %s\n", args[2])
		os.Exit(1)
	}
	methodName := args[3]
	structName := args[4]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create extract method request
	request := types.ExtractMethodRequest{
		SourceFile:    sourceFile,
		StartLine:     startLine,
		EndLine:       endLine,
		NewMethodName: methodName,
		TargetStruct:  structName,
	}

	// Generate refactoring plan
	plan, err := engine.ExtractMethod(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating extract method plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Extract method %s from lines %d-%d in %s", methodName, startLine, endLine, sourceFile))
}

// ExtractFunctionCommand extracts a function from code lines
func ExtractFunctionCommand(args []string) {
	if len(args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: extract function requires 4 arguments: <file> <start-line> <end-line> <function-name>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract function main.go 10 15 newFunction\n")
		os.Exit(1)
	}

	sourceFile := args[0]
	startLine, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid start line: %s\n", args[1])
		os.Exit(1)
	}
	endLine, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid end line: %s\n", args[2])
		os.Exit(1)
	}
	functionName := args[3]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages for extract function
	if *cli.GlobalFlags.Verbose {
		fmt.Fprintf(os.Stderr, "Extract function - Workspace root: %s\n", workspace.RootPath)
		fmt.Fprintf(os.Stderr, "Looking for source file: %s\n", sourceFile)
		fmt.Fprintf(os.Stderr, "Loaded packages:\n")
		for path, pkg := range workspace.Packages {
			fmt.Fprintf(os.Stderr, "  %s -> %s (%s)\n", path, pkg.Name, pkg.Dir)
			for filename, file := range pkg.Files {
				fmt.Fprintf(os.Stderr, "    File: %s -> %s\n", filename, file.Path)
			}
		}
	}

	// Create extract function request
	request := types.ExtractFunctionRequest{
		SourceFile:      sourceFile,
		StartLine:       startLine,
		EndLine:         endLine,
		NewFunctionName: functionName,
	}

	// Generate refactoring plan
	plan, err := engine.ExtractFunction(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating extract function plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Extract function %s from lines %d-%d in %s", functionName, startLine, endLine, sourceFile))
}

// ExtractInterfaceCommand extracts an interface from a struct
func ExtractInterfaceCommand(args []string) {
	if len(args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: extract interface requires 4 arguments: <struct-name> <interface-name> <methods> <target-package>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract interface MyStruct MyInterface Method1,Method2 pkg/interfaces\n")
		os.Exit(1)
	}

	structName := args[0]
	interfaceName := args[1]
	methodsStr := args[2]
	targetPackage := args[3]

	// Parse methods list
	methods := strings.Split(methodsStr, ",")
	for i, method := range methods {
		methods[i] = strings.TrimSpace(method)
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create extract interface request
	request := types.ExtractInterfaceRequest{
		SourceStruct:  structName,
		InterfaceName: interfaceName,
		Methods:       methods,
		TargetPackage: targetPackage,
	}

	// Generate refactoring plan
	plan, err := engine.ExtractInterface(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating extract interface plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Extract interface %s from %s with methods [%s]", interfaceName, structName, strings.Join(methods, ", ")))
}

// ExtractVariableCommand extracts a variable from an expression
func ExtractVariableCommand(args []string) {
	if len(args) < 5 {
		fmt.Fprintf(os.Stderr, "Error: extract variable requires 5 arguments: <file> <start-line> <end-line> <variable-name> <expression>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract variable main.go 20 20 myVar \"someExpression()\"\n")
		os.Exit(1)
	}

	sourceFile := args[0]
	startLine, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid start line: %s\n", args[1])
		os.Exit(1)
	}
	endLine, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid end line: %s\n", args[2])
		os.Exit(1)
	}
	variableName := args[3]
	expression := args[4]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create extract variable request
	request := types.ExtractVariableRequest{
		SourceFile:   sourceFile,
		StartLine:    startLine,
		EndLine:      endLine,
		VariableName: variableName,
		Expression:   expression,
	}

	// Generate refactoring plan
	plan, err := engine.ExtractVariable(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating extract variable plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	ProcessPlan(engine, plan, fmt.Sprintf("Extract variable %s from expression at line %d in %s", variableName, startLine, sourceFile))
}

// ExtractConstantCommand extracts a constant from a literal
func ExtractConstantCommand(args []string) {
	if len(args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: extract constant requires 4 arguments: <file> <line> <column> <constant-name>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract constant main.go 10 5 MyConstant\n")
		os.Exit(1)
	}

	sourceFile := args[0]
	lineNum, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid line number: %s\n", args[1])
		os.Exit(1)
	}
	colNum, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid column number: %s\n", args[2])
		os.Exit(1)
	}
	constantName := args[3]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Convert line/column to token.Pos (simplified)
	position := CalculateTokenPos(workspace, sourceFile, lineNum, colNum)

	// Determine scope
	scope := types.WorkspaceScope
	if *cli.GlobalFlags.PackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.ExtractConstantOperation{
		SourceFile:   sourceFile,
		Position:     position,
		ConstantName: constantName,
		Scope:        scope,
	}

	ExecuteOperation(engine, workspace, operation)
}

// ExtractBlockCommand extracts a block of code to a new function
func ExtractBlockCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: extract block requires 3 arguments: <file> <position> <function-name>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract block main.go 15 newFunction\n")
		fmt.Fprintf(os.Stderr, "  position: line number where the target block is located\n")
		os.Exit(1)
	}

	sourceFile := args[0]
	position, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid position: %s\n", args[1])
		os.Exit(1)
	}
	functionName := args[2]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages for extract block
	if *cli.GlobalFlags.Verbose {
		fmt.Fprintf(os.Stderr, "Extract block - Workspace root: %s\n", workspace.RootPath)
		fmt.Fprintf(os.Stderr, "Looking for source file: %s\n", sourceFile)
		for pkgPath, pkg := range workspace.Packages {
			fmt.Fprintf(os.Stderr, "Package: %s\n", pkgPath)
			for fileName := range pkg.Files {
				fmt.Fprintf(os.Stderr, "  File: %s\n", fileName)
			}
		}
	}

	// Create extract block operation
	operation := &refactor.ExtractBlockOperation{
		SourceFile:      sourceFile,
		Position:        position,
		NewFunctionName: functionName,
	}

	// Execute the operation
	ExecuteOperation(engine, workspace, operation)
}

// CalculateTokenPos is a helper function to calculate token position
func CalculateTokenPos(workspace *types.Workspace, filename string, line, col int) token.Pos {
	// This is a simplified implementation
	// In practice, we would need to use the FileSet to properly calculate positions
	for _, pkg := range workspace.Packages {
		if file, exists := pkg.Files[filename]; exists {
			content := file.OriginalContent
			currentLine := 1
			currentCol := 1

			for i, char := range content {
				if currentLine == line && currentCol == col {
					return token.Pos(i)
				}

				if char == '\n' {
					currentLine++
					currentCol = 1
				} else {
					currentCol++
				}
			}
		}
	}

	return token.Pos(1) // Fallback
}