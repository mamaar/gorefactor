package mcp

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- move_symbol ---

type MoveSymbolInput struct {
	Symbol      string   `json:"symbol,omitempty" jsonschema:"name of the one symbol to move; give exactly one of symbol and symbols"`
	Symbols     []string `json:"symbols,omitempty" jsonschema:"names of several symbols of from_package to move together; give exactly one of symbol and symbols"`
	FromPackage string   `json:"from_package" jsonschema:"source package path (relative to workspace root)"`
	ToPackage   string   `json:"to_package" jsonschema:"target package path (relative to workspace root)"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// request is the move the input asks for, or an error when it names no symbol
// or both symbol and symbols.
func (in MoveSymbolInput) request(ws *types.Workspace) (types.MoveSymbolRequest, error) {
	if (in.Symbol == "") == (len(in.Symbols) == 0) {
		return types.MoveSymbolRequest{}, fmt.Errorf("give exactly one of symbol and symbols")
	}
	from := types.ResolvePackagePath(ws, in.FromPackage)
	req := types.MoveSymbolRequest{
		SymbolName:  in.Symbol,
		FromPackage: from,
		ToPackage:   types.ResolvePackagePath(ws, in.ToPackage),
	}
	for _, name := range in.Symbols {
		req.Symbols = append(req.Symbols, types.QualifiedSymbol{Package: from, Name: name})
	}
	return req, nil
}

// label names the symbols the input moves.
func (in MoveSymbolInput) label() string {
	if in.Symbol != "" {
		return in.Symbol
	}
	return strings.Join(in.Symbols, ", ")
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
	req, err := in.request(ws)
	if err != nil {
		return nil, err
	}
	return state.GetEngine().MoveSymbolAcrossConfigs(req)
}

func registerMoveTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "move_symbol",
		Description: "Move a symbol (function, type, variable, constant) from one package to another. Updates all references across the workspace. " +
			"Give one name as symbol, or several as symbols to move them together. " +
			"Names declared together must be moved together through symbols: the constants of an iota group (or any group that repeats an expression implicitly) and the names of one declaration such as var a, b = f(). " +
			"Moving a variable moves its initializer's side effects to the target package's init. " +
			"A type does not take its constants along: name them in the same call, or the move is refused as an import cycle when its methods use constants left behind. " +
			"A type's methods move with it from every non-test file of its package; a method declared in a test file refuses the move, as does anything the move would have to edit in a generated file. " +
			"A method or struct field cannot be moved on its own: move its type. " +
			"The move is planned for the default build configuration and checked under every other one (GOOS/GOARCH variants, custom build tags) that covers the workspace; " +
			"it is refused when a file outside the default configuration references or declares the symbol.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in MoveSymbolInput) (result *mcpsdk.CallToolResult, _ any, retErr error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				state.logger.Error("panic in move_symbol handler",
					"symbol", in.label(),
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
		planResult, err := executePlan(state, plan, "move symbol "+in.label(), in.DryRun)
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
