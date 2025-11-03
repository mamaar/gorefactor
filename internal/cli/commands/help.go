package commands

import (
	"fmt"
	"os"

	"github.com/mamaar/gorefactor/internal/cli"
)

// HelpCommand handles help requests for specific commands
func HelpCommand(args []string) {
	if len(args) > 0 {
		cmd := args[0]
		switch cmd {
		case "move":
			fmt.Println(`Move Command - Move a symbol from one package to another

Usage: gorefactor move <symbol> <from-package> <to-package>

Arguments:
  symbol        The name of the symbol to move (function, type, const, or var)
  from-package  The source package path (e.g., pkg/old)
  to-package    The destination package path (e.g., pkg/new)

The move command will:
  - Move the symbol definition to the target package
  - Update all imports in files that reference the symbol
  - Create the target package if it doesn't exist
  - Ensure no import cycles are created
  - Validate that the symbol can be safely moved

Examples:
  gorefactor move MyFunction pkg/utils pkg/helpers
  gorefactor --dry-run move UserType internal/models pkg/types`)

		case "rename":
			fmt.Println(`Rename Command - Rename a symbol

Usage: gorefactor rename <symbol> <new-name> [package]
       gorefactor rename <TypeName.MethodName> <TypeName.NewMethodName> [package]

Arguments:
  symbol     The current name of the symbol, or TypeName.MethodName for methods
  new-name   The new name for the symbol, or TypeName.NewMethodName for methods  
  package    Optional: limit renaming to this package only

The rename command will:
  - Rename the symbol definition
  - Update all references to the symbol
  - Ensure the new name is a valid Go identifier
  - Check for naming conflicts
  - Preserve type safety and interface compliance
  - For qualified method names (TypeName.MethodName), only rename the method on that specific type

Examples:
  gorefactor rename OldFunction NewFunction
  gorefactor rename User Account pkg/models
  gorefactor rename MyStruct.Write MyStruct.WriteBytes
  gorefactor --rename-implementations rename Writer.Write Writer.WriteBytes
  gorefactor --package-only rename helper utility pkg/internal`)

		case "rename-package":
			fmt.Println(`Rename Package Command - Rename a package

Usage: gorefactor rename-package <old-name> <new-name> <package-path>

Arguments:
  old-name      The current package name  
  new-name      The new package name
  package-path  The path to the package directory

The rename-package command will:
  - Update package declarations in all files within the package
  - Update import statements in other packages that reference this package
  - Ensure the new name is a valid Go identifier
  - Check for naming conflicts
  - Preserve compilation and type safety

Examples:
  gorefactor rename-package auth authentication internal/auth
  gorefactor rename-package utils utilities pkg/utils
  gorefactor rename-package models domain internal/models
  gorefactor --dry-run rename-package old new pkg/path/to/package`)

		case "extract":
			fmt.Println(`Extract Command - Extract method, function, interface, or variable

Usage: gorefactor extract <type> [arguments...]

Types:
  method     Extract a method from code lines
  function   Extract a function from code lines
  interface  Extract an interface from a struct
  variable   Extract a variable from an expression

Method extraction:
  gorefactor extract method <file> <start-line> <end-line> <method-name> <struct-name>

Function extraction:
  gorefactor extract function <file> <start-line> <end-line> <function-name>

Interface extraction:
  gorefactor extract interface <struct-name> <interface-name> <methods> <target-package>

Variable extraction:
  gorefactor extract variable <file> <start-line> <end-line> <variable-name> <expression>

Examples:
  gorefactor extract method main.go 10 15 processData MyStruct
  gorefactor extract function main.go 10 15 calculateSum
  gorefactor extract interface MyStruct Processor Process,Validate pkg/interfaces
  gorefactor extract variable main.go 20 20 result "calculateValue(x, y)"`)

		case "inline":
			fmt.Println(`Inline Command - Inline method, variable, or function calls

Usage: gorefactor inline <type> [arguments...]

Types:
  method     Inline a method call with its implementation
  variable   Inline a variable with its value
  function   Inline a function call with its implementation

Method inlining:
  gorefactor inline method <method-name> <struct-name> <target-file>

Variable inlining:
  gorefactor inline variable <variable-name> <source-file>

Function inlining:
  gorefactor inline function <function-name> <source-file>

Examples:
  gorefactor inline method processData Calculator main.go
  gorefactor inline variable tempResult main.go
  gorefactor inline function utility main.go`)

		case "analyze":
			fmt.Println(`Analyze Command - Analyze a symbol and its usage

Usage: gorefactor analyze <symbol> [package]

Arguments:
  symbol     The name of the symbol to analyze
  package    Optional: look for the symbol in this package only

The analyze command will show:
  - Symbol location and type
  - Export status
  - Number of references
  - Reference locations (with --verbose)

Examples:
  gorefactor analyze MyFunction
  gorefactor analyze Config pkg/config
  gorefactor --verbose analyze DatabaseConnection`)

		case "complexity":
			fmt.Println(`Complexity Command - Analyze cyclomatic complexity of functions

Usage: gorefactor complexity [package]

Arguments:
  package    Optional: analyze only this package (otherwise analyzes entire workspace)

Options:
  --min-complexity N    Only show functions with complexity >= N (default: 10)
  --json               Output results in JSON format
  --verbose            Show detailed metrics for each function

The complexity command will show:
  - Cyclomatic complexity (decision points + 1)
  - Cognitive complexity (human readability metric)
  - Lines of code, parameters, local variables
  - Maximum nesting depth
  - Complexity classification (low, moderate, high, very_high, extreme)

Complexity Thresholds:
  1-4:   Low complexity (easy to understand and maintain)
  5-9:   Moderate complexity (still manageable)
  10-14: High complexity (consider refactoring)
  15-19: Very high complexity (refactoring recommended)
  20+:   Extreme complexity (refactoring strongly recommended)

Examples:
  gorefactor complexity
  gorefactor complexity pkg/handlers
  gorefactor --min-complexity 5 complexity
  gorefactor --json complexity pkg/utils`)

		case "delete":
			fmt.Println(`Delete Command - Safely delete symbols

Usage: gorefactor delete <symbol-name> <file>

Arguments:
  symbol-name  The name of the symbol to delete
  file         The file containing the symbol

The delete command will:
  - Check if the symbol is referenced elsewhere
  - Warn about potential breaking changes
  - Safely remove the symbol if unused

Examples:
  gorefactor delete UnusedFunction main.go
  gorefactor --force delete OldType utils.go`)

		case "unused":
			fmt.Println(`Unused Command - Find unused symbols in the workspace

Usage: gorefactor unused [options]

Find unused symbols in the workspace that can be safely removed.

Options:
  -a, --all           Show all unused symbols (including exported ones)
  -p, --package PATH  Filter results to specific package
  -h, --help          Show this help message

Global Options:
  --json             Output results in JSON format
  --verbose          Show detailed information
  --workspace DIR    Specify workspace directory (default: current directory)

The unused command will identify:
  - Unexported symbols with no references (safe to delete)
  - Variables, functions, types, constants that are unused
  - Methods that are unused (excluding interface implementations)

Examples:
  gorefactor unused                    # Find unexported unused symbols
  gorefactor unused --all              # Find all unused symbols
  gorefactor unused -p pkg/analysis    # Find unused symbols in specific package
  gorefactor unused --json             # Output in JSON format

Note: By default, only unexported (private) symbols are shown as they are
safe to delete. Use --all to see exported symbols that might be unused
but could be used by external packages.`)

		case "batch":
			fmt.Println(`Batch Command - Execute multiple operations atomically

Usage: gorefactor batch --operation "cmd args" --operation "cmd args" [options]

Options:
  --operation "cmd args"    Add an operation to the batch
  --rollback-on-failure     Rollback all changes if any operation fails
  --dry-run                Preview what would be done

Examples:
  gorefactor batch --operation "move-package src dest" --operation "clean-aliases"
  gorefactor batch --rollback-on-failure --operation "rename old new" --operation "delete unused file.go"`)

		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
			cli.Usage()
		}
	} else {
		cli.Usage()
	}
}