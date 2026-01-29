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
		case "options", "flags", "global-options":
			fmt.Println(`Global Options - Available for all commands

Usage: gorefactor [options] <command> [arguments]

Options:
  --version               Show version information and exit
  --workspace <dir>       Path to workspace root (default: current directory)
                          The workspace is the Go module root containing go.mod
  --dry-run               Preview changes without applying them
                          Shows what files would be modified and how
  --json                  Output results in JSON format for programmatic use
                          Useful for integrating with other tools or scripts
  --verbose               Enable verbose output with detailed progress info
                          Shows loaded packages, resolved paths, and more
  --force                 Force operation even with warnings
                          Use cautiously - may result in broken code
  --backup                Create backup files before making changes (default: true)
                          Backups are stored in .gorefactor-backup/
  --package-only          For rename operations, only rename within the specified package
                          Prevents workspace-wide renaming
  --create-target         Create target package if it doesn't exist (default: true)
                          Automatically creates directories and package files
  --skip-compilation      Skip compilation validation after refactoring
                          Faster but won't catch errors introduced by changes
  --allow-breaking        Allow potentially breaking refactorings that may require manual fixes
                          Enable when you know the external impact is acceptable
  --min-complexity <n>    Minimum complexity threshold for complexity analysis (default: 10)
                          Only functions with complexity >= n will be shown
  --rename-implementations  When renaming interface methods, also rename all implementations
                          Ensures interface contracts remain satisfied

Examples:
  # Preview moving a function without making changes
  gorefactor --dry-run move MyFunc pkg/old pkg/new

  # Force move even if there are warnings
  gorefactor --force move DeprecatedFunc pkg/old pkg/new

  # Use a different workspace root
  gorefactor --workspace /path/to/project move MyFunc pkg/a pkg/b

  # Get JSON output for scripting
  gorefactor --json analyze MySymbol | jq '.references'

  # Rename only within a specific package
  gorefactor --package-only rename oldName newName pkg/internal`)

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
  - Create the target package if it doesn't exist (unless --create-target=false)
  - Ensure no import cycles are created
  - Validate that the symbol can be safely moved
  - Handle associated types (e.g., methods on moved types)

Safety Checks:
  - Verifies symbol exists in source package
  - Checks for naming conflicts in target package
  - Detects potential import cycles
  - Validates compilation after move (unless --skip-compilation)

Related Options:
  --dry-run         Preview the move without applying changes
  --create-target   Create target package if needed (default: true)
  --force           Force move even with warnings
  --verbose         Show detailed information about the move

Examples:
  # Move a function between packages
  gorefactor move MyFunction pkg/utils pkg/helpers

  # Preview a move without applying changes
  gorefactor --dry-run move UserType internal/models pkg/types

  # Move a type and see detailed output
  gorefactor --verbose move Config internal/config pkg/config

  # Force move even if there are warnings
  gorefactor --force move LegacyHelper pkg/old pkg/new`)

		case "rename":
			fmt.Println(`Rename Command - Rename a symbol across the codebase

Usage: gorefactor rename <symbol> <new-name> [package]
       gorefactor rename <TypeName.MethodName> <TypeName.NewMethodName> [package]

Arguments:
  symbol     The current name of the symbol, or TypeName.MethodName for methods
  new-name   The new name for the symbol, or TypeName.NewMethodName for methods
  package    Optional: limit renaming to this package only

Symbol Types Supported:
  - Functions (top-level functions)
  - Types (structs, interfaces, type aliases)
  - Methods (using Type.Method syntax)
  - Constants
  - Variables (package-level)
  - Interface methods (with --rename-implementations)

The rename command will:
  - Rename the symbol definition
  - Update all references to the symbol across the workspace
  - Ensure the new name is a valid Go identifier
  - Check for naming conflicts in the scope
  - Preserve type safety and interface compliance
  - For qualified method names (TypeName.MethodName), only rename the method on that specific type

Related Options:
  --package-only            Only rename within the specified package
  --rename-implementations  Also rename interface method implementations
  --dry-run                 Preview changes without applying
  --verbose                 Show all files that will be modified

Examples:
  # Rename a function across entire workspace
  gorefactor rename OldFunction NewFunction

  # Rename a type only within a specific package
  gorefactor rename User Account pkg/models

  # Rename a method on a specific struct
  gorefactor rename MyStruct.Write MyStruct.WriteBytes

  # Rename an interface method and all implementations
  gorefactor --rename-implementations rename Writer.Write Writer.WriteBytes

  # Rename only within a package (don't update external references)
  gorefactor --package-only rename helper utility pkg/internal

  # Preview rename changes
  gorefactor --dry-run rename Config Settings`)

		case "rename-package":
			fmt.Println(`Rename Package Command - Rename a package and update all references

Usage: gorefactor rename-package <old-name> <new-name> <package-path>

Arguments:
  old-name      The current package name (as declared in Go files)
  new-name      The new package name to use
  package-path  The filesystem path to the package directory (relative to workspace)

The rename-package command will:
  - Update 'package X' declarations in all files within the package
  - Update import statements in ALL packages that reference this package
  - Update import aliases if they match the old package name
  - Ensure the new name is a valid Go identifier (lowercase, no hyphens)
  - Check for naming conflicts with existing packages
  - Preserve compilation and type safety

Important Notes:
  - This renames the package IDENTIFIER, not the directory
  - The directory path stays the same; only the package declaration changes
  - Use move-package if you want to relocate the package directory
  - Package names should follow Go conventions (lowercase, short)

Related Options:
  --dry-run   Preview changes without applying
  --verbose   Show all files that will be modified
  --force     Force rename even with warnings

Examples:
  # Rename 'auth' package to 'authentication'
  gorefactor rename-package auth authentication internal/auth

  # Rename 'utils' to 'utilities'
  gorefactor rename-package utils utilities pkg/utils

  # Rename 'models' to 'domain' for DDD naming
  gorefactor rename-package models domain internal/models

  # Preview rename without changes
  gorefactor --dry-run rename-package old new pkg/path/to/package

  # Rename with verbose output to see all affected files
  gorefactor --verbose rename-package handlers api internal/handlers`)

		case "extract":
			fmt.Println(`Extract Command - Extract code into methods, functions, interfaces, or variables

Usage: gorefactor extract <type> [arguments...]

Types:
  method      Extract selected lines into a new method on a struct
  function    Extract selected lines into a standalone function
  interface   Extract an interface from a struct's methods
  variable    Extract an expression into a named variable
  constant    Extract a literal value into a named constant
  block       Extract a code block into a separate scope

Subcommand Details:

  METHOD EXTRACTION
  Usage: gorefactor extract method <file> <start-line> <end-line> <method-name> <struct-name>

  Arguments:
    file          Source file containing the code to extract
    start-line    First line of code to extract (1-indexed)
    end-line      Last line of code to extract (1-indexed)
    method-name   Name for the new method
    struct-name   The struct type to attach the method to

  The extractor will:
    - Analyze variables used in the selection
    - Determine which need to be parameters
    - Determine return values based on variables used after selection
    - Generate appropriate method signature

  FUNCTION EXTRACTION
  Usage: gorefactor extract function <file> <start-line> <end-line> <function-name>

  Arguments:
    file           Source file containing the code to extract
    start-line     First line of code to extract (1-indexed)
    end-line       Last line of code to extract (1-indexed)
    function-name  Name for the new function

  INTERFACE EXTRACTION
  Usage: gorefactor extract interface <struct-name> <interface-name> <methods> <target-package>

  Arguments:
    struct-name     Source struct to extract interface from
    interface-name  Name for the new interface
    methods         Comma-separated list of method names to include
    target-package  Package where the interface will be created

  VARIABLE EXTRACTION
  Usage: gorefactor extract variable <file> <line> <line> <variable-name> <expression>

  Arguments:
    file           Source file containing the expression
    line           Line number where expression appears (use same for start/end)
    variable-name  Name for the new variable
    expression     The exact expression text to extract

Related Options:
  --dry-run   Preview the extraction without applying changes
  --verbose   Show detailed analysis of the extraction

Examples:
  # Extract lines 10-15 into a method on MyStruct
  gorefactor extract method main.go 10 15 processData MyStruct

  # Extract lines into a standalone function
  gorefactor extract function main.go 10 15 calculateSum

  # Extract interface from struct methods
  gorefactor extract interface MyStruct Processor Process,Validate pkg/interfaces

  # Extract complex expression into a variable
  gorefactor extract variable main.go 20 20 result "calculateValue(x, y)"

  # Preview method extraction
  gorefactor --dry-run extract method handler.go 25 40 handleRequest Server`)

		case "inline":
			fmt.Println(`Inline Command - Replace calls with their implementations

Usage: gorefactor inline <type> [arguments...]

Types:
  method     Inline a method call with its implementation
  variable   Inline a variable with its assigned value
  function   Inline a function call with its implementation

Subcommand Details:

  METHOD INLINING
  Usage: gorefactor inline method <method-name> <struct-name> <target-file>

  Arguments:
    method-name  Name of the method to inline
    struct-name  The struct type the method belongs to
    target-file  File where calls should be inlined

  The inliner will:
    - Find all calls to the method in the target file
    - Replace each call with the method body
    - Substitute parameters with actual arguments
    - Handle receiver access appropriately

  VARIABLE INLINING
  Usage: gorefactor inline variable <variable-name> <source-file>

  Arguments:
    variable-name  Name of the variable to inline
    source-file    File containing the variable

  The inliner will:
    - Find the variable's initialization expression
    - Replace all uses of the variable with the expression
    - Remove the variable declaration if it becomes unused

  FUNCTION INLINING
  Usage: gorefactor inline function <function-name> <source-file>

  Arguments:
    function-name  Name of the function to inline
    source-file    File where calls should be inlined

  The inliner will:
    - Find all calls to the function
    - Replace each call with the function body
    - Substitute parameters with actual arguments
    - Handle return values appropriately

Safety Considerations:
  - Inlining functions with side effects may change behavior
  - Multiple return statements require careful handling
  - Variables may need renaming to avoid conflicts

Related Options:
  --dry-run   Preview inlining without applying changes
  --verbose   Show detailed information about each inline operation

Examples:
  # Inline a method call
  gorefactor inline method processData Calculator main.go

  # Inline a temporary variable
  gorefactor inline variable tempResult main.go

  # Inline a utility function
  gorefactor inline function utility main.go

  # Preview function inlining
  gorefactor --dry-run inline function helper service.go`)

		case "analyze":
			fmt.Println(`Analyze Command - Analyze a symbol's usage and refactoring impact

Usage: gorefactor analyze <symbol> [package]

Arguments:
  symbol     The name of the symbol to analyze (function, type, method, const, var)
  package    Optional: look for the symbol in this package only

Output Information:
  - Symbol type (function, type, method, const, var)
  - File location and line number
  - Package where defined
  - Export status (exported/unexported)
  - Total number of references
  - Reference locations (files and line numbers with --verbose)
  - Dependent packages (packages that import and use this symbol)
  - Impact assessment for refactoring

Use Cases:
  - Before moving a symbol, understand its usage scope
  - Identify dead code (symbols with no references)
  - Understand coupling between packages
  - Plan refactoring by seeing all affected code

Related Options:
  --verbose   Show detailed reference locations (file:line for each reference)
  --json      Output analysis in JSON format for scripting
  --package-only  Only analyze within the specified package scope

Examples:
  # Analyze a function to see where it's used
  gorefactor analyze MyFunction

  # Analyze a type in a specific package
  gorefactor analyze Config pkg/config

  # Get detailed reference locations
  gorefactor --verbose analyze DatabaseConnection

  # Get JSON output for programmatic use
  gorefactor --json analyze Handler | jq '.references[]'

  # Analyze a method (use Type.Method syntax)
  gorefactor analyze Server.HandleRequest

  # Analyze only within a package
  gorefactor --package-only analyze helper pkg/internal`)

		case "complexity":
			fmt.Println(`Complexity Command - Analyze cyclomatic complexity of functions

Usage: gorefactor complexity [package]

Arguments:
  package    Optional: analyze only this package (otherwise analyzes entire workspace)

The complexity command will show:
  - Cyclomatic complexity (decision points + 1)
  - Cognitive complexity (human readability metric)
  - Lines of code (LOC)
  - Number of parameters
  - Number of local variables
  - Maximum nesting depth
  - Complexity classification (low, moderate, high, very_high, extreme)

Complexity Thresholds:
  1-4:   Low complexity     - Easy to understand and maintain
  5-9:   Moderate complexity - Still manageable
  10-14: High complexity    - Consider refactoring
  15-19: Very high complexity - Refactoring recommended
  20+:   Extreme complexity  - Refactoring strongly recommended

Metrics Explained:
  Cyclomatic Complexity: Counts independent paths through code
    - +1 for each if, for, case, &&, ||, ?:
    - Lower is better; aim for < 10

  Cognitive Complexity: Measures human comprehension difficulty
    - Adds weight for nesting depth
    - Penalizes control flow breaks (break, continue, goto)
    - Often more accurate than cyclomatic for readability

Related Options:
  --min-complexity N  Only show functions with complexity >= N (default: 10)
  --json              Output results in JSON format for tooling
  --verbose           Show detailed metrics for each function

Output Columns (default):
  Package | Function | Cyclomatic | Cognitive | LOC | Classification

Examples:
  # Analyze entire workspace for high complexity functions
  gorefactor complexity

  # Analyze specific package
  gorefactor complexity pkg/handlers

  # Lower threshold to see more functions
  gorefactor --min-complexity 5 complexity

  # Get JSON output for CI integration
  gorefactor --json complexity pkg/utils

  # Verbose output with all metrics
  gorefactor --verbose complexity internal/service

  # Find most complex functions across codebase
  gorefactor --min-complexity 15 complexity`)

		case "delete":
			fmt.Println(`Delete Command - Safely delete symbols from the codebase

Usage: gorefactor delete <symbol-name> <file>

Arguments:
  symbol-name  The name of the symbol to delete (function, type, const, var, method)
  file         The file containing the symbol definition

The delete command will:
  - Check if the symbol is referenced elsewhere in the workspace
  - Warn about potential breaking changes (external references)
  - Safely remove the symbol definition if unused
  - Remove associated code (e.g., methods when deleting a type)
  - Clean up imports that become unused after deletion

Safety Behavior:
  - By default, refuses to delete symbols with references
  - Use --force to delete even with references (will break code)
  - Exported symbols require --force (may have external users)

What Gets Deleted:
  - Function: The entire function definition
  - Type: The type definition and all methods on that type
  - Constant: The constant declaration
  - Variable: The variable declaration
  - Method: Just the method (not the receiver type)

Related Options:
  --force     Delete even if references exist (will break code!)
  --dry-run   Preview what would be deleted without making changes
  --verbose   Show all references before deletion

Examples:
  # Delete an unused function
  gorefactor delete UnusedFunction main.go

  # Force delete even with references (use cautiously!)
  gorefactor --force delete OldType utils.go

  # Preview deletion
  gorefactor --dry-run delete DeprecatedHelper helper.go

  # Delete with verbose output to see references
  gorefactor --verbose delete LegacyConfig config.go

See Also:
  gorefactor unused   - Find unused symbols that can be safely deleted`)

		case "unused":
			fmt.Println(`Unused Command - Find unused symbols that can be safely deleted

Usage: gorefactor unused [options]

Scans the workspace to find symbols that are defined but never referenced.

Command Options:
  -a, --all           Show all unused symbols (including exported ones)
  -p, --package PATH  Filter results to a specific package path

The unused command will identify:
  - Unexported (private) symbols with no references - safe to delete
  - Functions, types, constants, variables that are never used
  - Methods that are unused (excluding interface implementations)
  - Struct fields that are never accessed (with --verbose)

Output Columns:
  Package | Symbol | Type | File:Line | Status

Symbol Categories:
  - SAFE:    Unexported and unused - can be deleted without impact
  - CAUTION: Exported and unused - might have external users
  - SKIP:    Interface implementation methods (kept for compliance)

Related Options:
  --json        Output results in JSON format for tooling integration
  --verbose     Show additional details (struct fields, method receivers)
  --workspace   Specify workspace directory (default: current directory)

Best Practices:
  1. Start with 'gorefactor unused' to find safe deletions
  2. Review each symbol before deletion
  3. Use 'gorefactor analyze <symbol>' for detailed reference check
  4. Use 'gorefactor delete <symbol> <file>' to remove

Examples:
  # Find all safely deletable unexported symbols
  gorefactor unused

  # Include exported symbols (may have external users)
  gorefactor unused --all

  # Find unused symbols in a specific package
  gorefactor unused -p pkg/analysis
  gorefactor unused --package internal/handlers

  # Get JSON output for CI/tooling
  gorefactor --json unused

  # Verbose output with file locations
  gorefactor --verbose unused

  # Combine filters
  gorefactor unused --all -p pkg/legacy --json

Note: By default, only unexported (private) symbols are shown as they are
guaranteed safe to delete. Use --all to see exported symbols, but verify
they're not used by external packages before deleting.

See Also:
  gorefactor delete   - Delete symbols from the codebase
  gorefactor analyze  - Analyze symbol usage in detail`)

		case "batch":
			fmt.Println(`Batch Command - Execute multiple refactoring operations atomically

Usage: gorefactor batch --operation "cmd args" [--operation "cmd args"...] [options]

Command Options:
  --operation "cmd args"    Add a refactoring operation to the batch
                            Can be specified multiple times
  --rollback-on-failure     Rollback ALL changes if ANY operation fails
  --dry-run                 Preview what would be done without making changes

How It Works:
  1. All operations are validated before execution
  2. Operations execute in order (first to last)
  3. Each operation's changes are tracked for rollback
  4. If --rollback-on-failure is set and any operation fails:
     - All previously applied changes are reverted
     - Workspace returns to original state
  5. Without --rollback-on-failure, partial changes may remain

Supported Operations in Batch:
  - move <symbol> <from> <to>
  - move-package <source> <target>
  - move-dir <source> <target>
  - rename <old> <new>
  - rename-package <old> <new> <path>
  - delete <symbol> <file>
  - clean-aliases
  - Any other gorefactor command

Operation String Format:
  The --operation value is the command and arguments as you would type them,
  WITHOUT the 'gorefactor' prefix. Quote the entire operation string.

Use Cases:
  - Coordinated multi-package refactoring
  - Atomic reorganization (all or nothing)
  - Complex transformations that require multiple steps
  - Safe refactoring with guaranteed rollback

Examples:
  # Move package and clean up aliases atomically
  gorefactor batch --operation "move-package src dest" --operation "clean-aliases"

  # Multiple renames with rollback protection
  gorefactor batch --rollback-on-failure \
    --operation "rename OldType NewType" \
    --operation "rename oldFunc newFunc"

  # Preview a complex batch operation
  gorefactor batch --dry-run \
    --operation "move-package internal/shared pkg/" \
    --operation "rename-package shared infrastructure pkg/shared"

  # Refactoring with deletion (dangerous - use rollback!)
  gorefactor batch --rollback-on-failure \
    --operation "rename old new" \
    --operation "delete unused file.go"

See Also:
  gorefactor plan     - Create a plan file for complex refactoring
  gorefactor execute  - Execute a saved plan file
  gorefactor rollback - Manually rollback previous operations`)

		case "move-package":
			fmt.Println(`Move Package Command - Move an entire package to a new location

Usage: gorefactor move-package <source-package> <target-package>

Arguments:
  source-package  The current package path (e.g., internal/shared/command)
  target-package  The new package path (e.g., pkg/command)

The move-package command will:
  - Move all files from source to target directory
  - Update package declarations in moved files
  - Update ALL import statements across the workspace
  - Create target directory if it doesn't exist
  - Preserve file structure within the package

Differences from move command:
  - move: Moves a single symbol between packages
  - move-package: Moves the entire package (all symbols, all files)

What Gets Moved:
  - All .go files in the package
  - Package documentation (doc.go)
  - Test files (*_test.go)
  - The directory structure is recreated at target

Related Options:
  --dry-run        Preview the move without making changes
  --create-target  Create target directory if needed (default: true)
  --verbose        Show detailed progress information

Examples:
  # Move a package to a new location
  gorefactor move-package internal/shared/command pkg/command

  # Preview the move
  gorefactor --dry-run move-package internal/old pkg/new

  # Move with verbose output
  gorefactor --verbose move-package internal/utils pkg/utils

See Also:
  gorefactor move          - Move individual symbols
  gorefactor move-dir      - Move directory structure
  gorefactor move-packages - Move multiple packages atomically`)

		case "move-dir":
			fmt.Println(`Move Dir Command - Move a directory structure preserving package relationships

Usage: gorefactor move-dir <source-dir> <target-dir>

Arguments:
  source-dir  The source directory path (e.g., internal/shared)
  target-dir  The target directory path (e.g., pkg/infrastructure)

The move-dir command will:
  - Move entire directory tree from source to target
  - Preserve subdirectory structure and relationships
  - Update ALL package declarations in moved files
  - Update ALL import statements across the workspace
  - Handle multiple packages within the directory tree
  - Maintain internal references between moved packages

Differences from move-package:
  - move-package: Moves a single package (one directory level)
  - move-dir: Moves entire directory tree (multiple packages/levels)

Example Directory Structure:
  Before: internal/shared/
            ├── command/
            ├── events/
            └── utils/

  After:  pkg/infrastructure/
            ├── command/
            ├── events/
            └── utils/

Related Options:
  --dry-run   Preview the move without making changes
  --verbose   Show detailed progress for each package

Examples:
  # Move directory structure
  gorefactor move-dir internal/shared pkg/infrastructure

  # Preview the move
  gorefactor --dry-run move-dir old/path new/path

  # Move with verbose output
  gorefactor --verbose move-dir internal/services pkg/services

See Also:
  gorefactor move-package  - Move single package
  gorefactor move-packages - Move specific packages atomically`)

		case "move-packages":
			fmt.Println(`Move Packages Command - Move multiple specified packages atomically

Usage: gorefactor move-packages <package1,package2,...> <target-dir>

Arguments:
  packages    Comma-separated list of package paths to move
  target-dir  Directory where packages will be moved to

The move-packages command will:
  - Move all specified packages to the target directory
  - Perform all moves as a single atomic operation
  - Update ALL import statements across the workspace
  - Preserve package names (only location changes)
  - Handle cross-references between moved packages correctly

Atomicity:
  - All packages move together or none move
  - If any package move fails, all are rolled back
  - Ensures workspace consistency

Package Naming in Target:
  Source: internal/shared/command → Target: pkg/command
  Source: internal/shared/events  → Target: pkg/events
  (Package name derived from last path component)

Related Options:
  --dry-run        Preview moves without making changes
  --create-target  Create target directories if needed (default: true)
  --verbose        Show detailed progress for each package

Examples:
  # Move multiple related packages
  gorefactor move-packages internal/shared/command,internal/shared/events pkg/infrastructure/

  # Preview the moves
  gorefactor --dry-run move-packages pkg/old1,pkg/old2 pkg/new/

  # Move with verbose output
  gorefactor --verbose move-packages internal/a,internal/b,internal/c pkg/

  # Note: target-dir should end with / to indicate directory
  gorefactor move-packages internal/models,internal/repos pkg/domain/

See Also:
  gorefactor move-package - Move single package
  gorefactor move-dir     - Move entire directory structure
  gorefactor batch        - Execute multiple operations atomically`)

		case "create-facade":
			fmt.Println(`Create Facade Command - Create a facade package with re-exports

Usage: gorefactor create-facade <facade-package> --from <package.Symbol>...

Arguments:
  facade-package  Path for the new facade package (e.g., pkg/commission)
  --from          Specify exports in 'package.Symbol' format (repeatable)

The create-facade command will:
  - Create a new package at the specified location
  - Generate re-export files for each specified symbol
  - Create type aliases for types
  - Create wrapper functions for functions
  - Maintain proper import relationships

Facade Pattern:
  A facade provides a simplified interface to a complex subsystem.
  Instead of importing multiple internal packages, consumers can
  import a single facade package that re-exports what they need.

Export Specification Format:
  --from modules/commission/models.Commission
         ^^^^^^^^^^^^^^^^^^^^^^^  ^^^^^^^^^^
         package path             symbol name

What Gets Generated:
  - Type aliases: type Commission = models.Commission
  - Function wrappers: func NewCommission(...) = models.NewCommission(...)
  - Constant re-exports: const MaxRetries = config.MaxRetries

Related Options:
  --dry-run   Preview generated code without creating files
  --verbose   Show detailed generation information

Examples:
  # Create facade with single export
  gorefactor create-facade pkg/commission \
    --from modules/commission/models.Commission

  # Create facade with multiple exports
  gorefactor create-facade pkg/commission \
    --from modules/commission/models.Commission \
    --from modules/commission/commands.CreateCommand \
    --from modules/commission/events.CommissionCreated

  # Preview facade generation
  gorefactor --dry-run create-facade pkg/api \
    --from internal/handlers.Handler

See Also:
  gorefactor generate-facades - Auto-generate facades for all modules
  gorefactor update-facades   - Update existing facades`)

		case "generate-facades":
			fmt.Println(`Generate Facades Command - Auto-generate facade packages for modules

Usage: gorefactor generate-facades <modules-dir> <target-dir>

Arguments:
  modules-dir  Directory containing module packages to create facades for
  target-dir   Directory where facade packages will be created

The generate-facades command will:
  - Scan all packages in modules-dir
  - Identify exported symbols suitable for facades
  - Generate facade packages in target-dir
  - Create proper re-exports for types, functions, constants
  - Follow naming conventions (module/models → pkg/models)

Default Export Types:
  The command looks for packages named:
  - commands - Command handlers and DTOs
  - models   - Domain models and entities
  - events   - Event types and handlers

Generated Structure:
  modules/commission/
    ├── commands/
    ├── models/
    └── events/

  Generates:
  pkg/commission/
    ├── commands.go  (re-exports from modules/commission/commands)
    ├── models.go    (re-exports from modules/commission/models)
    └── events.go    (re-exports from modules/commission/events)

Related Options:
  --dry-run   Preview what would be generated
  --verbose   Show detailed progress for each module

Examples:
  # Generate facades for all modules
  gorefactor generate-facades modules/ pkg/

  # Preview facade generation
  gorefactor --dry-run generate-facades modules/ pkg/

  # Generate with verbose output
  gorefactor --verbose generate-facades internal/modules/ api/

See Also:
  gorefactor create-facade  - Create single facade with specific exports
  gorefactor update-facades - Update existing facades after changes`)

		case "update-facades":
			fmt.Println(`Update Facades Command - Update existing facade packages

Usage: gorefactor update-facades <facade-dir>

Arguments:
  facade-dir  Directory containing facade packages to update

The update-facades command will:
  - Scan existing facade packages for their source mappings
  - Detect changes in underlying source packages
  - Add re-exports for new symbols
  - Remove re-exports for deleted symbols
  - Update type signatures if they changed
  - Preserve custom code added to facades

When to Use:
  - After adding new types/functions to source packages
  - After removing symbols from source packages
  - After renaming symbols in source packages
  - As part of CI to ensure facades are current

Auto-Detection:
  The command analyzes existing facade code to determine:
  - Which source packages are re-exported
  - Which symbols are currently exposed
  - What the intended facade structure is

Related Options:
  --dry-run   Preview updates without making changes
  --verbose   Show detailed information about changes

Examples:
  # Update all facades in pkg/
  gorefactor update-facades pkg/

  # Preview updates
  gorefactor --dry-run update-facades pkg/facades/

  # Update with verbose output
  gorefactor --verbose update-facades api/

See Also:
  gorefactor create-facade    - Create new facade package
  gorefactor generate-facades - Generate facades for all modules`)

		case "clean-aliases":
			fmt.Println(`Clean Aliases Command - Remove unnecessary import aliases

Usage: gorefactor clean-aliases [workspace]

Arguments:
  workspace   Optional: workspace directory (default: current directory)

The clean-aliases command will:
  - Scan all Go files for import statements
  - Remove aliases that match the package name (redundant)
  - Remove aliases where no conflict exists
  - Preserve aliases needed to avoid naming conflicts
  - Update all uses of the alias to use package name

What Gets Cleaned:
  BEFORE:
    import myutils "github.com/project/utils"
    import utils "github.com/other/utilities"  // conflict, preserved

    myutils.Helper()

  AFTER:
    import "github.com/project/utils"
    import utils "github.com/other/utilities"  // conflict, preserved

    utils.Helper()

Preservation Rules:
  - Aliases are kept when two packages have the same name
  - Aliases are kept when package name conflicts with local variable
  - Custom meaningful aliases may be removed (use standardize-imports to enforce)

Related Options:
  --dry-run   Preview changes without applying
  --verbose   Show each alias that would be removed

Examples:
  # Clean aliases in current workspace
  gorefactor clean-aliases

  # Clean aliases in specific directory
  gorefactor clean-aliases /path/to/project

  # Preview what would be cleaned
  gorefactor --dry-run clean-aliases

See Also:
  gorefactor standardize-imports     - Enforce consistent alias conventions
  gorefactor resolve-alias-conflicts - Fix conflicting aliases
  gorefactor convert-aliases         - Convert between alias styles`)

		case "standardize-imports":
			fmt.Println(`Standardize Imports Command - Enforce consistent import aliases

Usage: gorefactor standardize-imports --alias <alias=package>...

Arguments:
  --alias  Define an alias rule in 'alias=package' format (repeatable)

The standardize-imports command will:
  - Apply specified alias rules across the entire workspace
  - Update all imports matching the package patterns
  - Update all references to use the standardized alias
  - Ensure consistency across all files

Rule Format:
  --alias events=myproject/pkg/events
          ^^^^^^ ^^^^^^^^^^^^^^^^^^^^^
          alias  package path pattern

The package pattern is matched against import paths.
All imports matching the pattern will use the specified alias.

Use Cases:
  - Enforce team naming conventions
  - Clarify purpose of similarly-named packages
  - Maintain consistency after merging codebases
  - Apply organizational import standards

Related Options:
  --dry-run   Preview changes without applying
  --verbose   Show each import that would be changed

Examples:
  # Standardize a single package alias
  gorefactor standardize-imports --alias events=myproject/pkg/events

  # Standardize multiple aliases
  gorefactor standardize-imports \
    --alias events=myproject/pkg/events \
    --alias cmd=myproject/pkg/command \
    --alias models=myproject/internal/models

  # Preview changes
  gorefactor --dry-run standardize-imports --alias utils=pkg/utilities

See Also:
  gorefactor clean-aliases           - Remove unnecessary aliases
  gorefactor resolve-alias-conflicts - Fix conflicting aliases
  gorefactor convert-aliases         - Convert between alias styles`)

		case "resolve-alias-conflicts":
			fmt.Println(`Resolve Alias Conflicts Command - Find and fix import alias conflicts

Usage: gorefactor resolve-alias-conflicts [workspace]

Arguments:
  workspace   Optional: workspace directory (default: current directory)

The resolve-alias-conflicts command will:
  - Scan all files for import statements
  - Detect files with conflicting aliases (same alias, different packages)
  - Detect shadowing (alias matches local variable name)
  - Automatically resolve conflicts using full package names
  - Update all references to use resolved names

Types of Conflicts Detected:
  1. Same alias, different packages in one file:
     import utils "pkg/a"
     import utils "pkg/b"  // CONFLICT

  2. Alias shadows local variable:
     import utils "pkg/utils"
     func foo() {
       utils := "local"  // SHADOWS import
       utils.Helper()    // ERROR
     }

Resolution Strategy:
  By default, conflicts are resolved by using full package names:
  - Short conflicting aliases are expanded
  - Unique suffixes may be added (_a, _b)

Related Options:
  --dry-run   Preview conflict resolution without applying
  --verbose   Show each conflict and its resolution

Examples:
  # Resolve conflicts in current workspace
  gorefactor resolve-alias-conflicts

  # Resolve in specific directory
  gorefactor resolve-alias-conflicts /path/to/project

  # Preview resolutions
  gorefactor --dry-run resolve-alias-conflicts

See Also:
  gorefactor clean-aliases       - Remove unnecessary aliases
  gorefactor standardize-imports - Enforce consistent aliases
  gorefactor convert-aliases     - Convert between alias styles`)

		case "convert-aliases":
			fmt.Println(`Convert Aliases Command - Convert between aliased and non-aliased imports

Usage: gorefactor convert-aliases [--to-full-names | --from-full-names] [workspace]

Arguments:
  --to-full-names     Remove all aliases, use full package names (default)
  --from-full-names   Add aliases based on package name
  workspace           Optional: workspace directory (default: current)

The convert-aliases command will:
  - Scan all import statements in the workspace
  - Convert between aliased and non-aliased forms
  - Update all references to match the new import style
  - Preserve aliases where conflicts would occur

With --to-full-names (default):
  BEFORE:
    import u "github.com/project/utils"
    u.Helper()

  AFTER:
    import "github.com/project/utils"
    utils.Helper()

With --from-full-names:
  BEFORE:
    import "github.com/project/utilities"
    utilities.Helper()

  AFTER:
    import utilities "github.com/project/utilities"
    utilities.Helper()

Use Cases:
  - Prepare codebase for consistent style enforcement
  - Remove terse aliases for better readability
  - Add explicit aliases for clarity

Related Options:
  --dry-run   Preview changes without applying
  --verbose   Show each conversion

Examples:
  # Convert to full package names (remove aliases)
  gorefactor convert-aliases --to-full-names

  # Convert to aliased imports
  gorefactor convert-aliases --from-full-names

  # Preview conversion
  gorefactor --dry-run convert-aliases --to-full-names

  # Convert in specific workspace
  gorefactor convert-aliases --to-full-names /path/to/project

See Also:
  gorefactor clean-aliases           - Remove redundant aliases
  gorefactor standardize-imports     - Enforce specific alias rules
  gorefactor resolve-alias-conflicts - Fix conflicting aliases`)

		case "move-by-dependencies":
			fmt.Println(`Move By Dependencies Command - Reorganize code based on dependency analysis

Usage: gorefactor move-by-dependencies [options]

Options:
  --move-shared-to <dir>   Move shared/common code to this directory
  --keep-internal <dirs>   Comma-separated dirs to keep as internal
  --analyze-only           Only analyze, don't make changes
  --workspace <dir>        Workspace directory (default: current)

The move-by-dependencies command will:
  - Analyze the dependency graph of your codebase
  - Identify shared code used by multiple packages
  - Identify code that should be internal
  - Suggest or perform moves based on dependency flow
  - Ensure no circular dependencies are created

Dependency Analysis:
  - Symbols used by 2+ packages → candidates for shared
  - Symbols used by 1 package → candidates for internal
  - Symbols forming cycles → candidates for extraction

Move Decisions:
  The command uses the following heuristics:
  1. Heavily-depended-upon code moves to --move-shared-to
  2. Code only used within a module stays internal
  3. Cycle-forming code may need interface extraction

Related Options:
  --dry-run   Preview suggested moves without applying
  --verbose   Show detailed dependency analysis
  --json      Output analysis in JSON format

Examples:
  # Analyze dependencies only (no changes)
  gorefactor move-by-dependencies --analyze-only

  # Move shared code to pkg/
  gorefactor move-by-dependencies --move-shared-to pkg/

  # Move shared but keep certain dirs internal
  gorefactor move-by-dependencies \
    --move-shared-to pkg/ \
    --keep-internal internal/app,internal/config

  # Preview moves
  gorefactor --dry-run move-by-dependencies --move-shared-to shared/

See Also:
  gorefactor organize-by-layers   - Organize by architectural layers
  gorefactor analyze-dependencies - Detailed dependency analysis
  gorefactor fix-cycles           - Fix circular dependencies`)

		case "organize-by-layers":
			fmt.Println(`Organize By Layers Command - Restructure code by architectural layers

Usage: gorefactor organize-by-layers --domain <dir> --infrastructure <dir> --application <dir>

Options:
  --domain <dir>          Directory for domain/business logic layer
  --infrastructure <dir>  Directory for infrastructure/external layer
  --application <dir>     Directory for application/use-case layer
  --reorder-imports       Also reorder imports by layer
  --workspace <dir>       Workspace directory (default: current)

The organize-by-layers command will:
  - Analyze code to determine appropriate layer for each package
  - Move packages to their designated layer directories
  - Enforce dependency direction (domain ← application ← infrastructure)
  - Update all imports across the workspace

Layer Architecture (Clean/Hexagonal):
  Domain Layer (innermost):
    - Business entities and rules
    - No external dependencies
    - Pure Go, no frameworks

  Application Layer (middle):
    - Use cases and application logic
    - Depends only on domain
    - Orchestrates business operations

  Infrastructure Layer (outermost):
    - External interfaces (DB, HTTP, messaging)
    - Depends on domain and application
    - Framework and library integrations

Dependency Direction:
  Domain ← Application ← Infrastructure
  (arrows show allowed dependency direction)

Related Options:
  --dry-run   Preview reorganization without applying
  --verbose   Show layer classification for each package

Examples:
  # Organize codebase by layers
  gorefactor organize-by-layers \
    --domain modules/ \
    --infrastructure pkg/ \
    --application internal/

  # Also reorder imports to match layering
  gorefactor organize-by-layers \
    --domain domain/ \
    --infrastructure infra/ \
    --application app/ \
    --reorder-imports

  # Preview organization
  gorefactor --dry-run organize-by-layers \
    --domain core/ \
    --infrastructure adapters/ \
    --application services/

See Also:
  gorefactor move-by-dependencies - Move based on dependency analysis
  gorefactor analyze-dependencies - Analyze current dependency structure`)

		case "fix-cycles":
			fmt.Println(`Fix Cycles Command - Detect and fix circular dependencies

Usage: gorefactor fix-cycles [--auto-fix] [workspace]

Arguments:
  --auto-fix        Automatically apply fixes (otherwise just reports)
  --output-report   Write detailed report to file
  workspace         Optional: workspace directory (default: current)

The fix-cycles command will:
  - Build complete dependency graph of the workspace
  - Detect all circular dependency chains
  - Report cycles with full package paths
  - Suggest fixes for each cycle
  - Optionally apply fixes automatically

Cycle Detection:
  A → B → C → A (cycle detected)

  The command finds all such chains and reports:
  - Packages involved
  - Specific symbols causing the dependency
  - Suggested resolution strategy

Fix Strategies:
  1. Interface Extraction:
     - Create interface in shared package
     - Have both packages depend on interface
     - Break direct dependency

  2. Dependency Inversion:
     - Move shared code to common package
     - Both packages import common
     - No direct dependency between them

  3. Merge Packages:
     - If packages are tightly coupled
     - Combine into single package
     - Eliminates cycle trivially

Related Options:
  --dry-run   Show fixes without applying (same as no --auto-fix)
  --verbose   Show detailed cycle analysis
  --json      Output in JSON format

Examples:
  # Detect cycles only (report mode)
  gorefactor fix-cycles

  # Detect and automatically fix cycles
  gorefactor fix-cycles --auto-fix

  # Generate detailed report
  gorefactor fix-cycles --output-report cycles.json

  # Check specific workspace
  gorefactor fix-cycles /path/to/project

See Also:
  gorefactor analyze-dependencies - Full dependency analysis
  gorefactor move-by-dependencies - Reorganize based on dependencies`)

		case "analyze-dependencies":
			fmt.Println(`Analyze Dependencies Command - Comprehensive dependency flow analysis

Usage: gorefactor analyze-dependencies [options] [workspace]

Options:
  --detect-backwards-deps  Find dependencies that violate layer rules
  --suggest-moves          Suggest moves to improve architecture
  --output <file>          Write analysis to file (JSON format)
  workspace                Optional: workspace directory (default: current)

The analyze-dependencies command will:
  - Build complete dependency graph of the workspace
  - Calculate metrics for each package
  - Identify architectural issues
  - Suggest improvements

Analysis Output:
  1. Package Metrics:
     - Afferent coupling (Ca): packages that depend on this one
     - Efferent coupling (Ce): packages this one depends on
     - Instability (I): Ce / (Ca + Ce)
     - Abstractness (A): ratio of interfaces to types

  2. Dependency Flow:
     - Direct dependencies for each package
     - Transitive dependencies (full closure)
     - Reverse dependencies (dependents)

  3. Issues Detected:
     - Circular dependencies
     - Backwards dependencies (layer violations)
     - High coupling packages
     - Orphan packages (no dependents)

Backwards Dependencies:
  With --detect-backwards-deps, identifies dependencies that go
  "the wrong way" in your architecture. Requires layer definitions
  (see organize-by-layers) or uses heuristics based on paths.

Related Options:
  --dry-run   No effect (analysis is read-only)
  --verbose   Show full dependency details
  --json      Format output as JSON

Examples:
  # Basic dependency analysis
  gorefactor analyze-dependencies

  # Detect layer violations
  gorefactor analyze-dependencies --detect-backwards-deps

  # Get move suggestions
  gorefactor analyze-dependencies --suggest-moves

  # Full analysis to file
  gorefactor analyze-dependencies \
    --detect-backwards-deps \
    --suggest-moves \
    --output analysis.json

  # JSON output for tooling
  gorefactor --json analyze-dependencies

See Also:
  gorefactor fix-cycles           - Detect and fix circular dependencies
  gorefactor organize-by-layers   - Restructure by architectural layers
  gorefactor move-by-dependencies - Move based on dependency analysis`)

		case "plan":
			fmt.Println(`Plan Command - Create a structured refactoring plan file

Usage: gorefactor plan [operations...] --output <file> [--dry-run]

Options:
  --move-shared <from> <to>     Add operation to move shared code
  --create-facades <mods> <tgt> Add operation to create facades
  --output <file>               Output plan file (default: refactor-plan.json)
  --dry-run                     Validate plan without saving

The plan command will:
  - Parse the specified operations
  - Validate that all operations are feasible
  - Check for conflicts between operations
  - Generate a detailed execution plan
  - Save the plan to a JSON file

Plan File Format:
  {
    "version": "1.0",
    "created": "2024-01-15T10:30:00Z",
    "workspace": "/path/to/project",
    "steps": [
      {
        "type": "move-shared",
        "args": {"from": "internal/shared", "to": "pkg/"},
        "order": 1
      },
      ...
    ]
  }

Use Cases:
  - Review refactoring before execution
  - Share plan with team for approval
  - Execute plan in CI/CD pipeline
  - Document architectural changes
  - Retry failed refactoring

Supported Operations:
  --move-shared <from> <to>      Move shared code to new location
  --create-facades <mods> <tgt>  Generate facade packages

Related Options:
  --dry-run   Validate plan without creating file
  --verbose   Show detailed plan analysis

Examples:
  # Create a simple plan
  gorefactor plan --move-shared internal/shared pkg/ --output plan.json

  # Create plan with multiple operations
  gorefactor plan \
    --move-shared internal/shared pkg/ \
    --create-facades modules/ pkg/ \
    --output refactor-plan.json

  # Validate plan without saving
  gorefactor plan --move-shared src dst --output plan.json --dry-run

See Also:
  gorefactor execute  - Execute a plan file
  gorefactor rollback - Rollback executed operations
  gorefactor batch    - Execute operations directly (no plan file)`)

		case "execute":
			fmt.Println(`Execute Command - Execute a refactoring plan from file

Usage: gorefactor execute <plan-file>

Arguments:
  plan-file   Path to the JSON plan file created by 'gorefactor plan'

The execute command will:
  - Load and validate the plan file
  - Verify workspace state matches plan expectations
  - Execute each step in order
  - Track progress for potential rollback
  - Report success or failure for each step

Execution Behavior:
  - Steps execute sequentially in defined order
  - Each step validates before execution
  - Failed steps stop execution (remaining steps skipped)
  - Partial execution can be rolled back

Pre-Execution Checks:
  - Plan file is valid JSON
  - Workspace path exists
  - Source files/packages exist
  - No conflicting changes since plan creation

Progress Tracking:
  The command creates a .gorefactor-state file that tracks:
  - Which steps have completed
  - Backup information for rollback
  - Execution timestamps

Related Options:
  --dry-run   Preview execution without making changes
  --verbose   Show detailed execution progress
  --force     Execute even if workspace has changed

Examples:
  # Execute a plan
  gorefactor execute refactor-plan.json

  # Preview execution
  gorefactor --dry-run execute refactor-plan.json

  # Execute with verbose output
  gorefactor --verbose execute plan.json

  # Force execution even with changes
  gorefactor --force execute plan.json

See Also:
  gorefactor plan     - Create a plan file
  gorefactor rollback - Rollback executed operations
  gorefactor batch    - Execute operations without plan file`)

		case "rollback":
			fmt.Println(`Rollback Command - Revert previous refactoring operations

Usage: gorefactor rollback [--last-batch | --to-step <n>]

Options:
  --last-batch   Rollback the most recent batch operation
  --to-step <n>  Rollback to a specific step number

The rollback command will:
  - Read the operation history from .gorefactor-state
  - Restore files from backups
  - Revert import changes
  - Return workspace to previous state

Rollback Modes:
  --last-batch:
    Reverts all changes from the most recent batch or execute command.
    Useful when a refactoring didn't work as expected.

  --to-step N:
    Reverts to the state after step N completed.
    Steps are numbered starting from 1.
    Use this for partial rollback.

Requirements:
  - Backups must exist (--backup=true during original operation)
  - .gorefactor-state file must be present
  - Workspace should not have unrelated changes

State File:
  The .gorefactor-state file contains:
  - Operation history with timestamps
  - Backup file locations
  - Step completion status

Related Options:
  --dry-run   Preview rollback without making changes
  --verbose   Show detailed rollback progress
  --force     Rollback even if workspace has changed

Examples:
  # Rollback the last operation
  gorefactor rollback --last-batch

  # Rollback to step 5 (undo steps 6+)
  gorefactor rollback --to-step 5

  # Preview rollback
  gorefactor --dry-run rollback --last-batch

  # Verbose rollback
  gorefactor --verbose rollback --to-step 3

Warnings:
  - Rollback cannot be undone (no rollback of rollback)
  - Ensure no manual changes were made since original operation
  - Consider using --dry-run first to preview

See Also:
  gorefactor batch   - Execute operations with rollback support
  gorefactor execute - Execute plan with rollback support
  gorefactor plan    - Create refactoring plan`)

		case "change", "change-signature":
			fmt.Println(`Change Command - Modify function or method signatures

Usage: gorefactor change signature <function-name> <file> <new-params> [new-returns]

Subcommands:
  signature   Change function/method signature

CHANGE SIGNATURE
Usage: gorefactor change signature <function-name> <file> <new-params> [new-returns]

Arguments:
  function-name  Name of the function or method to change
  file           File containing the function definition
  new-params     New parameter list in 'name:type,name:type' format
  new-returns    Optional: new return types in 'type,type' format

The change signature command will:
  - Parse the new signature specification
  - Update the function definition
  - Update ALL call sites across the workspace
  - Reorder, add, or remove parameters
  - Update return value handling at call sites

Parameter Format:
  'name:type,name:type,...'
  Example: 'ctx:context.Context,id:int,name:string'

Return Format:
  'type,type,...'
  Example: 'error'
  Example: '*User,error'

Signature Changes Supported:
  - Add new parameters (call sites get zero values or require update)
  - Remove parameters (call sites have argument removed)
  - Reorder parameters (call sites reordered to match)
  - Rename parameters (local only, no call site changes)
  - Change parameter types (may require manual call site fixes)
  - Add/remove/change return types

Related Options:
  --dry-run       Preview changes without applying
  --verbose       Show all call sites that will be updated
  --package-only  Only update calls within the package
  --force         Apply even if some call sites can't be auto-fixed

Examples:
  # Add context parameter to function
  gorefactor change signature myFunc main.go 'ctx:context.Context,id:int'

  # Change return type
  gorefactor change signature GetUser user.go 'id:int' '*User,error'

  # Add parameter and change returns
  gorefactor change signature Process handler.go 'ctx:context.Context,req:Request' 'Response,error'

  # Preview signature change
  gorefactor --dry-run change signature myFunc main.go 'a:string,b:int'

  # Change within package only
  gorefactor --package-only change signature helper internal.go 'x:int'

Warnings:
  - Adding required parameters may require manual call site updates
  - Type changes may cause compilation errors at call sites
  - Use --dry-run to preview the impact

See Also:
  gorefactor rename   - Rename symbols including functions
  gorefactor analyze  - Analyze function usage before changes`)

		case "version":
			fmt.Println(`Version Command - Show gorefactor version information

Usage: gorefactor version
       gorefactor --version

The version command displays:
  - GoRefactor version number
  - Build information
  - Go version used for compilation

Examples:
  gorefactor version
  gorefactor --version`)

		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
			fmt.Fprintln(os.Stderr, "Available commands:")
			fmt.Fprintln(os.Stderr, "  Basic:       move, rename, rename-package, extract, inline, analyze, complexity, unused, delete")
			fmt.Fprintln(os.Stderr, "  Packages:    move-package, move-dir, move-packages")
			fmt.Fprintln(os.Stderr, "  Facades:     create-facade, generate-facades, update-facades")
			fmt.Fprintln(os.Stderr, "  Imports:     clean-aliases, standardize-imports, resolve-alias-conflicts, convert-aliases")
			fmt.Fprintln(os.Stderr, "  Dependencies: move-by-dependencies, organize-by-layers, fix-cycles, analyze-dependencies")
			fmt.Fprintln(os.Stderr, "  Batch:       batch, plan, execute, rollback")
			fmt.Fprintln(os.Stderr, "  Other:       change, version, help")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "Use 'gorefactor help <command>' for detailed help on any command.")
			fmt.Fprintln(os.Stderr, "Use 'gorefactor help options' for global options help.")
		}
	} else {
		cli.Usage()
	}
}