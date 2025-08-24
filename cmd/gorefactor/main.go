package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

const version = "0.1.0"

// Command line flags
var (
	flagVersion         = flag.Bool("version", false, "Show version information")
	flagWorkspace       = flag.String("workspace", ".", "Path to workspace root (defaults to current directory)")
	flagDryRun          = flag.Bool("dry-run", false, "Preview changes without applying them")
	flagJson            = flag.Bool("json", false, "Output results in JSON format")
	flagVerbose         = flag.Bool("verbose", false, "Enable verbose output")
	flagForce           = flag.Bool("force", false, "Force operation even with warnings")
	flagBackup          = flag.Bool("backup", true, "Create backup files before making changes")
	flagPackageOnly     = flag.Bool("package-only", false, "For rename operations, only rename within the specified package")
	flagCreateTarget    = flag.Bool("create-target", true, "Create target package if it doesn't exist")
	flagSkipCompilation = flag.Bool("skip-compilation", false, "Skip compilation validation after refactoring")
	flagAllowBreaking         = flag.Bool("allow-breaking", false, "Allow potentially breaking refactorings that may require manual fixes")
	flagMinComplexity         = flag.Int("min-complexity", 10, "Minimum complexity threshold for complexity analysis")
	flagRenameImplementations = flag.Bool("rename-implementations", false, "When renaming interface methods, also rename all implementations")
)

// Subcommands
var commands = map[string]func([]string){
	"move":       moveCommand,
	"rename":     renameCommand,
	"extract":    extractCommand,
	"inline":     inlineCommand,
	"analyze":    analyzeCommand,
	"complexity": complexityCommand,
	"change":     changeCommand,
	"delete":     deleteCommand,
	"help":       helpCommand,
}

func parseFlags() {
	flag.Usage = usage
	flag.Parse()

}
func main() {
	log.SetFlags(0) // Remove timestamp from log output
	parseFlags()
	if *flagVersion {
		fmt.Printf("gorefactor version %s\n", version)
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	if fn, ok := commands[cmd]; ok {
		fn(args[1:])
	} else {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `GoRefactor - Safe Go code refactoring tool

Usage: gorefactor [options] <command> [arguments]

Commands:
  move <symbol> <from-package> <to-package>
    Move a symbol from one package to another

  rename <symbol> <new-name> [package]
    Rename a symbol (optionally limited to a specific package)

  extract <type> [arguments...]
    Extract method, function, interface, variable, or block
    Types: method, function, interface, variable, block

  inline <type> [arguments...]
    Inline method, variable, or function calls
    Types: method, variable, function

  analyze <symbol> [package]
    Analyze the impact of refactoring a symbol

  complexity [package]
    Analyze cyclomatic complexity of functions in workspace or package

  help [command]
    Show help for a specific command

Options:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Examples:
  # Move a function from one package to another
  gorefactor move MyFunction pkg/old pkg/new

  # Rename a type across the entire workspace
  gorefactor rename OldType NewType

  # Rename a function only within a specific package
  gorefactor --package-only rename oldFunc newFunc pkg/mypackage

  # Rename an interface method and all its implementations
  gorefactor --rename-implementations rename Execute Process

  # Extract a method from lines 10-15 in main.go
  gorefactor extract method main.go 10 15 newMethod MyStruct

  # Extract a function from lines 10-15 in main.go
  gorefactor extract function main.go 10 15 newFunction

  # Extract an interface from a struct
  gorefactor extract interface MyStruct MyInterface Method1,Method2 pkg/interfaces

  # Extract a variable from an expression
  gorefactor extract variable main.go 20 20 myVar "someComplexExpression()"

  # Inline a method call with its implementation
  gorefactor inline method processData Calculator main.go

  # Inline a variable with its value
  gorefactor inline variable tempResult main.go

  # Inline a function call with its implementation
  gorefactor inline function utility main.go

  # Preview changes without applying them
  gorefactor --dry-run move MyStruct pkg/models pkg/types

  # Analyze impact before refactoring
  gorefactor analyze MyFunction pkg/utils
`)
}

// createEngineWithFlags creates an engine with configuration based on command line flags
func createEngineWithFlags() refactor.RefactorEngine {
	config := &refactor.EngineConfig{
		SkipCompilation: *flagSkipCompilation,
		AllowBreaking:   *flagAllowBreaking,
	}
	return refactor.CreateEngineWithConfig(config)
}

func moveCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: move requires 3 arguments: <symbol> <from-package> <to-package>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor move MyFunction pkg/old pkg/new\n")
		os.Exit(1)
	}

	symbolName := args[0]
	fromPackage := args[1]
	toPackage := args[2]

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages
	if *flagVerbose {
		fmt.Fprintf(os.Stderr, "Workspace root: %s\n", workspace.RootPath)
		fmt.Fprintf(os.Stderr, "Loaded packages:\n")
		for path, pkg := range workspace.Packages {
			fmt.Fprintf(os.Stderr, "  %s -> %s (%s)\n", path, pkg.Name, pkg.Dir)
			if pkg.Name == "main" {
				fmt.Fprintf(os.Stderr, "    Files in main package:\n")
				for filename := range pkg.Files {
					fmt.Fprintf(os.Stderr, "      %s\n", filename)
				}
			}
		}
	}

	// Resolve package paths to actual workspace keys
	resolvedFromPackage := resolvePackagePath(workspace, fromPackage)
	resolvedToPackage := resolvePackagePath(workspace, toPackage)

	// Create move request
	request := types.MoveSymbolRequest{
		SymbolName:   symbolName,
		FromPackage:  resolvedFromPackage,
		ToPackage:    resolvedToPackage,
		CreateTarget: *flagCreateTarget,
	}

	// Generate refactoring plan
	plan, err := engine.MoveSymbol(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	processPlan(engine, plan, fmt.Sprintf("Move %s from %s to %s", symbolName, fromPackage, toPackage))
}

func renameCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: rename requires at least 2 arguments: <symbol> <new-name> [package]\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor rename OldName NewName [pkg/optional]\n")
		os.Exit(1)
	}

	symbolName := args[0]
	newName := args[1]
	packagePath := ""

	if len(args) > 2 {
		packagePath = args[2]
	}

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Check if this is an interface method rename by searching for the symbol first
	if interfaceMethod := findInterfaceMethodSymbol(workspace, symbolName, packagePath); interfaceMethod != nil {
		// Handle interface method rename specially
		handleInterfaceMethodRename(engine, workspace, interfaceMethod, symbolName, newName, packagePath)
		return
	}

	// Create rename request for regular symbols
	request := types.RenameSymbolRequest{
		SymbolName: symbolName,
		NewName:    newName,
		Package:    packagePath,
	}

	if *flagPackageOnly && packagePath == "" {
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
	processPlan(engine, plan, description)
}

func extractCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: extract requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor extract <method|interface|variable> [arguments...]\n")
		os.Exit(1)
	}

	extractType := args[0]
	remainingArgs := args[1:]

	switch extractType {
	case "method":
		extractMethodCommand(remainingArgs)
	case "function":
		extractFunctionCommand(remainingArgs)
	case "interface":
		extractInterfaceCommand(remainingArgs)
	case "variable":
		extractVariableCommand(remainingArgs)
	case "constant":
		extractConstantCommand(remainingArgs)
	case "block":
		extractBlockCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown extract type: %s\n", extractType)
		fmt.Fprintf(os.Stderr, "Valid types: method, function, interface, variable, constant, block\n")
		os.Exit(1)
	}
}

func extractMethodCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
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
	processPlan(engine, plan, fmt.Sprintf("Extract method %s from lines %d-%d in %s", methodName, startLine, endLine, sourceFile))
}

func extractFunctionCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages for extract function
	if *flagVerbose {
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
	processPlan(engine, plan, fmt.Sprintf("Extract function %s from lines %d-%d in %s", functionName, startLine, endLine, sourceFile))
}

func extractInterfaceCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
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
	processPlan(engine, plan, fmt.Sprintf("Extract interface %s from %s with methods [%s]", interfaceName, structName, strings.Join(methods, ", ")))
}

func extractVariableCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
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
	processPlan(engine, plan, fmt.Sprintf("Extract variable %s from expression at line %d in %s", variableName, startLine, sourceFile))
}

func inlineCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: inline requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline <method|variable|function> [arguments...]\n")
		os.Exit(1)
	}

	inlineType := args[0]
	remainingArgs := args[1:]

	switch inlineType {
	case "method":
		inlineMethodCommand(remainingArgs)
	case "variable":
		inlineVariableCommand(remainingArgs)
	case "function":
		inlineFunctionCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown inline type: %s\n", inlineType)
		fmt.Fprintf(os.Stderr, "Valid types: method, variable, function\n")
		os.Exit(1)
	}
}

func inlineMethodCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: inline method requires 3 arguments: <method-name> <struct-name> <target-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline method processData Calculator main.go\n")
		os.Exit(1)
	}

	methodName := args[0]
	structName := args[1]
	targetFile := args[2]

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create inline method request
	request := types.InlineMethodRequest{
		MethodName:   methodName,
		SourceStruct: structName,
		TargetFile:   targetFile,
	}

	// Generate refactoring plan
	plan, err := engine.InlineMethod(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating inline method plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	processPlan(engine, plan, fmt.Sprintf("Inline method %s from %s in %s", methodName, structName, targetFile))
}

func inlineVariableCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: inline variable requires 2 arguments: <variable-name> <source-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline variable tempResult main.go\n")
		os.Exit(1)
	}

	variableName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create inline variable request
	request := types.InlineVariableRequest{
		VariableName: variableName,
		SourceFile:   sourceFile,
		TargetFiles:  []string{sourceFile}, // Default to same file
	}

	// Generate refactoring plan
	plan, err := engine.InlineVariable(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating inline variable plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	processPlan(engine, plan, fmt.Sprintf("Inline variable %s in %s", variableName, sourceFile))
}

func inlineFunctionCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: inline function requires 2 arguments: <function-name> <source-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline function utility main.go\n")
		os.Exit(1)
	}

	functionName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create inline function request
	request := types.InlineFunctionRequest{
		FunctionName: functionName,
		SourceFile:   sourceFile,
		TargetFiles:  []string{sourceFile}, // Default to same file
	}

	// Generate refactoring plan
	plan, err := engine.InlineFunction(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating inline function plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	processPlan(engine, plan, fmt.Sprintf("Inline function %s in %s", functionName, sourceFile))
}

func analyzeCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: analyze requires at least 1 argument: <symbol> [package]\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor analyze MyFunction [pkg/optional]\n")
		os.Exit(1)
	}

	symbolName := args[0]
	packagePath := ""

	if len(args) > 1 {
		packagePath = args[1]
	}

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Find the symbol
	var symbol *types.Symbol
	if packagePath != "" {
		// Look in specific package
		if pkg, exists := workspace.Packages[packagePath]; exists {
			symbol = findSymbolInPackage(pkg, symbolName)
		}
	} else {
		// Search all packages
		for _, pkg := range workspace.Packages {
			if s := findSymbolInPackage(pkg, symbolName); s != nil {
				symbol = s
				break
			}
		}
	}

	if symbol == nil {
		fmt.Fprintf(os.Stderr, "Error: symbol %s not found", symbolName)
		if packagePath != "" {
			fmt.Fprintf(os.Stderr, " in package %s", packagePath)
		}
		fmt.Fprintf(os.Stderr, "\n")
		os.Exit(1)
	}

	// Output analysis
	if *flagJson {
		outputJSON(map[string]interface{}{
			"symbol":   symbol,
			"package":  symbol.Package,
			"file":     symbol.File,
			"kind":     symbol.Kind.String(),
			"exported": symbol.Exported,
		})
	} else {
		fmt.Printf("Symbol Analysis: %s\n", symbolName)
		fmt.Printf("================\n")
		fmt.Printf("Package: %s\n", symbol.Package)
		fmt.Printf("File: %s:%d:%d\n", symbol.File, symbol.Line, symbol.Column)
		fmt.Printf("Kind: %s\n", getSymbolKindName(symbol.Kind))
		fmt.Printf("Exported: %v\n", symbol.Exported)

		if symbol.Signature != "" {
			fmt.Printf("Signature: %s\n", symbol.Signature)
		}

		if len(symbol.References) > 0 {
			fmt.Printf("\nReferences: %d found\n", len(symbol.References))
			if *flagVerbose {
				for i, ref := range symbol.References {
					fmt.Printf("  %d. %s:%d:%d\n", i+1, ref.File, ref.Line, ref.Column)
				}
			}
		}
	}
}

func helpCommand(args []string) {
	if len(args) > 0 {
		cmd := args[0]
		switch cmd {
		case "move":
			fmt.Println(`Move Command - Move a symbol from one package to another

Usage: gorefactor move <symbol> <from-package> <to-package>

Arguments:
  symbol        The name of the symbol to move (function, type, const, or var)
  from-package  The source package path (e.g., pkg/old)
  to-package    The destination package path (e.g., pkg/new)

The move command will:
  - Move the symbol definition to the target package
  - Update all imports in files that reference the symbol
  - Create the target package if it doesn't exist
  - Ensure no import cycles are created
  - Validate that the symbol can be safely moved

Examples:
  gorefactor move MyFunction pkg/utils pkg/helpers
  gorefactor --dry-run move UserType internal/models pkg/types`)

		case "rename":
			fmt.Println(`Rename Command - Rename a symbol

Usage: gorefactor rename <symbol> <new-name> [package]

Arguments:
  symbol     The current name of the symbol
  new-name   The new name for the symbol
  package    Optional: limit renaming to this package only

The rename command will:
  - Rename the symbol definition
  - Update all references to the symbol
  - Ensure the new name is a valid Go identifier
  - Check for naming conflicts
  - Preserve type safety and interface compliance

Examples:
  gorefactor rename OldFunction NewFunction
  gorefactor rename User Account pkg/models
  gorefactor --package-only rename helper utility pkg/internal`)

		case "extract":
			fmt.Println(`Extract Command - Extract method, function, interface, or variable

Usage: gorefactor extract <type> [arguments...]

Types:
  method     Extract a method from code lines
  function   Extract a function from code lines
  interface  Extract an interface from a struct
  variable   Extract a variable from an expression

Method extraction:
  gorefactor extract method <file> <start-line> <end-line> <method-name> <struct-name>

Function extraction:
  gorefactor extract function <file> <start-line> <end-line> <function-name>

Interface extraction:
  gorefactor extract interface <struct-name> <interface-name> <methods> <target-package>

Variable extraction:
  gorefactor extract variable <file> <start-line> <end-line> <variable-name> <expression>

Examples:
  gorefactor extract method main.go 10 15 processData MyStruct
  gorefactor extract function main.go 10 15 calculateSum
  gorefactor extract interface MyStruct Processor Process,Validate pkg/interfaces
  gorefactor extract variable main.go 20 20 result "calculateValue(x, y)"`)

		case "inline":
			fmt.Println(`Inline Command - Inline method, variable, or function calls

Usage: gorefactor inline <type> [arguments...]

Types:
  method     Inline a method call with its implementation
  variable   Inline a variable with its value
  function   Inline a function call with its implementation

Method inlining:
  gorefactor inline method <method-name> <struct-name> <target-file>

Variable inlining:
  gorefactor inline variable <variable-name> <source-file>

Function inlining:
  gorefactor inline function <function-name> <source-file>

Examples:
  gorefactor inline method processData Calculator main.go
  gorefactor inline variable tempResult main.go
  gorefactor inline function utility main.go`)

		case "analyze":
			fmt.Println(`Analyze Command - Analyze a symbol and its usage

Usage: gorefactor analyze <symbol> [package]

Arguments:
  symbol     The name of the symbol to analyze
  package    Optional: look for the symbol in this package only

The analyze command will show:
  - Symbol location and type
  - Export status
  - Number of references
  - Reference locations (with --verbose)

Examples:
  gorefactor analyze MyFunction
  gorefactor analyze Config pkg/config
  gorefactor --verbose analyze DatabaseConnection`)

		case "complexity":
			fmt.Println(`Complexity Command - Analyze cyclomatic complexity of functions

Usage: gorefactor complexity [package]

Arguments:
  package    Optional: analyze only this package (otherwise analyzes entire workspace)

Options:
  --min-complexity N    Only show functions with complexity >= N (default: 10)
  --json               Output results in JSON format
  --verbose            Show detailed metrics for each function

The complexity command will show:
  - Cyclomatic complexity (decision points + 1)
  - Cognitive complexity (human readability metric)
  - Lines of code, parameters, local variables
  - Maximum nesting depth
  - Complexity classification (low, moderate, high, very_high, extreme)

Complexity Thresholds:
  1-4:   Low complexity (easy to understand and maintain)
  5-9:   Moderate complexity (still manageable)
  10-14: High complexity (consider refactoring)
  15-19: Very high complexity (refactoring recommended)
  20+:   Extreme complexity (refactoring strongly recommended)

Examples:
  gorefactor complexity
  gorefactor complexity pkg/handlers
  gorefactor --min-complexity 5 complexity
  gorefactor --json complexity pkg/utils`)

		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
			usage()
		}
	} else {
		usage()
	}
}

func processPlan(engine refactor.RefactorEngine, plan *types.RefactoringPlan, description string) {
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
	if *flagJson {
		outputJSON(plan)
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
		if *flagVerbose {
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

	if hasWarnings && !*flagForce {
		fmt.Fprintf(os.Stderr, "\nWarnings found. Use --force to proceed anyway\n")
		os.Exit(1)
	}

	// Preview or execute
	if *flagDryRun {
		fmt.Println("\nDry run mode - no changes will be applied")

		// Show preview
		preview, err := engine.PreviewPlan(plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error generating preview: %v\n", err)
			os.Exit(1)
		}

		if *flagVerbose {
			fmt.Printf("\nDetailed Preview:\n%s\n", preview)
		}
	} else {
		// Create backups if requested
		if *flagBackup {
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
				if *flagVerbose {
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

func findSymbolInPackage(pkg *types.Package, symbolName string) *types.Symbol {
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

func getSymbolKindName(kind types.SymbolKind) string {
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

func outputJSON(data interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// resolvePackagePath resolves a user-provided package reference to an actual workspace package key
func resolvePackagePath(workspace *types.Workspace, userPath string) string {
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

// Helper function to execute operations
func executeOperation(engine refactor.RefactorEngine, workspace *types.Workspace, operation types.Operation) {
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
	if *flagDryRun {
		fmt.Println("=== DRY RUN MODE ===")
		fmt.Printf("Operation: %s\n", operation.Description())
		fmt.Printf("Affected files: %d\n", len(plan.AffectedFiles))

		if *flagVerbose {
			for _, file := range plan.AffectedFiles {
				fmt.Printf("  - %s\n", file)
			}
		}

		fmt.Printf("Changes: %d\n", len(plan.Changes))
		if *flagVerbose {
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

// New command implementations

func extractConstantCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Convert line/column to token.Pos (simplified)
	position := calculateTokenPos(workspace, sourceFile, lineNum, colNum)

	// Determine scope
	scope := types.WorkspaceScope
	if *flagPackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.ExtractConstantOperation{
		SourceFile:   sourceFile,
		Position:     position,
		ConstantName: constantName,
		Scope:        scope,
	}

	executeOperation(engine, workspace, operation)
}

func extractBlockCommand(args []string) {
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
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Debug: Print loaded packages for extract block
	if *flagVerbose {
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
	executeOperation(engine, workspace, operation)
}

func changeCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: change requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor change <signature> [arguments...]\n")
		os.Exit(1)
	}

	changeType := args[0]
	remainingArgs := args[1:]

	switch changeType {
	case "signature":
		changeSignatureCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown change type: %s\n", changeType)
		fmt.Fprintf(os.Stderr, "Valid types: signature\n")
		os.Exit(1)
	}
}

func changeSignatureCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: change signature requires at least 3 arguments: <function-name> <file> <new-params>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor change signature myFunc main.go 'param1:string,param2:int' [returns]\n")
		os.Exit(1)
	}

	functionName := args[0]
	sourceFile := args[1]
	paramsStr := args[2]

	// Parse parameters
	var newParams []refactor.Parameter
	if paramsStr != "" {
		paramPairs := strings.Split(paramsStr, ",")
		for _, pair := range paramPairs {
			parts := strings.Split(strings.TrimSpace(pair), ":")
			if len(parts) == 2 {
				newParams = append(newParams, refactor.Parameter{
					Name: strings.TrimSpace(parts[0]),
					Type: strings.TrimSpace(parts[1]),
				})
			}
		}
	}

	// Parse return types
	var newReturns []string
	if len(args) > 3 {
		returnStr := args[3]
		if returnStr != "" {
			newReturns = strings.Split(returnStr, ",")
			for i, ret := range newReturns {
				newReturns[i] = strings.TrimSpace(ret)
			}
		}
	}

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Determine scope
	scope := types.WorkspaceScope
	if *flagPackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.ChangeSignatureOperation{
		FunctionName: functionName,
		SourceFile:   sourceFile,
		NewParams:    newParams,
		NewReturns:   newReturns,
		Scope:        scope,
	}

	executeOperation(engine, workspace, operation)
}

func deleteCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: delete requires 2 arguments: <symbol-name> <file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor delete MySymbol main.go\n")
		os.Exit(1)
	}

	symbolName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Determine scope
	scope := types.WorkspaceScope
	if *flagPackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.SafeDeleteOperation{
		SymbolName: symbolName,
		SourceFile: sourceFile,
		Scope:      scope,
		Force:      *flagForce,
	}

	executeOperation(engine, workspace, operation)
}

// Helper function to calculate token position (simplified)
func calculateTokenPos(workspace *types.Workspace, filename string, line, col int) token.Pos {
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

func complexityCommand(args []string) {
	packagePath := ""
	if len(args) > 0 {
		packagePath = args[0]
	}

	// Load workspace
	engine := createEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*flagWorkspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create complexity analyzer
	analyzer := analysis.NewComplexityAnalyzer(workspace, *flagMinComplexity)

	var results []*analysis.ComplexityResult
	if packagePath != "" {
		// Analyze specific package
		resolvedPackage := resolvePackagePath(workspace, packagePath)
		pkg, exists := workspace.Packages[resolvedPackage]
		if !exists {
			fmt.Fprintf(os.Stderr, "Error: package not found: %s\n", packagePath)
			os.Exit(1)
		}

		results, err = analyzer.AnalyzePackage(pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error analyzing package complexity: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Analyze entire workspace
		results, err = analyzer.AnalyzeWorkspace()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error analyzing workspace complexity: %v\n", err)
			os.Exit(1)
		}
	}

	// Output results
	if *flagJson {
		outputJSON(map[string]interface{}{
			"complexityResults": results,
			"thresholds":        analysis.GetComplexityThresholds(),
			"minComplexity":     *flagMinComplexity,
		})
	} else {
		fmt.Printf("Complexity Analysis Report\n")
		fmt.Printf("==========================\n")
		if packagePath != "" {
			fmt.Printf("Package: %s\n", packagePath)
		} else {
			fmt.Printf("Workspace: %s\n", workspace.RootPath)
		}
		fmt.Printf("Minimum complexity threshold: %d\n\n", *flagMinComplexity)

		report := analysis.FormatComplexityReport(results)
		fmt.Print(report)

		// Summary statistics
		if len(results) > 0 {
			fmt.Printf("\nSummary:\n")
			fmt.Printf("========\n")
			
			// Count by complexity level
			counts := make(map[string]int)
			totalComplexity := 0
			for _, result := range results {
				level := analysis.ClassifyComplexity(result.Metrics.CyclomaticComplexity)
				counts[level]++
				totalComplexity += result.Metrics.CyclomaticComplexity
			}
			
			fmt.Printf("Total functions analyzed: %d\n", len(results))
			fmt.Printf("Average complexity: %.1f\n", float64(totalComplexity)/float64(len(results)))
			
			for level, count := range counts {
				if count > 0 {
					fmt.Printf("%s complexity: %d functions\n", strings.Title(strings.Replace(level, "_", " ", -1)), count)
				}
			}
		}
	}
}

// findInterfaceMethodSymbol searches for an interface method symbol
func findInterfaceMethodSymbol(workspace *types.Workspace, symbolName, packagePath string) *types.Symbol {
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
				if interfaceMethod := findMethodInInterface(workspace, symbol, symbolName); interfaceMethod != nil {
					return interfaceMethod
				}
			}
		}
	}
	
	return nil
}

// findMethodInInterface searches for a method within an interface type
func findMethodInInterface(workspace *types.Workspace, interfaceSymbol *types.Symbol, methodName string) *types.Symbol {
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
							Exported: isExported(field.Names[0].Name),
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

// handleInterfaceMethodRename handles the special case of renaming interface methods
func handleInterfaceMethodRename(engine refactor.RefactorEngine, workspace *types.Workspace, methodSymbol *types.Symbol, oldName, newName, packagePath string) {
	// Find the interface that contains this method
	interfaceSymbol := findInterfaceContainingMethod(workspace, methodSymbol)
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
		UpdateImplementations: *flagRenameImplementations,
	}

	// Generate refactoring plan
	plan, err := engine.RenameInterfaceMethod(workspace, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating interface method refactoring plan: %v\n", err)
		os.Exit(1)
	}

	// Process the plan
	var description string
	if *flagRenameImplementations {
		description = fmt.Sprintf("Rename interface method %s.%s to %s (including implementations)", interfaceSymbol.Name, oldName, newName)
	} else {
		description = fmt.Sprintf("Rename interface method %s.%s to %s", interfaceSymbol.Name, oldName, newName)
	}
	
	if packagePath != "" {
		description += fmt.Sprintf(" in package %s", packagePath)
	}
	
	processPlan(engine, plan, description)
}

// findInterfaceContainingMethod finds the interface symbol that contains the given method
func findInterfaceContainingMethod(workspace *types.Workspace, methodSymbol *types.Symbol) *types.Symbol {
	pkg := workspace.Packages[methodSymbol.Package]
	if pkg == nil || pkg.Symbols == nil {
		return nil
	}

	// Look for interfaces in the same package
	for _, symbol := range pkg.Symbols.Types {
		if symbol.Kind == types.InterfaceSymbol {
			if findMethodInInterface(workspace, symbol, methodSymbol.Name) != nil {
				return symbol
			}
		}
	}
	
	return nil
}

// isExported checks if a symbol name is exported (starts with uppercase)
func isExported(name string) bool {
	return len(name) > 0 && unicode.IsUpper(rune(name[0]))
}
