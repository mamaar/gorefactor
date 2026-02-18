# GoRefactor

GoRefactor is an MCP (Model Context Protocol) server that provides safe, automated refactoring tools for Go code. It integrates with AI assistants like Claude to enable intelligent code transformations with comprehensive safety checks.

## Installation

```bash
git clone https://github.com/mamaar/gorefactor.git
cd gorefactor

# Build the MCP server
make build

# Or install to GOPATH/bin
make install
```

## Configuration

Add GoRefactor to your MCP client configuration. For Claude Code, add to your settings:

```json
{
  "mcpServers": {
    "gorefactor": {
      "command": "gorefactor-mcp"
    }
  }
}
```

The server communicates over stdio using the MCP protocol.

## Tools

### Workspace

| Tool | Description |
|------|-------------|
| `load_workspace` | Load a Go workspace for analysis and refactoring |
| `workspace_status` | Show current workspace state |

### Refactoring

| Tool | Description |
|------|-------------|
| `move_symbol` | Move a function, type, constant, or variable between packages |
| `move_package` | Move an entire package to a new location |
| `move_dir` | Move a directory of packages |
| `move_packages` | Move multiple packages at once |
| `rename_symbol` | Rename a symbol across the workspace |
| `rename_method` | Rename a method on a type |
| `rename_package` | Rename a package |
| `extract_function` | Extract a code block into a new function |
| `extract_method` | Extract a code block into a new method |
| `extract_interface` | Extract an interface from a struct's methods |
| `extract_variable` | Extract an expression into a variable |
| `inline_function` | Inline a function at its call sites |
| `inline_method` | Inline a method at its call sites |
| `inline_variable` | Inline a variable at its usage sites |
| `change_signature` | Change a function's parameter list and update all callers |
| `add_context_parameter` | Add a `context.Context` parameter to a function and its callers |
| `safe_delete` | Delete a symbol only if it has no references |
| `batch_operations` | Run multiple refactoring operations atomically |

### Analysis

| Tool | Description |
|------|-------------|
| `analyze_symbol` | Analyze a symbol's usage, references, and dependencies |
| `analyze_dependencies` | Analyze package dependency structure |
| `complexity` | Compute cyclomatic complexity for functions |
| `unused` | Find unused symbols in the workspace |

### Code Quality Detection & Auto-Fix

| Tool | Description |
|------|-------------|
| `detect_if_init_assignments` | Find assignments that could use if-init statements |
| `fix_if_init_assignments` | Auto-fix if-init assignments |
| `detect_boolean_branching` | Find boolean parameters that control branching |
| `fix_boolean_branching` | Auto-fix boolean branching issues |
| `detect_deep_if_else_chains` | Find deeply nested if-else chains |
| `fix_deep_if_else_chains` | Flatten deep if-else chains |
| `detect_improper_error_wrapping` | Find improperly wrapped errors |
| `fix_error_wrapping` | Auto-fix error wrapping |
| `detect_missing_context_params` | Find functions that should accept `context.Context` |
| `detect_environment_booleans` | Find environment variable boolean patterns |

### Import Management

| Tool | Description |
|------|-------------|
| `clean_aliases` | Remove unnecessary import aliases |
| `standardize_imports` | Standardize import grouping and ordering |
| `resolve_alias_conflicts` | Resolve conflicting import aliases |
| `convert_aliases` | Convert between alias styles |

### Package Organization

| Tool | Description |
|------|-------------|
| `create_facade` | Create a facade package that re-exports symbols |
| `generate_facades` | Generate facades for a set of packages |
| `update_facades` | Update existing facades after changes |
| `move_by_dependencies` | Reorganize packages based on dependency analysis |
| `organize_by_layers` | Organize packages into architectural layers |
| `fix_cycles` | Break import cycles |

## Safety

GoRefactor validates all transformations before applying them:

- Compilation validation
- Import cycle detection
- Visibility rule enforcement
- Name conflict detection
- Reference tracking across the workspace

A file watcher keeps the workspace state current as files change on disk.

## Development

```bash
make test           # Run all tests
make test-coverage  # Run tests with coverage report
make lint           # Run golangci-lint
make fmt            # Format code
make dev            # Format, build, and test
```

## License

MIT License - see LICENSE file for details
