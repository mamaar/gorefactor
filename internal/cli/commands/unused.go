package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/analysis"
)

// UnusedCommand finds unused symbols in the workspace
func UnusedCommand(args []string) {
	var showAll bool
	var packageFilter string

	// Parse arguments
	for i, arg := range args {
		switch arg {
		case "--all", "-a":
			showAll = true
		case "--package", "-p":
			if i+1 < len(args) {
				packageFilter = args[i+1]
			} else {
				fmt.Fprintf(os.Stderr, "Error: --package requires a package path\n")
				os.Exit(1)
			}
		case "--help", "-h":
			printUnusedHelp()
			return
		}
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create unused analyzer
	analyzer := analysis.NewUnusedAnalyzer(workspace)

	var unusedSymbols []*analysis.UnusedSymbol
	if showAll {
		// Find all unused symbols (including exported ones)
		unusedSymbols, err = analyzer.FindUnusedSymbols()
	} else {
		// Find only unexported unused symbols (safe to delete)
		unusedSymbols, err = analyzer.GetUnusedUnexportedSymbols()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error analyzing unused symbols: %v\n", err)
		os.Exit(1)
	}

	// Filter by package if specified
	if packageFilter != "" {
		unusedSymbols = filterByPackage(unusedSymbols, packageFilter)
	}

	// Sort by file and line number for consistent output
	sort.Slice(unusedSymbols, func(i, j int) bool {
		if unusedSymbols[i].Symbol.File == unusedSymbols[j].Symbol.File {
			return unusedSymbols[i].Symbol.Line < unusedSymbols[j].Symbol.Line
		}
		return unusedSymbols[i].Symbol.File < unusedSymbols[j].Symbol.File
	})

	// Output results
	if *cli.GlobalFlags.Json {
		outputUnusedJSON(unusedSymbols, showAll)
	} else {
		outputUnusedText(unusedSymbols, showAll, packageFilter)
	}
}

func outputUnusedText(unusedSymbols []*analysis.UnusedSymbol, showAll bool, packageFilter string) {
	if len(unusedSymbols) == 0 {
		fmt.Println("No unused symbols found.")
		return
	}

	// Print header
	symbolType := "unexported symbols (safe to delete)"
	if showAll {
		symbolType = "symbols"
	}

	header := fmt.Sprintf("Found %d unused %s", len(unusedSymbols), symbolType)
	if packageFilter != "" {
		header += fmt.Sprintf(" in package %s", packageFilter)
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("=", len(header)))
	fmt.Println()

	// Group by file
	fileGroups := make(map[string][]*analysis.UnusedSymbol)
	for _, unused := range unusedSymbols {
		file := unused.Symbol.File
		fileGroups[file] = append(fileGroups[file], unused)
	}

	// Sort files
	var files []string
	for file := range fileGroups {
		files = append(files, file)
	}
	sort.Strings(files)

	// Print grouped results
	for _, file := range files {
		fmt.Printf("File: %s\n", file)
		fmt.Println(strings.Repeat("-", len(file)+6))
		
		for _, unused := range fileGroups[file] {
			symbol := unused.Symbol
			safeIndicator := ""
			if unused.SafeToDelete {
				safeIndicator = " [SAFE TO DELETE]"
			}
			
			fmt.Printf("  %s %s (line %d:%d)%s\n", 
				symbol.Kind.String(), 
				symbol.Name, 
				symbol.Line, 
				symbol.Column,
				safeIndicator)
			
			if *cli.GlobalFlags.Verbose {
				fmt.Printf("    Package: %s\n", symbol.Package)
				fmt.Printf("    Reason: %s\n", unused.Reason)
				if symbol.Signature != "" {
					fmt.Printf("    Signature: %s\n", symbol.Signature)
				}
			}
		}
		fmt.Println()
	}

	// Print summary
	safeToDelete := 0
	for _, unused := range unusedSymbols {
		if unused.SafeToDelete {
			safeToDelete++
		}
	}

	if showAll && safeToDelete < len(unusedSymbols) {
		fmt.Printf("Summary: %d total, %d safe to delete, %d exported/require manual review\n", 
			len(unusedSymbols), safeToDelete, len(unusedSymbols)-safeToDelete)
	}
}

func outputUnusedJSON(unusedSymbols []*analysis.UnusedSymbol, showAll bool) {
	data := map[string]interface{}{
		"unused_symbols": make([]map[string]interface{}, len(unusedSymbols)),
		"total_count":    len(unusedSymbols),
		"show_all":       showAll,
	}

	safeToDelete := 0
	for i, unused := range unusedSymbols {
		if unused.SafeToDelete {
			safeToDelete++
		}

		data["unused_symbols"].([]map[string]interface{})[i] = map[string]interface{}{
			"name":           unused.Symbol.Name,
			"kind":           unused.Symbol.Kind.String(),
			"package":        unused.Symbol.Package,
			"file":           unused.Symbol.File,
			"line":           unused.Symbol.Line,
			"column":         unused.Symbol.Column,
			"exported":       unused.Symbol.Exported,
			"safe_to_delete": unused.SafeToDelete,
			"reason":         unused.Reason,
			"signature":      unused.Symbol.Signature,
		}
	}

	data["safe_to_delete_count"] = safeToDelete

	OutputJSON(data)
}

func filterByPackage(unusedSymbols []*analysis.UnusedSymbol, packageFilter string) []*analysis.UnusedSymbol {
	var filtered []*analysis.UnusedSymbol
	for _, unused := range unusedSymbols {
		if strings.Contains(unused.Symbol.Package, packageFilter) {
			filtered = append(filtered, unused)
		}
	}
	return filtered
}

func printUnusedHelp() {
	fmt.Println("Usage: gorefactor unused [options]")
	fmt.Println()
	fmt.Println("Find unused symbols in the workspace.")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -a, --all           Show all unused symbols (including exported ones)")
	fmt.Println("  -p, --package PATH  Filter results to specific package")
	fmt.Println("  -h, --help          Show this help message")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  --json             Output results in JSON format")
	fmt.Println("  --verbose          Show detailed information")
	fmt.Println("  --workspace DIR    Specify workspace directory (default: current directory)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  gorefactor unused                    # Find unexported unused symbols")
	fmt.Println("  gorefactor unused --all              # Find all unused symbols")
	fmt.Println("  gorefactor unused -p pkg/analysis    # Find unused symbols in specific package")
	fmt.Println("  gorefactor unused --json             # Output in JSON format")
	fmt.Println()
	fmt.Println("Note: By default, only unexported (private) symbols are shown as they are")
	fmt.Println("safe to delete. Use --all to see exported symbols that might be unused")
	fmt.Println("but could be used by external packages.")
}