package commands

import (
	"fmt"
	"os"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/types"
)

// InlineCommand handles various inline operations
func InlineCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: inline requires at least 1 argument: <type>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline <method|variable|function> [arguments...]\n")
		os.Exit(1)
	}

	inlineType := args[0]
	remainingArgs := args[1:]

	switch inlineType {
	case "method":
		InlineMethodCommand(remainingArgs)
	case "variable":
		InlineVariableCommand(remainingArgs)
	case "function":
		InlineFunctionCommand(remainingArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown inline type: %s\n", inlineType)
		fmt.Fprintf(os.Stderr, "Valid types: method, variable, function\n")
		os.Exit(1)
	}
}

// InlineMethodCommand inlines a method call with its implementation
func InlineMethodCommand(args []string) {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: inline method requires 3 arguments: <method-name> <struct-name> <target-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline method processData Calculator main.go\n")
		os.Exit(1)
	}

	methodName := args[0]
	structName := args[1]
	targetFile := args[2]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
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
	ProcessPlan(engine, plan, fmt.Sprintf("Inline method %s from %s in %s", methodName, structName, targetFile))
}

// InlineVariableCommand inlines a variable with its value
func InlineVariableCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: inline variable requires 2 arguments: <variable-name> <source-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline variable tempResult main.go\n")
		os.Exit(1)
	}

	variableName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
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
	ProcessPlan(engine, plan, fmt.Sprintf("Inline variable %s in %s", variableName, sourceFile))
}

// InlineFunctionCommand inlines a function call with its implementation
func InlineFunctionCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: inline function requires 2 arguments: <function-name> <source-file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor inline function utility main.go\n")
		os.Exit(1)
	}

	functionName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
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
	ProcessPlan(engine, plan, fmt.Sprintf("Inline function %s in %s", functionName, sourceFile))
}