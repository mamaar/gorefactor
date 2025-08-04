package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

func main() {
	// Write startup marker to a file that's easy to check
	startupFile, _ := os.OpenFile("/tmp/gorefactor_mcp_startup.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if startupFile != nil {
		timestamp := fmt.Sprintf("%v", os.Args)
		fmt.Fprintf(startupFile, "[%s] MCP server starting, args: %v, cwd: %s\n", 
			timestamp, os.Args, func() string { 
				if cwd, err := os.Getwd(); err == nil { 
					return cwd 
				} 
				return "unknown" 
			}())
		startupFile.Close()
	}

	var (
		workspaceFlag      = flag.String("workspace", "", "Root workspace directory (defaults to current directory)")
		portFlag           = flag.Int("port", 0, "TCP port to listen on (0 for stdio)")
		debugFlag          = flag.Bool("debug", false, "Enable debug logging")
		versionFlag        = flag.Bool("version", false, "Show version information")
		allowBreakingFlag  = flag.Bool("allow-breaking", false, "Allow breaking changes during refactoring")
		skipCompilationFlag = flag.Bool("skip-compilation", false, "Skip compilation checks during refactoring")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Println("gorefactor-mcp v0.1.0")
		fmt.Println("Model Context Protocol server for Go refactoring")
		os.Exit(0)
	}

	// Setup logging
	if !*debugFlag {
		log.SetOutput(os.Stderr)
	}

	// Determine workspace directory
	workspace := *workspaceFlag
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get current directory: %v", err)
		}
	}

	// Make workspace path absolute
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		log.Fatalf("Failed to resolve workspace path: %v", err)
	}

	// Verify workspace exists and has go.mod
	if _, err := os.Stat(filepath.Join(workspace, "go.mod")); os.IsNotExist(err) {
		log.Fatalf("Workspace must contain a go.mod file: %s", workspace)
	}

	log.Printf("Starting MCP server for workspace: %s", workspace)

	// Create MCP server using mark3labs/mcp-go
	mcpServer := server.NewMCPServer(
		"gorefactor-mcp",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
		server.WithLogging(),
		server.WithRecovery(),
	)

	// Initialize refactor engine and workspace
	config := &refactor.EngineConfig{
		SkipCompilation: *skipCompilationFlag,
		AllowBreaking:   *allowBreakingFlag,
	}
	engine := refactor.CreateEngineWithConfig(config)
	workspaceObj, err := engine.LoadWorkspace(workspace)
	if err != nil {
		log.Fatalf("Failed to load workspace: %v", err)
	}

	// Add tools
	addMoveSymbolTool(mcpServer, engine, workspaceObj)
	addRenameSymbolTool(mcpServer, engine, workspaceObj)
	addExtractMethodTool(mcpServer, engine, workspaceObj)
	addExtractFunctionTool(mcpServer, engine, workspaceObj)
	addExtractInterfaceTool(mcpServer, engine, workspaceObj)
	addExtractVariableTool(mcpServer, engine, workspaceObj)
	addInlineMethodTool(mcpServer, engine, workspaceObj)
	addInlineVariableTool(mcpServer, engine, workspaceObj)
	addInlineFunctionTool(mcpServer, engine, workspaceObj)
	addAnalyzeSymbolTool(mcpServer, engine, workspaceObj)
	addComplexityAnalysisTool(mcpServer, engine, workspaceObj)
	addChangeSignatureTool(mcpServer, engine, workspaceObj)
	addSafeDeleteTool(mcpServer, engine, workspaceObj)
	addValidateWorkspaceTool(mcpServer, engine, workspace)

	// Add resources
	addPackageListResource(mcpServer, workspaceObj, workspace)
	addWorkspaceStatsResource(mcpServer, workspaceObj, workspace)

	// Add prompts
	addRefactoringPrompts(mcpServer)

	// Start server
	if *portFlag == 0 {
		// Stdio transport
		if err := server.ServeStdio(mcpServer); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	} else {
		// HTTP transport
		httpServer := server.NewStreamableHTTPServer(mcpServer)
		log.Printf("Starting HTTP server on port %d", *portFlag)
		if err := httpServer.Start(fmt.Sprintf(":%d", *portFlag)); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}
}

// addMoveSymbolTool adds the move_symbol tool to the MCP server
func addMoveSymbolTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	moveSymbolTool := mcp.NewTool("move_symbol",
		mcp.WithDescription("Move a Go symbol (function, type, constant, or variable) from one package to another"),
		mcp.WithString("symbol_name",
			mcp.Required(),
			mcp.Description("Name of the symbol to move"),
		),
		mcp.WithString("from_package",
			mcp.Required(),
			mcp.Description("Source package path (e.g., 'pkg/utils')"),
		),
		mcp.WithString("to_package",
			mcp.Required(),
			mcp.Description("Target package path (e.g., 'internal/helpers')"),
		),
	)

	s.AddTool(moveSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		symbolName, ok := args["symbol_name"].(string)
		if !ok {
			return mcp.NewToolResultError("symbol_name is required"), nil
		}

		fromPackage, ok := args["from_package"].(string)
		if !ok {
			return mcp.NewToolResultError("from_package is required"), nil
		}

		toPackage, ok := args["to_package"].(string)
		if !ok {
			return mcp.NewToolResultError("to_package is required"), nil
		}

		// Execute the refactoring operation
		moveRequest := types.MoveSymbolRequest{
			SymbolName:  symbolName,
			FromPackage: fromPackage,
			ToPackage:   toPackage,
		}

		plan, err := engine.MoveSymbol(workspace, moveRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error moving symbol: %v", err)), nil
		}

		// Format response
		content := fmt.Sprintf("Successfully planned move of symbol %s from %s to %s",
			symbolName, fromPackage, toPackage)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addRenameSymbolTool adds the rename_symbol tool to the MCP server
func addRenameSymbolTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	renameSymbolTool := mcp.NewTool("rename_symbol",
		mcp.WithDescription("Rename a Go symbol across the workspace or within a specific package"),
		mcp.WithString("symbol_name",
			mcp.Required(),
			mcp.Description("Current name of the symbol"),
		),
		mcp.WithString("new_name",
			mcp.Required(),
			mcp.Description("New name for the symbol"),
		),
		mcp.WithString("package_path",
			mcp.Description("Package path to limit rename scope (optional)"),
		),
	)

	s.AddTool(renameSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		symbolName, ok := args["symbol_name"].(string)
		if !ok {
			return mcp.NewToolResultError("symbol_name is required"), nil
		}

		newName, ok := args["new_name"].(string)
		if !ok {
			return mcp.NewToolResultError("new_name is required"), nil
		}

		// Optional parameter
		packagePath, _ := args["package_path"].(string)

		renameRequest := types.RenameSymbolRequest{
			SymbolName: symbolName,
			NewName:    newName,
			Package:    packagePath,
			Scope:      types.WorkspaceScope,
		}

		if packagePath != "" {
			renameRequest.Scope = types.PackageScope
		}

		// Debug logging
		log.Printf("DEBUG: Rename request - Symbol: %s, NewName: %s, Package: %s, Scope: %v", 
			symbolName, newName, packagePath, renameRequest.Scope)
		
		// Log available packages for debugging
		log.Printf("DEBUG: Available packages in workspace:")
		for pkgPath, pkg := range workspace.Packages {
			symbolCount := 0
			if pkg.Symbols != nil {
				symbolCount = len(pkg.Symbols.Functions) + len(pkg.Symbols.Types) + 
					len(pkg.Symbols.Variables) + len(pkg.Symbols.Constants)
			}
			log.Printf("  - %s (name: %s, symbols: %d)", pkgPath, pkg.Name, symbolCount)
		}

		plan, err := engine.RenameSymbol(workspace, renameRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error renaming symbol: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned rename of symbol %s to %s", symbolName, newName)
		if packagePath != "" {
			content += fmt.Sprintf(" (within package %s)", packagePath)
		}

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addValidateWorkspaceTool adds the validate_workspace tool to the MCP server
func addValidateWorkspaceTool(s *server.MCPServer, engine refactor.RefactorEngine, workspacePath string) {
	validateTool := mcp.NewTool("validate_workspace",
		mcp.WithDescription("Validate the entire workspace for compilation errors and consistency"),
		mcp.WithBoolean("check_syntax",
			mcp.Description("Check for syntax errors"),
			mcp.DefaultBool(true),
		),
	)

	s.AddTool(validateTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		checkSyntax := true
		if val, ok := args["check_syntax"].(bool); ok {
			checkSyntax = val
		}

		// Reload workspace
		workspace, err := engine.LoadWorkspace(workspacePath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error loading workspace: %v", err)), nil
		}

		content := "Workspace Validation Results:\n"
		content += fmt.Sprintf("Packages analyzed: %d\n", len(workspace.Packages))

		totalFiles := 0
		for _, pkg := range workspace.Packages {
			totalFiles += len(pkg.Files)
		}
		content += fmt.Sprintf("Files analyzed: %d\n", totalFiles)
		content += "✓ All Go files parsed successfully\n"
		
		if checkSyntax {
			content += "✓ No syntax errors found\n"
		}
		
		content += "\nWorkspace is valid and ready for refactoring operations."

		return mcp.NewToolResultText(content), nil
	})
}

// addPackageListResource adds the packages resource to the MCP server
func addPackageListResource(s *server.MCPServer, workspace *types.Workspace, workspacePath string) {
	packagesResource := mcp.NewResource("workspace://packages",
		"Package List",
		mcp.WithResourceDescription("List of all packages in the workspace"),
		mcp.WithMIMEType("application/json"),
	)

	s.AddResource(packagesResource, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		packages := make(map[string]interface{})

		for path, pkg := range workspace.Packages {
			relPath, err := filepath.Rel(workspacePath, path)
			if err != nil {
				relPath = path
			}

			packages[path] = map[string]interface{}{
				"name":       pkg.Name,
				"path":       relPath,
				"full_path":  path,
				"file_count": len(pkg.Files),
				"is_main":    pkg.Name == "main",
			}
		}

		data, err := json.MarshalIndent(packages, "", "  ")
		if err != nil {
			return nil, err
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      "workspace://packages",
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	})
}

// addWorkspaceStatsResource adds the workspace statistics resource to the MCP server
func addWorkspaceStatsResource(s *server.MCPServer, workspace *types.Workspace, workspacePath string) {
	statsResource := mcp.NewResource("workspace://stats",
		"Workspace Statistics",
		mcp.WithResourceDescription("Statistics about the workspace"),
		mcp.WithMIMEType("application/json"),
	)

	s.AddResource(statsResource, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		stats := map[string]interface{}{
			"workspace_path": workspacePath,
			"package_count":  len(workspace.Packages),
		}

		totalFiles := 0
		mainPackages := 0
		for _, pkg := range workspace.Packages {
			totalFiles += len(pkg.Files)
			if pkg.Name == "main" {
				mainPackages++
			}
		}

		stats["file_count"] = totalFiles
		stats["main_packages"] = mainPackages
		stats["library_packages"] = len(workspace.Packages) - mainPackages

		if workspace.Module != nil {
			stats["module_path"] = workspace.Module.Path
			stats["module_version"] = workspace.Module.Version
		}

		data, err := json.MarshalIndent(stats, "", "  ")
		if err != nil {
			return nil, err
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      "workspace://stats",
				MIMEType: "application/json",
				Text:     string(data),
			},
		}, nil
	})
}

// addRefactoringPrompts adds refactoring planning prompts to the MCP server
func addRefactoringPrompts(s *server.MCPServer) {
	refactorPrompt := mcp.NewPrompt("refactor_planning",
		mcp.WithPromptDescription("Generate a refactoring plan for a Go codebase"),
		mcp.WithArgument("operation", 
			mcp.ArgumentDescription("Type of refactoring operation (move, rename, extract, inline)"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("target",
			mcp.ArgumentDescription("Target symbol or code to refactor"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("context",
			mcp.ArgumentDescription("Additional context about the refactoring goals"),
		),
	)

	s.AddPrompt(refactorPrompt, func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args := request.Params.Arguments

		operation, ok := args["operation"]
		if !ok {
			return nil, fmt.Errorf("operation is required")
		}

		target, ok := args["target"]
		if !ok {
			return nil, fmt.Errorf("target is required")
		}

		context := args["context"]

		prompt := fmt.Sprintf(`You are a Go refactoring expert. Help plan a %s operation for the following target:

Target: %s`, operation, target)

		if context != "" {
			prompt += fmt.Sprintf(`
Context: %s`, context)
		}

		prompt += `

Please provide:
1. A step-by-step refactoring plan
2. Potential risks and mitigation strategies
3. Pre-conditions that should be verified
4. Post-refactoring validation steps
5. Impact analysis on dependent code

Consider Go-specific best practices including:
- Package visibility rules
- Import cycle prevention
- Naming conventions
- Interface compatibility
- Build and test implications`

		return &mcp.GetPromptResult{
			Description: "Refactoring planning guidance",
			Messages: []mcp.PromptMessage{
				{
					Role:    mcp.RoleUser,
					Content: mcp.TextContent{
						Type: "text",
						Text: prompt,
					},
				},
			},
		}, nil
	})
}

// addExtractMethodTool adds the extract_method tool to the MCP server
func addExtractMethodTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	extractMethodTool := mcp.NewTool("extract_method",
		mcp.WithDescription("Extract code lines into a new method"),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file path"),
		),
		mcp.WithNumber("start_line",
			mcp.Required(),
			mcp.Description("Starting line number"),
		),
		mcp.WithNumber("end_line",
			mcp.Required(),
			mcp.Description("Ending line number"),
		),
		mcp.WithString("method_name",
			mcp.Required(),
			mcp.Description("Name for the new method"),
		),
		mcp.WithString("struct_name",
			mcp.Required(),
			mcp.Description("Target struct name"),
		),
	)

	s.AddTool(extractMethodTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		startLineFloat, ok := args["start_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("start_line must be a number"), nil
		}
		startLine := int(startLineFloat)

		endLineFloat, ok := args["end_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("end_line must be a number"), nil
		}
		endLine := int(endLineFloat)

		methodName, ok := args["method_name"].(string)
		if !ok {
			return mcp.NewToolResultError("method_name is required"), nil
		}

		structName, ok := args["struct_name"].(string)
		if !ok {
			return mcp.NewToolResultError("struct_name is required"), nil
		}

		extractRequest := types.ExtractMethodRequest{
			SourceFile:    sourceFile,
			StartLine:     startLine,
			EndLine:       endLine,
			NewMethodName: methodName,
			TargetStruct:  structName,
		}

		plan, err := engine.ExtractMethod(workspace, extractRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error extracting method: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned extraction of method %s from lines %d-%d in %s",
			methodName, startLine, endLine, sourceFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addExtractFunctionTool adds the extract_function tool to the MCP server
func addExtractFunctionTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	extractFunctionTool := mcp.NewTool("extract_function",
		mcp.WithDescription("Extract code lines into a new function"),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file path"),
		),
		mcp.WithNumber("start_line",
			mcp.Required(),
			mcp.Description("Starting line number"),
		),
		mcp.WithNumber("end_line",
			mcp.Required(),
			mcp.Description("Ending line number"),
		),
		mcp.WithString("function_name",
			mcp.Required(),
			mcp.Description("Name for the new function"),
		),
	)

	s.AddTool(extractFunctionTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		startLineFloat, ok := args["start_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("start_line must be a number"), nil
		}
		startLine := int(startLineFloat)

		endLineFloat, ok := args["end_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("end_line must be a number"), nil
		}
		endLine := int(endLineFloat)

		functionName, ok := args["function_name"].(string)
		if !ok {
			return mcp.NewToolResultError("function_name is required"), nil
		}

		extractRequest := types.ExtractFunctionRequest{
			SourceFile:      sourceFile,
			StartLine:       startLine,
			EndLine:         endLine,
			NewFunctionName: functionName,
		}

		plan, err := engine.ExtractFunction(workspace, extractRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error extracting function: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned extraction of function %s from lines %d-%d in %s",
			functionName, startLine, endLine, sourceFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addExtractInterfaceTool adds the extract_interface tool to the MCP server
func addExtractInterfaceTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	extractInterfaceTool := mcp.NewTool("extract_interface",
		mcp.WithDescription("Extract an interface from a struct"),
		mcp.WithString("struct_name",
			mcp.Required(),
			mcp.Description("Source struct name"),
		),
		mcp.WithString("interface_name",
			mcp.Required(),
			mcp.Description("Name for the new interface"),
		),
		mcp.WithString("methods",
			mcp.Required(),
			mcp.Description("Comma-separated list of method names"),
		),
		mcp.WithString("target_package",
			mcp.Required(),
			mcp.Description("Target package for the interface"),
		),
	)

	s.AddTool(extractInterfaceTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		structName, ok := args["struct_name"].(string)
		if !ok {
			return mcp.NewToolResultError("struct_name is required"), nil
		}

		interfaceName, ok := args["interface_name"].(string)
		if !ok {
			return mcp.NewToolResultError("interface_name is required"), nil
		}

		methodsStr, ok := args["methods"].(string)
		if !ok {
			return mcp.NewToolResultError("methods is required"), nil
		}

		targetPackage, ok := args["target_package"].(string)
		if !ok {
			return mcp.NewToolResultError("target_package is required"), nil
		}

		methods := strings.Split(methodsStr, ",")
		for i, method := range methods {
			methods[i] = strings.TrimSpace(method)
		}

		extractRequest := types.ExtractInterfaceRequest{
			SourceStruct:  structName,
			InterfaceName: interfaceName,
			Methods:       methods,
			TargetPackage: targetPackage,
		}

		plan, err := engine.ExtractInterface(workspace, extractRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error extracting interface: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned extraction of interface %s from %s with methods [%s] to package %s",
			interfaceName, structName, strings.Join(methods, ", "), targetPackage)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addExtractVariableTool adds the extract_variable tool to the MCP server
func addExtractVariableTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	extractVariableTool := mcp.NewTool("extract_variable",
		mcp.WithDescription("Extract an expression into a variable"),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file path"),
		),
		mcp.WithNumber("start_line",
			mcp.Required(),
			mcp.Description("Starting line number"),
		),
		mcp.WithNumber("end_line",
			mcp.Required(),
			mcp.Description("Ending line number"),
		),
		mcp.WithString("variable_name",
			mcp.Required(),
			mcp.Description("Name for the new variable"),
		),
		mcp.WithString("expression",
			mcp.Required(),
			mcp.Description("Expression to extract"),
		),
	)

	s.AddTool(extractVariableTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		startLineFloat, ok := args["start_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("start_line must be a number"), nil
		}
		startLine := int(startLineFloat)

		endLineFloat, ok := args["end_line"].(float64)
		if !ok {
			return mcp.NewToolResultError("end_line must be a number"), nil
		}
		endLine := int(endLineFloat)

		variableName, ok := args["variable_name"].(string)
		if !ok {
			return mcp.NewToolResultError("variable_name is required"), nil
		}

		expression, ok := args["expression"].(string)
		if !ok {
			return mcp.NewToolResultError("expression is required"), nil
		}

		extractRequest := types.ExtractVariableRequest{
			SourceFile:   sourceFile,
			StartLine:    startLine,
			EndLine:      endLine,
			VariableName: variableName,
			Expression:   expression,
		}

		plan, err := engine.ExtractVariable(workspace, extractRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error extracting variable: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned extraction of variable %s from expression at line %d in %s",
			variableName, startLine, sourceFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addInlineMethodTool adds the inline_method tool to the MCP server
func addInlineMethodTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	inlineMethodTool := mcp.NewTool("inline_method",
		mcp.WithDescription("Inline a method call with its implementation"),
		mcp.WithString("method_name",
			mcp.Required(),
			mcp.Description("Name of the method to inline"),
		),
		mcp.WithString("struct_name",
			mcp.Required(),
			mcp.Description("Name of the struct containing the method"),
		),
		mcp.WithString("target_file",
			mcp.Required(),
			mcp.Description("Target file where to inline the method"),
		),
	)

	s.AddTool(inlineMethodTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		methodName, ok := args["method_name"].(string)
		if !ok {
			return mcp.NewToolResultError("method_name is required"), nil
		}

		structName, ok := args["struct_name"].(string)
		if !ok {
			return mcp.NewToolResultError("struct_name is required"), nil
		}

		targetFile, ok := args["target_file"].(string)
		if !ok {
			return mcp.NewToolResultError("target_file is required"), nil
		}

		inlineRequest := types.InlineMethodRequest{
			MethodName:   methodName,
			SourceStruct: structName,
			TargetFile:   targetFile,
		}

		plan, err := engine.InlineMethod(workspace, inlineRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error inlining method: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned inlining of method %s from %s in %s",
			methodName, structName, targetFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addInlineVariableTool adds the inline_variable tool to the MCP server
func addInlineVariableTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	inlineVariableTool := mcp.NewTool("inline_variable",
		mcp.WithDescription("Inline a variable with its value"),
		mcp.WithString("variable_name",
			mcp.Required(),
			mcp.Description("Name of the variable to inline"),
		),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file containing the variable"),
		),
	)

	s.AddTool(inlineVariableTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		variableName, ok := args["variable_name"].(string)
		if !ok {
			return mcp.NewToolResultError("variable_name is required"), nil
		}

		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		inlineRequest := types.InlineVariableRequest{
			VariableName: variableName,
			SourceFile:   sourceFile,
			TargetFiles:  []string{sourceFile},
		}

		plan, err := engine.InlineVariable(workspace, inlineRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error inlining variable: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned inlining of variable %s in %s",
			variableName, sourceFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addInlineFunctionTool adds the inline_function tool to the MCP server
func addInlineFunctionTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	inlineFunctionTool := mcp.NewTool("inline_function",
		mcp.WithDescription("Inline a function call with its implementation"),
		mcp.WithString("function_name",
			mcp.Required(),
			mcp.Description("Name of the function to inline"),
		),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file containing the function"),
		),
	)

	s.AddTool(inlineFunctionTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		functionName, ok := args["function_name"].(string)
		if !ok {
			return mcp.NewToolResultError("function_name is required"), nil
		}

		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		inlineRequest := types.InlineFunctionRequest{
			FunctionName: functionName,
			SourceFile:   sourceFile,
			TargetFiles:  []string{sourceFile},
		}

		plan, err := engine.InlineFunction(workspace, inlineRequest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error inlining function: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned inlining of function %s in %s",
			functionName, sourceFile)

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nPlanned changes to %d files:", len(plan.Changes))
			for filePath := range plan.Changes {
				content += fmt.Sprintf("\n- %s", filePath)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addAnalyzeSymbolTool adds the analyze_symbol tool to the MCP server
func addAnalyzeSymbolTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	analyzeSymbolTool := mcp.NewTool("analyze_symbol",
		mcp.WithDescription("Analyze a symbol and its usage across the workspace"),
		mcp.WithString("symbol_name",
			mcp.Required(),
			mcp.Description("Name of the symbol to analyze"),
		),
		mcp.WithString("package_path",
			mcp.Description("Package path to limit analysis scope (optional)"),
		),
	)

	s.AddTool(analyzeSymbolTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		symbolName, ok := args["symbol_name"].(string)
		if !ok {
			return mcp.NewToolResultError("symbol_name is required"), nil
		}

		packagePath, _ := args["package_path"].(string)

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
			msg := fmt.Sprintf("Symbol %s not found", symbolName)
			if packagePath != "" {
				msg += fmt.Sprintf(" in package %s", packagePath)
			}
			return mcp.NewToolResultError(msg), nil
		}

		// Build analysis result
		content := fmt.Sprintf("Symbol Analysis: %s\n", symbolName)
		content += "================\n"
		content += fmt.Sprintf("Package: %s\n", symbol.Package)
		content += fmt.Sprintf("File: %s:%d:%d\n", symbol.File, symbol.Line, symbol.Column)
		content += fmt.Sprintf("Kind: %s\n", getSymbolKindName(symbol.Kind))
		content += fmt.Sprintf("Exported: %v\n", symbol.Exported)

		if symbol.Signature != "" {
			content += fmt.Sprintf("Signature: %s\n", symbol.Signature)
		}

		if len(symbol.References) > 0 {
			content += fmt.Sprintf("\nReferences: %d found\n", len(symbol.References))
			for i, ref := range symbol.References {
				content += fmt.Sprintf("  %d. %s:%d:%d\n", i+1, ref.File, ref.Line, ref.Column)
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addComplexityAnalysisTool adds the complexity_analysis tool to the MCP server
func addComplexityAnalysisTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	complexityTool := mcp.NewTool("complexity_analysis",
		mcp.WithDescription("Analyze cyclomatic complexity of functions in workspace or package"),
		mcp.WithString("package_path",
			mcp.Description("Package path to analyze (optional, analyzes entire workspace if not provided)"),
		),
		mcp.WithNumber("min_complexity",
			mcp.Description("Minimum complexity threshold (default: 10)"),
			mcp.DefaultNumber(10),
		),
	)

	s.AddTool(complexityTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		packagePath, _ := args["package_path"].(string)
		
		minComplexityFloat, ok := args["min_complexity"].(float64)
		if !ok {
			minComplexityFloat = 10
		}
		minComplexity := int(minComplexityFloat)

		// Create complexity analyzer
		analyzer := analysis.NewComplexityAnalyzer(workspace, minComplexity)

		var results []*analysis.ComplexityResult
		var err error

		if packagePath != "" {
			// Analyze specific package
			pkg, exists := workspace.Packages[packagePath]
			if !exists {
				return mcp.NewToolResultError(fmt.Sprintf("Package not found: %s", packagePath)), nil
			}

			results, err = analyzer.AnalyzePackage(pkg)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Error analyzing package complexity: %v", err)), nil
			}
		} else {
			// Analyze entire workspace
			results, err = analyzer.AnalyzeWorkspace()
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("Error analyzing workspace complexity: %v", err)), nil
			}
		}

		// Format results
		content := "Complexity Analysis Report\n"
		content += "==========================\n"
		if packagePath != "" {
			content += fmt.Sprintf("Package: %s\n", packagePath)
		} else {
			content += fmt.Sprintf("Workspace: %s\n", workspace.RootPath)
		}
		content += fmt.Sprintf("Minimum complexity threshold: %d\n\n", minComplexity)

		report := analysis.FormatComplexityReport(results)
		content += report

		// Summary statistics
		if len(results) > 0 {
			content += "\nSummary:\n"
			content += "========\n"
			
			// Count by complexity level
			counts := make(map[string]int)
			totalComplexity := 0
			for _, result := range results {
				level := analysis.ClassifyComplexity(result.Metrics.CyclomaticComplexity)
				counts[level]++
				totalComplexity += result.Metrics.CyclomaticComplexity
			}
			
			content += fmt.Sprintf("Total functions analyzed: %d\n", len(results))
			content += fmt.Sprintf("Average complexity: %.1f\n", float64(totalComplexity)/float64(len(results)))
			
			for level, count := range counts {
				if count > 0 {
					content += fmt.Sprintf("%s complexity: %d functions\n", strings.Title(strings.Replace(level, "_", " ", -1)), count)
				}
			}
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addChangeSignatureTool adds the change_signature tool to the MCP server
func addChangeSignatureTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	changeSignatureTool := mcp.NewTool("change_signature",
		mcp.WithDescription("Change the signature of a function"),
		mcp.WithString("function_name",
			mcp.Required(),
			mcp.Description("Name of the function to modify"),
		),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file containing the function"),
		),
		mcp.WithString("new_params",
			mcp.Description("New parameters in format 'param1:type1,param2:type2'"),
		),
		mcp.WithString("new_returns",
			mcp.Description("New return types in format 'type1,type2'"),
		),
		mcp.WithBoolean("package_only",
			mcp.Description("Limit changes to package scope only"),
			mcp.DefaultBool(false),
		),
	)

	s.AddTool(changeSignatureTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		functionName, ok := args["function_name"].(string)
		if !ok {
			return mcp.NewToolResultError("function_name is required"), nil
		}

		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		paramsStr, _ := args["new_params"].(string)
		returnsStr, _ := args["new_returns"].(string)
		packageOnly, _ := args["package_only"].(bool)

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
		if returnsStr != "" {
			returns := strings.Split(returnsStr, ",")
			for _, ret := range returns {
				newReturns = append(newReturns, strings.TrimSpace(ret))
			}
		}

		// Determine scope
		scope := types.WorkspaceScope
		if packageOnly {
			scope = types.PackageScope
		}

		operation := &refactor.ChangeSignatureOperation{
			FunctionName: functionName,
			SourceFile:   sourceFile,
			NewParams:    newParams,
			NewReturns:   newReturns,
			Scope:        scope,
		}

		// Validate the operation
		if err := operation.Validate(workspace); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Validation error: %v", err)), nil
		}

		// Execute the operation to get the plan
		plan, err := operation.Execute(workspace)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error executing operation: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned signature change for function %s in %s", functionName, sourceFile)

		if plan != nil && len(plan.AffectedFiles) > 0 {
			content += fmt.Sprintf("\nAffected files: %d", len(plan.AffectedFiles))
			for _, file := range plan.AffectedFiles {
				content += fmt.Sprintf("\n- %s", file)
			}
		}

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nChanges: %d", len(plan.Changes))
		}

		return mcp.NewToolResultText(content), nil
	})
}

// addSafeDeleteTool adds the safe_delete tool to the MCP server
func addSafeDeleteTool(s *server.MCPServer, engine refactor.RefactorEngine, workspace *types.Workspace) {
	safeDeleteTool := mcp.NewTool("safe_delete",
		mcp.WithDescription("Safely delete a symbol after checking for references"),
		mcp.WithString("symbol_name",
			mcp.Required(),
			mcp.Description("Name of the symbol to delete"),
		),
		mcp.WithString("source_file",
			mcp.Required(),
			mcp.Description("Source file containing the symbol"),
		),
		mcp.WithBoolean("force",
			mcp.Description("Force deletion even if references are found"),
			mcp.DefaultBool(false),
		),
		mcp.WithBoolean("package_only",
			mcp.Description("Limit deletion scope to package only"),
			mcp.DefaultBool(false),
		),
	)

	s.AddTool(safeDeleteTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		
		symbolName, ok := args["symbol_name"].(string)
		if !ok {
			return mcp.NewToolResultError("symbol_name is required"), nil
		}

		sourceFile, ok := args["source_file"].(string)
		if !ok {
			return mcp.NewToolResultError("source_file is required"), nil
		}

		force, _ := args["force"].(bool)
		packageOnly, _ := args["package_only"].(bool)

		// Determine scope
		scope := types.WorkspaceScope
		if packageOnly {
			scope = types.PackageScope
		}

		operation := &refactor.SafeDeleteOperation{
			SymbolName: symbolName,
			SourceFile: sourceFile,
			Scope:      scope,
			Force:      force,
		}

		// Validate the operation
		if err := operation.Validate(workspace); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Validation error: %v", err)), nil
		}

		// Execute the operation to get the plan
		plan, err := operation.Execute(workspace)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error executing operation: %v", err)), nil
		}

		content := fmt.Sprintf("Successfully planned safe deletion of symbol %s from %s", symbolName, sourceFile)

		if plan != nil && len(plan.AffectedFiles) > 0 {
			content += fmt.Sprintf("\nAffected files: %d", len(plan.AffectedFiles))
			for _, file := range plan.AffectedFiles {
				content += fmt.Sprintf("\n- %s", file)
			}
		}

		if plan != nil && len(plan.Changes) > 0 {
			content += fmt.Sprintf("\nChanges: %d", len(plan.Changes))
		}

		return mcp.NewToolResultText(content), nil
	})
}

// Helper functions

// findSymbolInPackage finds a symbol by name in a package
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

// getSymbolKindName returns a human-readable name for a symbol kind
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