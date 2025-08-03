# GoRefactor

GoRefactor is a safe, automated refactoring tool for Go code. It provides intelligent symbol moving and renaming with comprehensive safety checks to ensure your code remains correct after refactoring.

## Features

- **Safe Symbol Moving**: Move functions, types, constants, and variables between packages
- **Intelligent Renaming**: Rename symbols across your entire workspace or within specific packages
- **Comprehensive Validation**: Checks for compilation errors, import cycles, naming conflicts, and visibility violations
- **Format Preservation**: Maintains your code formatting and style
- **Dry Run Mode**: Preview changes before applying them
- **Backup Support**: Automatic backup creation before modifications

## Installation

```bash
# Clone the repository
git clone https://github.com/mamaar/gorefactor.git
cd gorefactor

# Build and install
make install

# Or just build locally
make build
./bin/gorefactor --help
```

## Usage

### Move a Symbol

Move a function, type, constant, or variable from one package to another:

```bash
# Move a function
gorefactor move MyFunction pkg/old pkg/new

# Move a type and all its methods
gorefactor move UserType internal/models pkg/types

# Preview changes without applying
gorefactor --dry-run move ConfigStruct config pkg/settings
```

### Rename a Symbol

Rename a symbol across your entire workspace or within a specific package:

```bash
# Rename across entire workspace
gorefactor rename OldName NewName

# Rename only within a specific package
gorefactor rename Helper Utility pkg/utils

# Use --package-only flag to ensure package-scoped rename
gorefactor --package-only rename internal externalAPI pkg/client
```

### Analyze Symbol Usage

Analyze a symbol to understand its usage before refactoring:

```bash
# Analyze a symbol
gorefactor analyze MyFunction

# Analyze with verbose output to see all references
gorefactor --verbose analyze DatabaseConnection pkg/db

# Output analysis in JSON format
gorefactor --json analyze Config
```

## Command Line Options

- `--workspace PATH`: Set the workspace root directory (default: current directory)
- `--dry-run`: Preview changes without applying them
- `--json`: Output results in JSON format
- `--verbose`: Enable verbose output
- `--force`: Force operation even with warnings
- `--allow-breaking`: Allow potentially breaking refactorings that may require manual fixes
- `--backup`: Create backup files before changes (default: true)
- `--package-only`: For rename operations, only rename within the specified package

## Examples

### Example 1: Moving a Utility Function

```bash
# Check current usage
gorefactor analyze StringUtils pkg/common

# Move to a more appropriate package
gorefactor --dry-run move StringUtils pkg/common pkg/strings

# Apply the change
gorefactor move StringUtils pkg/common pkg/strings
```

### Example 2: Renaming a Widely-Used Type

```bash
# See the impact first
gorefactor analyze User pkg/models

# Preview the rename
gorefactor --dry-run rename User Account

# Apply with backups
gorefactor --backup rename User Account
```

### Example 3: Package-Scoped Rename

```bash
# Rename only within the auth package
gorefactor rename Token AuthToken pkg/auth

# Ensure it only affects the specified package
gorefactor --package-only rename validate checkAuth pkg/auth
```

### Example 4: Breaking Refactoring with Manual Fix Intent

```bash
# Allow potentially breaking changes when you plan to fix issues manually
gorefactor --allow-breaking move LegacyHandler pkg/old pkg/new

# Combine with dry-run to see what breaking changes would occur
gorefactor --dry-run --allow-breaking rename ComplexType NewComplexType

# Use with backup for safety when allowing breaking changes
gorefactor --allow-breaking --backup move DatabaseConnection internal/db pkg/storage
```

## Safety Features

GoRefactor includes multiple safety checks:

1. **Compilation Validation**: Ensures changes don't break compilation
2. **Import Cycle Detection**: Prevents creating circular dependencies
3. **Visibility Rules**: Maintains Go's export rules and accessibility
4. **Name Conflict Detection**: Prevents naming conflicts in target packages
5. **Reference Tracking**: Updates all references to moved/renamed symbols

**Note:** The `--allow-breaking` flag disables these safety checks when you need to perform potentially breaking refactorings and plan to fix issues manually afterward.

## How It Works

1. **Parsing**: GoRefactor parses your entire Go workspace using the standard `go/ast` package
2. **Analysis**: Builds a complete symbol table and dependency graph
3. **Validation**: Checks all safety conditions before making changes
4. **Transformation**: Applies changes while preserving formatting
5. **Verification**: Ensures the resulting code is valid

## Limitations

- Currently supports moving functions, types, constants, and variables (not methods independently)
- Requires all code to be syntactically valid Go
- Works with modules (go.mod) and GOPATH projects

## Development

```bash
# Run tests
make test

# Run tests with coverage
make test-coverage

# Format code
make fmt

# Build for multiple platforms
make build-all
```

## Contributing

Contributions are welcome! Please ensure:
- All tests pass (`make test`)
- Code is formatted (`make fmt`)
- New features include tests

## License

MIT License - see LICENSE file for details