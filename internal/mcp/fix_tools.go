package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- run_go_fix ---

type RunGoFixInput struct {
	Packages string   `json:"packages,omitempty" jsonschema:"package pattern, defaults to ./..."`
	Fixers   []string `json:"fixers,omitempty" jsonschema:"optional list of fixer names to enable (e.g. any, rangeint, minmax)"`
	DiffOnly bool     `json:"diff_only,omitempty" jsonschema:"if true, run with -diff flag (preview only)"`
}

type RunGoFixOutput struct {
	PackagePattern string   `json:"package_pattern"`
	FixersApplied  []string `json:"fixers_applied,omitempty"`
	DiffOnly       bool     `json:"diff_only"`
	Output         string   `json:"output"`
	Stderr         string   `json:"stderr,omitempty"`
	ExitCode       int      `json:"exit_code"`
}

var knownFixers = map[string]bool{
	"any":          true,
	"bloop":        true,
	"buildtag":     true,
	"cgocheck":     true,
	"contextcheck": true,
	"deadcode":     true,
	"efaceany":     true,
	"errorf":       true,
	"httpmethod":   true,
	"loopvar":      true,
	"minmax":       true,
	"netip":        true,
	"osglob":       true,
	"rangeint":     true,
	"sigchanyzer":  true,
	"slicesclip":   true,
	"slicesdelete": true,
	"sortslice":    true,
	"stringscutprefix": true,
	"testingcleanup":   true,
	"unmarshal":        true,
}

func registerFixTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "run_go_fix",
		Description: "Run `go fix` on the loaded workspace. Optionally restrict to specific fixers or preview changes with diff mode.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in RunGoFixInput) (*mcpsdk.CallToolResult, any, error) {
		// Validate fixers
		for _, f := range in.Fixers {
			if !knownFixers[f] {
				return errResult(fmt.Errorf("unknown fixer %q; known fixers: %s", f, knownFixersList())), nil, nil
			}
		}

		// Get workspace root path
		state.RLock()
		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		rootPath := ws.RootPath
		state.RUnlock()

		pkgPattern := in.Packages
		if pkgPattern == "" {
			pkgPattern = "./..."
		}

		// Build command args
		args := []string{"fix"}
		if in.DiffOnly {
			args = append(args, "-diff")
		}
		for _, f := range in.Fixers {
			args = append(args, "-"+f)
		}
		args = append(args, pkgPattern)

		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = rootPath

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		exitCode := 0
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				return errResult(fmt.Errorf("exec go fix: %w", err)), nil, nil
			}
		}

		out := RunGoFixOutput{
			PackagePattern: pkgPattern,
			FixersApplied:  in.Fixers,
			DiffOnly:       in.DiffOnly,
			Output:         stdout.String(),
			Stderr:         stderr.String(),
			ExitCode:       exitCode,
		}
		return textResult(out), nil, nil
	})
}

func knownFixersList() string {
	names := make([]string, 0, len(knownFixers))
	for k := range knownFixers {
		names = append(names, k)
	}
	return strings.Join(names, ", ")
}
