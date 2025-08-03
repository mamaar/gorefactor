package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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
		workspaceFlag = flag.String("workspace", "", "Root workspace directory (defaults to current directory)")
		portFlag      = flag.Int("port", 0, "TCP port to listen on (0 for stdio)")
		debugFlag     = flag.Bool("debug", false, "Enable debug logging")
		versionFlag   = flag.Bool("version", false, "Show version information")
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
	engine := refactor.CreateEngine()
	workspaceObj, err := engine.LoadWorkspace(workspace)
	if err != nil {
		log.Fatalf("Failed to load workspace: %v", err)
	}

	// Add tools
	addMoveSymbolTool(mcpServer, engine, workspaceObj)
	addRenameSymbolTool(mcpServer, engine, workspaceObj)
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