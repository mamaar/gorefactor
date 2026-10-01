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
| `move_symbol` | Move a function, type, constant, or variable between packages; refused when it would leave another build configuration broken |
| `move_package` | Move an entire package to a new location (default build configuration only) |
| `move_dir` | Move a directory of packages (default build configuration only) |
| `move_packages` | Move multiple packages at once (default build configuration only) |
| `rename_symbol` | Rename a symbol across the workspace |
| `rename_method` | Rename a method on a type |
| `rename_field` | Rename a struct field and update all references, including selector expressions, keyed composite literals, and promoted access through embedding |
| `rename_package` | Rename a package (default build configuration only) |
| `extract_function` | Extract a code block into a new function |
| `extract_method` | Extract a code block into a new method |
| `extract_interface` | Extract an interface from a struct's methods |
| `extract_variable` | Extract an expression into a variable |
| `inline_function` | Inline a function at its call sites |
| `inline_method` | Inline a method at its call sites |
| `inline_variable` | Inline a variable at its usage sites |
| `change_signature` | Change a function's parameter list and update all callers |
| `add_context_parameter` | Add a `context.Context` parameter to a function and its callers; with `thread: true`, thread a real context transitively through every call chain in the workspace that reaches it instead; `stop_at` (thread mode only) lists functions where the climb stops |
| `safe_delete` | Delete a symbol only if it has no references in any build configuration; if it does, asks the human user to confirm before deleting unless `force` or `dry_run` is set. `force` deletes the declaration and reports surviving call sites in `skipped_sites` rather than rewriting them |
| `batch_operations` | Run multiple refactoring operations atomically; each step is planned the way its standalone tool plans it |

### Analysis

| Tool | Description |
|------|-------------|
| `analyze_symbol` | Analyze a symbol's usage, references, and dependencies |
| `analyze_dependencies` | Analyze package dependency structure |
| `complexity` | Compute cyclomatic complexity for functions |
| `unused` | Find symbols that no build configuration uses |

### Code Quality Detection & Auto-Fix

| Tool | Description |
|------|-------------|
| `detect_if_init_assignments` | Find if/switch/type-switch init assignments (`:=` or `=`) that should be split into a separate assignment and check |
| `fix_if_init_assignments` | Split if/switch/type-switch init assignments into separate assignment and check statements |
| `detect_boolean_branching` | Find boolean parameters that control branching |
| `fix_boolean_branching` | Auto-fix boolean branching issues |
| `detect_deep_if_else_chains` | Find deeply nested if-else chains |
| `fix_deep_if_else_chains` | Flatten deep if-else chains into sequential guard clauses |
| `detect_switchable_if_chains` | Find if/else-if chains that map onto a tagged, tagless, or type switch |
| `fix_switchable_if_chains` | Convert those chains into switch statements; complements `fix_deep_if_else_chains` |
| `detect_early_returns` | Find conditionals that could be inverted into early-return guard clauses |
| `fix_early_returns` | Invert those conditionals so the happy path is no longer nested |
| `detect_redundant_negations` | Find negated boolean expressions that De Morgan's laws would simplify |
| `fix_redundant_negations` | Simplify those negations where doing so strictly reduces the `!` count |
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
| `move_by_dependencies` | Move symbols shared by several packages to a common package; a move that would break a build configuration is skipped and reported |
| `organize_by_layers` | Organize packages into architectural layers |
| `fix_cycles` | Break import cycles |

## Build configurations

Files are selected with the Go toolchain's own rules: `//go:build` and `// +build` lines, `_GOOS` and `_GOARCH` file name suffixes, and cgo. A single configuration never contains every file of a repository, so GoRefactor computes a covering set of build configurations when the workspace loads: the host's default configuration plus the fewest additional combinations of tags, GOOS and GOARCH such that every file is included in at least one. The set is derived from the files themselves; there is nothing to configure. Files with `import "C"` are type-checked, and `C.*` expressions are never edited.

`load_workspace` and `workspace_status` report the set as `build_configs` (name, GOOS, GOARCH, cgo, tags and the number of files only that configuration adds) and list `uncovered_files`: files no configuration can include, such as `//go:build ignore`, an unsatisfiable constraint, or a file that does not parse.

These tools plan under every configuration and merge the results: `rename_symbol`, `rename_method`, `rename_field`, `change_signature`, `add_context_parameter`, `inline_function`, `inline_method`, `safe_delete` and `unused`. Declarations of the same package, receiver and name in differently constrained files are changed together, identical edits are applied once, and conflicting edits abort the operation naming both configurations. `safe_delete` is blocked by a reference in any configuration, and `unused` reports a symbol only when no configuration uses it. `move_symbol` is planned in the default configuration and refused when another configuration references or declares the symbol.

`batch_operations` plans each step as the standalone tool would: `rename_symbol`, `rename_method` and `rename_field` steps under every configuration, `move_symbol` steps as described above, and `rename_package` and `move_package` steps in the default configuration only. Every step is planned against the unmodified workspace, and two steps whose edits overlap fail the batch. `move_by_dependencies` picks its candidates in the default configuration and plans each like `move_symbol`; every candidate that passes is then moved in one combined plan, whatever its source package. A candidate whose move is refused, or that uses a symbol moved in the same call from another package, stays where it is and is listed in `skipped_sites`.

These tools act on the default configuration only and can miss files excluded by build constraints: `rename_package`, `move_package`, `move_dir`, `move_packages`, `analyze_symbol`, `analyze_dependencies`, `fix_cycles`, `generate_facades` and `update_facades`. The per-file tools (the `detect_*` and `fix_*` analyzers, `complexity`, `extract_*`, `inline_variable`, the import tools, `create_facade`, `organize_by_layers` and `run_go_fix`) only see files the default configuration includes.

Plan results from the tools that plan under every configuration carry:

- `build_configs`: the configurations the plan was computed under, when there is more than one. An abort message names the configuration it came from.
- `uncovered_files`: files in the reached packages that no configuration could type-check, with the configurations that include them and the reason. The plan holds no edits for them. A file that mentions a changed symbol also appears in `skipped_sites` and must be updated by hand.
- `type_errors`: reached packages that have type errors in non-test files, per configuration. Uses inside the erroring code may be missing from the plan.

`unused` returns `build_configs` when there is more than one, and `possibly_used` for symbols that a file no configuration type-checks, or an unresolved name in a file with a type error, may use.

Test files are excluded from type-checking one file at a time, and only those that themselves have type errors. A reached package is refused when dropping its erroring test files does not leave the remaining ones error-free, because then the files that use the changed symbols cannot be identified.

A cross-configuration operation on a repository with about 600 packages and five configurations takes about 10 seconds and peaks around 6 GB of memory; single-configuration workspaces are unaffected.

## Safety

GoRefactor validates all transformations before applying them:

- Compilation validation
- Import cycle detection
- Visibility rule enforcement
- Name conflict detection
- Reference tracking across the workspace

A file watcher keeps the workspace state current as files change on disk.

GoRefactor does not edit a file that carries the Go generated-code header (`// Code generated ... DO NOT EDIT.` before the package clause). An operation whose plan edits one is refused before anything is written, the allow-breaking setting cannot override it, and `dry_run` returns `success: false` with a `blocking_issue` naming the files, diffs still shown. Change the generator's input and regenerate. `move_package`, `move_dir`, `move_packages` and `rename_package` still relocate generated files and rewrite their package clause and import paths, keeping the header. `touch` skips generated files and reports them as `skipped` with a reason; `run_go_fix` runs the Go toolchain's own `go fix` and is not covered.

`fix_early_returns` covers three shapes that `fix_deep_if_else_chains` does not, since that one only fires when an `else` is present and nesting already exceeds a depth threshold. `guard_clause` is `if cond { body }` as the last statement of a function, rewritten to `if !cond { return }` followed by the body. `loop_continue` is the same shape as the last statement of a `for` or `range` body, rewritten with `continue`. `else_after_return` is `if cond { …; return } else { body }`, rewritten by dropping the `else` and dedenting its body. A single call rewrites control flow across every matching function in the workspace at once, so preview it with `dry_run` before applying.

Its restraint is deliberate and worth understanding, because it explains most findings reported as `fixable: false`. It never invents return values: a guard needs a terminating statement, and rather than synthesizing `return nil, 0, ""` from the result types — which would turn an `error` result into a silent nil-error success path — it refuses. In practice this costs nothing, since Go already requires a function with results to end in a terminating statement, so a trailing bare `if` only ever occurs in a result-less function. It also refuses any rewrite where dedenting a block would redeclare a name that already exists in the scope the block lands in (including parameters and named results, which share the function body's scope), since `x := 1` twice in one scope does not compile. Conditionals carrying an init statement (`if v, err := f(); err != nil`) are skipped entirely: those names are scoped to the if statement, and hoisting the body out would move references out of the scope that declares them. `else if` ladders are left alone — only a plain `else` block is collapsed. Nested matches never both fire; the outermost wins, because two overlapping rewrites of the same region cannot be applied together. One incidental effect: any file the tool touches also gets its imports regrouped and gofmt'd, which is the serializer's standard behaviour for every mutating tool, not something specific to this one.

`fix_deep_if_else_chains` works on a whole chain — `if C0 {B0} else if C1 {B1} … else {F}` — and rewrites each link with one of two moves. When a branch already hands control away, the `else` after it is redundant, so it emits `if Ci {Bi}` and carries on at the same level; nothing is negated, and the author's condition polarity survives. At the *last* link, when the final `else` hands control away, it inverts instead: `if !Ci {F}` followed by the body dedented, which is the move that actually removes a level of nesting. Inversion is confined to the last link because inverting an earlier one would pull the remaining `else if` links inside the new guard, nesting rather than flattening; and between the two moves it inverts only when that pays, meaning when the body cannot be elided or when the `else` is strictly the shorter branch, so the short branch is the one that ends up in the guard. "Hands control away" is Go's own definition of a terminating statement, so a branch ending in an if-else whose arms all return counts, as does one ending in `for {}` or an exhaustive `switch`.

What it will not do is hoist an `if init; cond` header. Those declarations are scoped to the whole if statement, so inverting the condition would strand them — and hoisting them safely, including downgrading a colliding `:=` to `=`, is exactly what `fix_if_init_assignments` already does. A link declaring variables in its header therefore keeps them: it can still be elided, but never inverted, so one level of nesting survives. **Run `fix_if_init_assignments` first when a chain declares variables in its `if` headers**, and the same chain flattens the rest of the way. The tool tells you when this applies rather than leaving you to guess: a finding can come back `fixable: true` *and* carry a reason, which means it was flattened as far as it safely could be and names the tool that gets you further. It also declines a chain containing a multi-line raw string literal, since flattening shifts indentation between nesting levels and inside a back-quoted literal that whitespace is part of the value, not layout.

`fix_redundant_negations` is a cleanup pass, not a standalone rewrite. `fix_early_returns` and `fix_deep_if_else_chains` both negate conditions through the shared `srctext.PushNeg`, which already distributes De Morgan on the spot rather than emitting a blunt `!(...)` wrapper, so most of their output needs no cleanup at all. What `PushNeg` cannot see through is an operand it has to treat as opaque — a call, say — where it puts a single `!` in front and leaves whatever is inside alone; a `!!` buried in a call argument is the residue that survives. `fix_redundant_negations` is what clears that up afterwards, pushing the `!` inward with De Morgan's laws — `!(!a && !b)` becomes `a || b`, `!!x` becomes `x`, `!(a == b)` becomes `a != b` — but only where a rewrite strictly reduces the number of `!` operators in the expression. `!(a && b)` is deliberately left alone, because its De Morgan dual `!a || !b` has *more* negations (one becomes two), not fewer; there's no taste call involved, just an operator count that either drops or doesn't. That rule also bounds repetition: every fix lowers the file's total negation count, so running the tool repeatedly converges rather than oscillating. The intended workflow is to run it after `fix_early_returns` or `fix_deep_if_else_chains` to collapse whatever negation those fixers could not push inward themselves.

`rename_field` has a couple of limitations worth knowing about. If the field being renamed has no struct tag, its `encoding/json` (and yaml/db) key is derived from the field name, so the rename silently changes the wire format; a field with an explicit tag like `json:"name"` is unaffected, since existing struct tags are never rewritten. Reflection-based and string-literal field access, such as `reflect`'s `FieldByName("Name")`, can't be detected by static analysis and won't be updated. Renaming an embedded field is also not supported, since that's really a type rename — use `rename_symbol` on the embedded type instead.

`add_context_parameter`'s `thread: true` mode threads a real `context.Context` transitively through every call chain that reaches the function, and it makes a few judgment calls worth understanding before trusting the diff. A call inside a closure — `go func(){...}()`, a `defer`, a callback — is treated as a threading root: it gets `context.TODO()` and the enclosing function is not climbed, *unless* the closure itself captures a ctx already in scope, in which case that capture is used instead. This is deliberate, and it's the most likely reason to see an unexplained `context.TODO()` in the diff. When a goroutine does capture an in-scope ctx, threading it through is usually the right call, but it can also be a real bug: a request-scoped context handed to a goroutine that outlives the request is a cancellation hazard, so review every goroutine the diff touches, not just whether it compiles. Interface handling has two separate blind spots. First, an implementation of a workspace-declared interface that lives outside the loaded workspace (another module, an untouched sibling repo) is invisible to the traversal, which only ever scans workspace packages — rewriting the interface will silently break that implementation. Second, detection of interfaces declared *outside* the workspace, which is what triggers an abort, is best-effort: it catches interfaces reachable from direct imports and from types that already appear in the workspace's own type information (which is how `http.Handler`, `io.Writer`, `sort.Interface`, and `fmt.Stringer` get caught), but it misses satisfaction that's only visible through `any`/reflection — `json.Marshal(v)` hiding a `json.Marshaler` implementation, for instance — and interfaces from imports that are only transitive and never named in a workspace signature. A package with type errors in non-test files keeps whatever partial type information was recovered and is listed in `type_errors`; threading through it can misclassify a caller as having no context in scope, so make sure the workspace builds first. `t.Context()` is emitted unconditionally at test roots and test helpers and requires Go 1.24+; a module still on an older `go` directive won't compile until it's bumped. `t.Context()` is cancelled before `t.Cleanup` functions run, so a testing-parameter helper called directly from a `Cleanup` closure is climbed like an ordinary function and the closure passes `context.TODO()`; a helper reached only transitively from a `Cleanup` closure still passes `t.Context()`, so review such call sites. `stop_at` lists functions where the climb stops: `Name` for a package-level function, `Type.Method` for a method or interface method, or `path/file.go:Type.Method` / `path/file.go:Name` to disambiguate. An unknown or ambiguous entry is an error, and the ambiguous error lists the file-qualified candidates. A stop keeps its signature; calls inside it pass a ctx already in scope, else `context.TODO()`, and its interface and other implementers are untouched. Each stop that took effect is listed in `skipped_sites` with a reason starting `stop_at:`; that is where a hand-written `...WithContext` variant goes, and it is not a build break. Naming the seed as a stop is an error, as is a stop that another function's interface expansion reaches (stop every implementer that reaches the seed, or none); a stop the climb never reaches is ignored silently. When the climb would have to edit a generated file, a declaration there gaining ctx or a call site there needing the argument, it refuses with no changes, naming the generated files and the hand-written functions to pass as `stop_at` in `path/file.go:Type.Method` form. For a hand-written `NativeBridgeWrapper.MapSearch` satisfying a generated `NativeBridge` interface, pass `stop_at: ["NativeBridgeWrapper.MapSearch"]` and hand-write `MapSearchWithContext`. If the threaded function is itself declared in or called from generated code, no stop helps and the message says to change the generator.

A `context.TODO()` left at a root is a marker, not a fix: `main` almost certainly wants `context.Background()` plus signal handling, and the tool won't write that for you. Adding a context parameter to an exported function is also a breaking API change for anything outside the workspace calling it. And as with every operation here, reflection is invisible to it, and so are generators and their templates (generated files in the workspace are seen and protected, as described under Safety); files excluded by build constraints are covered by the build configurations described above, except those listed in `uncovered_files`.

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
