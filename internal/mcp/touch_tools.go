package mcp

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/refactor"
)

// --- touch ---

type TouchInput struct {
	Files []string `json:"files" jsonschema:"list of .go file paths (absolute or workspace-relative) to format and organize imports"`
}

type TouchFileResult struct {
	File   string `json:"file"`
	Status string `json:"status"` // "modified", "unchanged", "skipped", "error"
	Error  string `json:"error,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type TouchOutput struct {
	Results  []TouchFileResult `json:"results"`
	Modified int               `json:"modified"`
	Total    int               `json:"total"`
}

func registerTouchTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "touch",
		Description: "Organize imports and format .go files without running the refactoring engine. Lightweight: no type checking, no plan generation — just read → organize imports → gofmt → write. Files with a generated-code header are skipped.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in TouchInput) (*mcpsdk.CallToolResult, any, error) {
		if len(in.Files) == 0 {
			return errResult(fmt.Errorf("files list is empty")), nil, nil
		}

		// Read workspace metadata under RLock, then release.
		state.RLock()
		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		modulePath := ""
		if ws.Module != nil {
			modulePath = ws.Module.Path
		}
		rootPath := ws.RootPath
		state.RUnlock()

		// Discover workspace modules for import grouping.
		workspaceModules, _ := refactor.DiscoverWorkspaceModules(rootPath)
		// Filter out the current module.
		var filtered []string
		for _, wm := range workspaceModules {
			if wm != modulePath {
				filtered = append(filtered, wm)
			}
		}

		var results []TouchFileResult
		var modifiedFiles []string

		for _, file := range in.Files {
			absPath := file
			if !filepath.IsAbs(file) {
				absPath = filepath.Join(rootPath, file)
			}

			// Skip non-.go files.
			if !strings.HasSuffix(absPath, ".go") {
				results = append(results, TouchFileResult{File: file, Status: "skipped"})
				continue
			}

			original, err := os.ReadFile(absPath)
			if err != nil {
				results = append(results, TouchFileResult{File: file, Status: "error", Error: err.Error()})
				continue
			}

			if refactor.IsGeneratedSource(absPath, original) {
				results = append(results, TouchFileResult{File: file, Status: "skipped", Reason: "generated-code header (DO NOT EDIT)"})
				continue
			}

			// Organize imports.
			content := string(original)
			if modulePath != "" {
				content = refactor.OrganizeImports(content, modulePath, filtered)
			}

			// Format.
			formatted, err := format.Source([]byte(content))
			if err != nil {
				results = append(results, TouchFileResult{File: file, Status: "error", Error: fmt.Sprintf("format: %v", err)})
				continue
			}

			// Write only if changed.
			if string(formatted) == string(original) {
				results = append(results, TouchFileResult{File: file, Status: "unchanged"})
				continue
			}

			if err := os.WriteFile(absPath, formatted, 0644); err != nil {
				results = append(results, TouchFileResult{File: file, Status: "error", Error: err.Error()})
				continue
			}

			modifiedFiles = append(modifiedFiles, absPath)
			results = append(results, TouchFileResult{File: file, Status: "modified"})
		}

		// Sync workspace state for modified files.
		if len(modifiedFiles) > 0 {
			_ = state.SyncWorkspaceChanges(modifiedFiles)
		}

		out := TouchOutput{
			Results:  results,
			Modified: len(modifiedFiles),
			Total:    len(in.Files),
		}
		return textResult(out), nil, nil
	})
}
