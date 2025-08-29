package commands

import (
	"fmt"
	"os"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/types"
)

// AnalyzeCommand handles symbol analysis
func AnalyzeCommand(args []string) {
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
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Find the symbol
	var symbol *types.Symbol
	if packagePath != "" {
		// Look in specific package
		if pkg, exists := workspace.Packages[packagePath]; exists {
			symbol = FindSymbolInPackage(pkg, symbolName)
		}
	} else {
		// Search all packages
		for _, pkg := range workspace.Packages {
			if s := FindSymbolInPackage(pkg, symbolName); s != nil {
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
	if *cli.GlobalFlags.Json {
		OutputJSON(map[string]interface{}{
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
		fmt.Printf("Kind: %s\n", GetSymbolKindName(symbol.Kind))
		fmt.Printf("Exported: %v\n", symbol.Exported)

		if symbol.Signature != "" {
			fmt.Printf("Signature: %s\n", symbol.Signature)
		}

		if len(symbol.References) > 0 {
			fmt.Printf("\nReferences: %d found\n", len(symbol.References))
			if *cli.GlobalFlags.Verbose {
				for i, ref := range symbol.References {
					fmt.Printf("  %d. %s:%d:%d\n", i+1, ref.File, ref.Line, ref.Column)
				}
			}
		}
	}
}