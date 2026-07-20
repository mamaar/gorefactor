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
| `rename_field` | Rename a struct field and update all references, including selector expressions, keyed composite literals, and promoted access through embedding |
| `rename_package` | Rename a package |
| `extract_function` | Extract a code block into a new function |
| `extract_method` | Extract a code block into a new method |
| `extract_interface` | Extract an interface from a struct's methods |
| `extract_variable` | Extract an expression into a variable |
| `inline_function` | Inline a function at its call sites |
| `inline_method` | Inline a method at its call sites |
| `inline_variable` | Inline a variable at its usage sites |
| `change_signature` | Change a function's parameter list and update all callers |
| `add_context_parameter` | Add a `context.Context` parameter to a function and its callers; with `thread: true`, thread a real context transitively through every call chain in the workspace that reaches it instead |
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
| `detect_if_init_assignments` | Find if/switch/type-switch init assignments (`:=` or `=`) that should be split into a separate assignment and check |
| `fix_if_init_assignments` | Split if/switch/type-switch init assignments into separate assignment and check statements |
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

`rename_field` has a couple of limitations worth knowing about. If the field being renamed has no struct tag, its `encoding/json` (and yaml/db) key is derived from the field name, so the rename silently changes the wire format; a field with an explicit tag like `json:"name"` is unaffected, since existing struct tags are never rewritten. Reflection-based and string-literal field access, such as `reflect`'s `FieldByName("Name")`, can't be detected by static analysis and won't be updated. Renaming an embedded field is also not supported, since that's really a type rename — use `rename_symbol` on the embedded type instead.

`add_context_parameter`'s `thread: true` mode threads a real `context.Context` transitively through every call chain that reaches the function, and it makes a few judgment calls worth understanding before trusting the diff. A call inside a closure — `go func(){...}()`, a `defer`, a callback — is treated as a threading root: it gets `context.TODO()` and the enclosing function is not climbed, *unless* the closure itself captures a ctx already in scope, in which case that capture is used instead. This is deliberate, and it's the most likely reason to see an unexplained `context.TODO()` in the diff. When a goroutine does capture an in-scope ctx, threading it through is usually the right call, but it can also be a real bug: a request-scoped context handed to a goroutine that outlives the request is a cancellation hazard, so review every goroutine the diff touches, not just whether it compiles. Interface handling has two separate blind spots. First, an implementation of a workspace-declared interface that lives outside the loaded workspace (another module, an untouched sibling repo) is invisible to the traversal, which only ever scans workspace packages — rewriting the interface will silently break that implementation. Second, detection of interfaces declared *outside* the workspace, which is what triggers an abort, is best-effort: it catches interfaces reachable from direct imports and from types that already appear in the workspace's own type information (which is how `http.Handler`, `io.Writer`, `sort.Interface`, and `fmt.Stringer` get caught), but it misses satisfaction that's only visible through `any`/reflection — `json.Marshal(v)` hiding a `json.Marshaler` implementation, for instance — and interfaces from imports that are only transitive and never named in a workspace signature. External test packages (`package foo_test`) are not type-checked by this tool's loader, which folds in only same-package test files, so call sites in an external test package are not rewritten. The loader also silently swallows type-check errors and keeps whatever partial type information it got, so threading a workspace that doesn't currently compile can misclassify a caller as having no context in scope — make sure the workspace builds first. `t.Context()` is emitted unconditionally at test roots and requires Go 1.24+; a module still on an older `go` directive won't compile until it's bumped. A `context.TODO()` left at a root is a marker, not a fix: `main` almost certainly wants `context.Background()` plus signal handling, and the tool won't write that for you. Adding a context parameter to an exported function is also a breaking API change for anything outside the workspace calling it. And as with every operation here, reflection, code generation, and files excluded by build tags are invisible to it.

Threading aborts outright — making no changes at all and returning an error — in two cases: when a function it would thread through is a method satisfying an interface declared outside the workspace, since adding ctx would silently stop it satisfying that interface; and when the function is ever used as a value rather than called (`var h = Seed`, `Seed` passed as a callback or taken as a method value). Both are refusals by design: the tool would rather stop than hand back a half-threaded workspace that doesn't compile.

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
