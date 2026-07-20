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
| `detect_early_returns` | Find conditionals that could be inverted into early-return guard clauses |
| `fix_early_returns` | Invert those conditionals so the happy path is no longer nested; previews by default |
| `detect_redundant_negations` | Find negated boolean expressions that De Morgan's laws would simplify |
| `fix_redundant_negations` | Simplify those negations where doing so strictly reduces the `!` count; previews by default |
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

`fix_early_returns` covers three shapes that `fix_deep_if_else_chains` does not, since that one only fires when an `else` is present and nesting already exceeds a depth threshold. `guard_clause` is `if cond { body }` as the last statement of a function, rewritten to `if !cond { return }` followed by the body. `loop_continue` is the same shape as the last statement of a `for` or `range` body, rewritten with `continue`. `else_after_return` is `if cond { …; return } else { body }`, rewritten by dropping the `else` and dedenting its body. Unlike every other mutating tool here, it takes `apply` rather than `dry_run` and **previews by default**: a call with no arguments returns the full before/after diff and writes nothing, because a single call rewrites control flow across every matching function in the workspace at once.

Its restraint is deliberate and worth understanding, because it explains most findings reported as `fixable: false`. It never invents return values: a guard needs a terminating statement, and rather than synthesizing `return nil, 0, ""` from the result types — which would turn an `error` result into a silent nil-error success path — it refuses. In practice this costs nothing, since Go already requires a function with results to end in a terminating statement, so a trailing bare `if` only ever occurs in a result-less function. It also refuses any rewrite where dedenting a block would redeclare a name that already exists in the scope the block lands in (including parameters and named results, which share the function body's scope), since `x := 1` twice in one scope does not compile. Conditionals carrying an init statement (`if v, err := f(); err != nil`) are skipped entirely: those names are scoped to the if statement, and hoisting the body out would move references out of the scope that declares them. `else if` ladders are left alone — only a plain `else` block is collapsed. Nested matches never both fire; the outermost wins, because two overlapping rewrites of the same region cannot be applied together. One incidental effect: any file the tool touches also gets its imports regrouped and gofmt'd, which is the serializer's standard behaviour for every mutating tool, not something specific to this one.

`fix_redundant_negations` is a cleanup pass, not a standalone rewrite: `fix_early_returns` and `fix_deep_if_else_chains` both invert conditions by wrapping them in a blunt `!(...)`, on the principle that each operation should stay simple rather than also being clever about negation. `fix_redundant_negations` is what does the clever part afterwards, pushing the `!` inward with De Morgan's laws — `!(!a && !b)` becomes `a || b`, `!!x` becomes `x`, `!(a == b)` becomes `a != b` — but only where a rewrite strictly reduces the number of `!` operators in the expression. `!(a && b)` is deliberately left alone, because its De Morgan dual `!a || !b` has *more* negations (one becomes two), not fewer; there's no taste call involved, just an operator count that either drops or doesn't. That rule also bounds repetition: every fix lowers the file's total negation count, so running the tool repeatedly converges rather than oscillating. Like `fix_early_returns` it takes `apply` rather than `dry_run` and previews by default. The intended workflow is to run it after `fix_early_returns` or `fix_deep_if_else_chains` to collapse the `!(...)` wrappers those fixers emit.

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
