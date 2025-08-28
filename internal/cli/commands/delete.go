package commands

import (
	"fmt"
	"os"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// DeleteCommand handles the delete command for safe symbol deletion
func DeleteCommand(args []string) {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Error: delete requires 2 arguments: <symbol-name> <file>\n")
		fmt.Fprintf(os.Stderr, "Usage: gorefactor delete MySymbol main.go\n")
		os.Exit(1)
	}

	symbolName := args[0]
	sourceFile := args[1]

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Determine scope
	scope := types.WorkspaceScope
	if *cli.GlobalFlags.PackageOnly {
		scope = types.PackageScope
	}

	operation := &refactor.SafeDeleteOperation{
		SymbolName: symbolName,
		SourceFile: sourceFile,
		Scope:      scope,
		Force:      *cli.GlobalFlags.Force,
	}

	ExecuteOperation(engine, workspace, operation)
}