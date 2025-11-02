package cli

import (
	"flag"
	"fmt"
	"os"
)

// Usage prints the usage information for the gorefactor command
func Usage() {
	fmt.Fprintf(os.Stderr, `GoRefactor - Safe Go code refactoring tool

Usage: gorefactor [options] <command> [arguments]

Basic Commands:
  move <symbol> <from-package> <to-package>
    Move a symbol from one package to another

  rename <symbol> <new-name> [package]
    Rename a symbol (optionally limited to a specific package)

  rename-package <old-name> <new-name> <package-path>
    Rename a package and update all references

  extract <type> [arguments...]
    Extract method, function, interface, variable, or block
    Types: method, function, interface, variable, block

  inline <type> [arguments...]
    Inline method, variable, or function calls
    Types: method, variable, function

  analyze <symbol> [package]
    Analyze the impact of refactoring a symbol

  complexity [package]
    Analyze cyclomatic complexity of functions in workspace or package

  unused [--all] [--package <path>]
    Find unused symbols that can be safely deleted

Bulk Package Operations:
  move-package <source-package> <target-package>
    Move entire package with all symbols

  move-dir <source-dir> <target-dir>
    Move directory structure preserving relationships

  move-packages <package1,package2,...> <target-dir>
    Move multiple related packages atomically

Module Facade Creation:
  create-facade <facade-package> --from <package.Symbol>...
    Create facade package with selected re-exports

  generate-facades <modules-dir> <target-dir>
    Auto-generate facades for all modules

  update-facades <facade-dir>
    Update existing facades when underlying packages change

Import Alias Management:
  clean-aliases [workspace]
    Remove all import aliases and use full package names

  standardize-imports --alias <alias=package>...
    Standardize import aliases across codebase

  resolve-alias-conflicts [workspace]
    Find and fix import alias conflicts

  convert-aliases [--to-full-names] [workspace]
    Convert between aliased and non-aliased imports

Dependency Graph Operations:
  move-by-dependencies [--move-shared-to <dir>] [--keep-internal <dirs>]
    Move symbols based on dependency analysis

  organize-by-layers --domain <dir> --infrastructure <dir> --application <dir>
    Organize packages by architectural layers

  fix-cycles [--auto-fix] [workspace]
    Detect and fix circular dependencies

  analyze-dependencies [--detect-backwards-deps] [--suggest-moves] [workspace]
    Analyze dependency flow and suggest improvements

Batch Operations:
  batch --operation "<cmd>"... [--rollback-on-failure] [--dry-run]
    Execute multiple operations atomically

  plan --move-shared <from> <to> --output <file> [--dry-run]
    Create structured refactoring plan

  execute <plan-file>
    Execute previously created refactoring plan

  rollback [--last-batch | --to-step <n>]
    Rollback operations to previous state

General:
  help [command]
    Show help for a specific command

Options:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Examples:
  # Move a function from one package to another
  gorefactor move MyFunction pkg/old pkg/new

  # Rename a type across the entire workspace
  gorefactor rename OldType NewType

  # Rename a function only within a specific package
  gorefactor --package-only rename oldFunc newFunc pkg/mypackage

  # Rename a package and update all imports
  gorefactor rename-package auth authentication internal/auth

  # Rename a method on a specific type
  gorefactor rename MyStruct.Write MyStruct.WriteBytes

  # Rename an interface method and all its implementations  
  gorefactor --rename-implementations rename Writer.Write Writer.WriteBytes

  # Rename an interface method (old syntax, still supported)
  gorefactor --rename-implementations rename Execute Process

  # Extract a method from lines 10-15 in main.go
  gorefactor extract method main.go 10 15 newMethod MyStruct

  # Extract a function from lines 10-15 in main.go
  gorefactor extract function main.go 10 15 newFunction

  # Extract an interface from a struct
  gorefactor extract interface MyStruct MyInterface Method1,Method2 pkg/interfaces

  # Extract a variable from an expression
  gorefactor extract variable main.go 20 20 myVar "someComplexExpression()"

  # Inline a method call with its implementation
  gorefactor inline method processData Calculator main.go

  # Inline a variable with its value
  gorefactor inline variable tempResult main.go

  # Inline a function call with its implementation
  gorefactor inline function utility main.go

  # Preview changes without applying them
  gorefactor --dry-run move MyStruct pkg/models pkg/types

  # Analyze impact before refactoring
  gorefactor analyze MyFunction pkg/utils

Bulk Operations Examples:
  # Move entire package
  gorefactor move-package internal/shared/command pkg/command

  # Move multiple packages atomically
  gorefactor move-packages internal/shared/command,internal/shared/events pkg/infrastructure/

  # Create facade with re-exports
  gorefactor create-facade pkg/commission --from modules/commission/models.Commission

  # Generate facades for all modules
  gorefactor generate-facades modules/ pkg/

  # Clean up import aliases globally
  gorefactor clean-aliases --workspace .

  # Standardize import aliases with rules
  gorefactor standardize-imports --alias events=myproject/pkg/events --alias cmd=myproject/pkg/command

  # Move symbols based on dependency analysis
  gorefactor move-by-dependencies --move-shared-to pkg/ --keep-internal internal/app

  # Organize by architectural layers
  gorefactor organize-by-layers --domain modules/ --infrastructure pkg/ --application internal/

  # Fix circular dependencies
  gorefactor fix-cycles --auto-fix --workspace .

  # Analyze dependencies with suggestions
  gorefactor analyze-dependencies --detect-backwards-deps --suggest-moves --output analysis.json

Batch Operations Examples:
  # Execute multiple operations atomically
  gorefactor batch --operation "move-package internal/shared pkg/" --operation "clean-aliases" --rollback-on-failure

  # Create refactoring plan
  gorefactor plan --move-shared internal/shared pkg/ --create-facades modules/ pkg/ --output plan.json --dry-run

  # Execute plan from file
  gorefactor execute refactor-plan.json

  # Rollback last batch operation
  gorefactor rollback --last-batch
`)
}