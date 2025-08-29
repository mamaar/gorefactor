package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/pkg/analysis"
)

// ComplexityCommand handles cyclomatic complexity analysis
func ComplexityCommand(args []string) {
	packagePath := ""
	if len(args) > 0 {
		packagePath = args[0]
	}

	// Load workspace
	engine := cli.CreateEngineWithFlags()
	workspace, err := engine.LoadWorkspace(*cli.GlobalFlags.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading workspace: %v\n", err)
		os.Exit(1)
	}

	// Create complexity analyzer
	analyzer := analysis.NewComplexityAnalyzer(workspace, *cli.GlobalFlags.MinComplexity)

	var results []*analysis.ComplexityResult
	if packagePath != "" {
		// Analyze specific package
		resolvedPackage := ResolvePackagePath(workspace, packagePath)
		pkg, exists := workspace.Packages[resolvedPackage]
		if !exists {
			fmt.Fprintf(os.Stderr, "Error: package not found: %s\n", packagePath)
			os.Exit(1)
		}

		results, err = analyzer.AnalyzePackage(pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error analyzing package complexity: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Analyze entire workspace
		results, err = analyzer.AnalyzeWorkspace()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error analyzing workspace complexity: %v\n", err)
			os.Exit(1)
		}
	}

	// Output results
	if *cli.GlobalFlags.Json {
		OutputJSON(map[string]interface{}{
			"complexityResults": results,
			"thresholds":        analysis.GetComplexityThresholds(),
			"minComplexity":     *cli.GlobalFlags.MinComplexity,
		})
	} else {
		fmt.Printf("Complexity Analysis Report\n")
		fmt.Printf("==========================\n")
		if packagePath != "" {
			fmt.Printf("Package: %s\n", packagePath)
		} else {
			fmt.Printf("Workspace: %s\n", workspace.RootPath)
		}
		fmt.Printf("Minimum complexity threshold: %d\n\n", *cli.GlobalFlags.MinComplexity)

		report := analysis.FormatComplexityReport(results)
		fmt.Print(report)

		// Summary statistics
		if len(results) > 0 {
			fmt.Printf("\nSummary:\n")
			fmt.Printf("========\n")
			
			// Count by complexity level
			counts := make(map[string]int)
			totalComplexity := 0
			for _, result := range results {
				level := analysis.ClassifyComplexity(result.Metrics.CyclomaticComplexity)
				counts[level]++
				totalComplexity += result.Metrics.CyclomaticComplexity
			}
			
			fmt.Printf("Total functions analyzed: %d\n", len(results))
			fmt.Printf("Average complexity: %.1f\n", float64(totalComplexity)/float64(len(results)))
			
			for level, count := range counts {
				if count > 0 {
					fmt.Printf("%s complexity: %d functions\n", strings.Title(strings.Replace(level, "_", " ", -1)), count)
				}
			}
		}
	}
}