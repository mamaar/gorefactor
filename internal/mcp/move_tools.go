package mcp

import (
	"context"
	"fmt"
	"runtime/debug"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- move_symbol ---

type MoveSymbolInput struct {
	Symbol      string `json:"symbol" jsonschema:"symbol name to move"`
	FromPackage string `json:"from_package" jsonschema:"source package path (relative to workspace root)"`
	ToPackage   string `json:"to_package" jsonschema:"target package path (relative to workspace root)"`
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- move_package ---

type MovePackageInput struct {
	SourcePackage string `json:"source_package" jsonschema:"source package path"`
	TargetPackage string `json:"target_package" jsonschema:"target package path - absolute, relative to the workspace root, or a module import path; need not exist yet"`
	DryRun        bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- move_dir ---

type MoveDirInput struct {
	SourceDir string `json:"source_dir" jsonschema:"source directory"`
	TargetDir string `json:"target_dir" jsonschema:"target directory"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- move_packages ---

type PackageMappingInput struct {
	Source string `json:"source" jsonschema:"source package path"`
	Target string `json:"target" jsonschema:"target package path - absolute, relative to the workspace root, or a module import path; need not exist yet"`
}

type MovePackagesInput struct {
	Packages  []PackageMappingInput `json:"packages" jsonschema:"list of source→target package mappings"`
	TargetDir string                `json:"target_dir,omitempty" jsonschema:"common target directory (used when packages list uses relative targets)"`
	DryRun    bool                  `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// planMoveSymbol plans in and checks it under every build configuration. The
// read lock stays held until the plan exists: a watcher batch takes the write
// lock and may drop a view being checked.
func planMoveSymbol(state *MCPServer, in MoveSymbolInput) (*types.RefactoringPlan, error) {
	state.RLock()
	defer state.RUnlock()
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	return state.GetEngine().MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
		SymbolName:  in.Symbol,
		FromPackage: types.ResolvePackagePath(ws, in.FromPackage),
		ToPackage:   types.ResolvePackagePath(ws, in.ToPackage),
	})
}

func registerMoveTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "move_symbol",
		Description: "Move a symbol (function, type, variable, constant) from one package to another. Updates all references across the workspace. " +
			"The move is planned for the default build configuration and checked under every other one (GOOS/GOARCH variants, custom build tags) that covers the workspace; " +
			"it is refused when a file outside the default configuration references or declares the symbol.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveSymbolInput) (result *mcpsdk.CallToolResult, _ any, retErr error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				state.logger.Error("panic in move_symbol handler",
					"symbol", in.Symbol,
					"from", in.FromPackage,
					"to", in.ToPackage,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
				)
				result = errResult(fmt.Errorf("internal error (panic) in move_symbol: %v", r))
			}
		}()

		plan, err := planMoveSymbol(state, in)
		if err != nil {
			return errResult(err), nil, nil
		}
		planResult, err := executePlan(state, plan, "move symbol "+in.Symbol, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(planResult), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_package",
		Description: "Move an entire package to a new location. Updates all import paths across the workspace. Acts on the default build configuration only and can miss files excluded by build constraints.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MovePackageInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		// Only the source is resolved here: ResolvePackagePath looks up packages
		// that already exist, which a move destination does not. The operation
		// resolves TargetPackage itself, as a path.
		src := types.ResolvePackagePath(ws, in.SourcePackage)
		plan, err := state.GetEngine().MovePackage(ws, types.MovePackageRequest{
			SourcePackage: src,
			TargetPackage: in.TargetPackage,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move package", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_dir",
		Description: "Move a directory (and all packages inside it) to a new location. Acts on the default build configuration only and can miss files excluded by build constraints.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveDirInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().MoveDir(ws, types.MoveDirRequest{
			SourceDir: in.SourceDir,
			TargetDir: in.TargetDir,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move directory", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "move_packages",
		Description: "Move multiple packages atomically. All import references are updated in a single operation. Acts on the default build configuration only and can miss files excluded by build constraints.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MovePackagesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		mappings := make([]types.PackageMapping, len(in.Packages))
		for i, m := range in.Packages {
			mappings[i] = types.PackageMapping{
				SourcePackage: types.ResolvePackagePath(ws, m.Source),
				// Destination left raw; see the move_package handler above.
				TargetPackage: m.Target,
			}
		}
		plan, err := state.GetEngine().MovePackages(ws, types.MovePackagesRequest{
			Packages:  mappings,
			TargetDir: in.TargetDir,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "move packages", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
