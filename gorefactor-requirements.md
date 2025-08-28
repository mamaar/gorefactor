# gorefactor CLI Enhancement Requirements

## Overview

This document outlines the missing functionality in the gorefactor CLI tool that would be required to handle comprehensive Go repository restructuring and architectural refactoring. These requirements emerged from analyzing a real-world repository cleanup scenario involving domain-driven design patterns, import alias management, and architectural pattern transformations.

## Current Capabilities

gorefactor currently excels at:
- Moving individual symbols between packages
- Automatic import updates when moving symbols  
- Creating target packages if they don't exist
- Validating import cycles
- Basic symbol renaming across codebases
- Extract/inline operations for methods, functions, interfaces

## Missing Functionality Categories

### 1. Bulk Package Operations

**Current Limitation**: Must move each symbol individually across potentially dozens of files.

**Required Commands**:

```bash
# Move entire package with all symbols
gorefactor move-package internal/shared/command pkg/command

# Move directory structure preserving relationships  
gorefactor move-dir internal/shared pkg/infrastructure

# Move multiple related packages atomically
gorefactor move-packages internal/shared/command,internal/shared/events pkg/infrastructure/
```

**Use Cases**:
- Moving shared infrastructure packages (e.g., `internal/shared/*` → `pkg/*`)
- Reorganizing module hierarchies
- Breaking up monolithic packages into focused packages
- Consolidating scattered packages

### 2. Module Facade Creation

**Current Limitation**: No way to automatically create and maintain facade packages with re-exports.

**Required Commands**:

```bash
# Create facade package with selected re-exports
gorefactor create-facade pkg/commission \
  --from modules/commission/models.Commission \
  --from modules/commission/commands.CreateCommissionCommand \
  --from modules/commission/repo.CommissionRepository

# Auto-generate facades for all modules
gorefactor generate-facades modules/ --target pkg/

# Update existing facades when underlying packages change
gorefactor update-facades pkg/
```

**Use Cases**:
- Creating clean public APIs for complex module structures
- Eliminating import alias requirements
- Providing stable interfaces while allowing internal restructuring
- Generating consistent module interfaces across a codebase

### 3. Import Alias Management

**Current Limitation**: No bulk import alias management capabilities.

**Required Commands**:

```bash
# Remove all import aliases and use full package names
gorefactor clean-aliases --workspace .

# Standardize import aliases across codebase
gorefactor standardize-imports \
  --alias events=blind-willie/pkg/events \
  --alias command=blind-willie/pkg/command

# Find and fix import alias conflicts
gorefactor resolve-alias-conflicts --workspace .

# Convert between aliased and non-aliased imports
gorefactor convert-aliases --to-full-names --workspace .
```

**Use Cases**:
- Cleaning up systematic alias patterns like `commissionCommands "module/commission/commands"`
- Standardizing import conventions across teams
- Resolving naming conflicts that require aliases
- Migrating between different import styles

### 4. Architectural Pattern Transformation

**Current Limitation**: gorefactor works at the symbol level, not architectural pattern level.

**Required Commands**:

```bash
# Transform reflection-based command bus to direct calls
gorefactor transform command-bus \
  --from internal/shared/command \
  --pattern direct-calls \
  --handlers modules/*/commands/

# Convert repository pattern implementations
gorefactor transform repository \
  --from generic-storage \
  --to domain-specific \
  --modules modules/*/repo/

# Transform dependency injection patterns
gorefactor transform di \
  --from constructor-injection \
  --to functional-options \
  --packages internal/services/
```

**Use Cases**:
- Converting reflection-based patterns to type-safe alternatives
- Migrating between different architectural approaches
- Standardizing patterns across a codebase
- Removing over-engineered abstractions

### 5. Dependency Graph Operations

**Current Limitation**: Each operation is isolated; no understanding of dependency relationships.

**Required Commands**:

```bash
# Move symbols based on dependency analysis
gorefactor move-by-dependencies \
  --move-shared-to pkg/ \
  --keep-internal internal/app,internal/handlers

# Reorder imports based on dependency layers
gorefactor organize-by-layers \
  --domain modules/ \
  --infrastructure pkg/ \
  --application internal/

# Detect and fix circular dependencies
gorefactor fix-cycles --workspace .

# Analyze dependency flow and suggest improvements
gorefactor analyze-dependencies \
  --detect-backwards-deps \
  --suggest-moves \
  --output analysis.json
```

**Use Cases**:
- Fixing backwards dependencies in layered architectures
- Organizing code by architectural layers
- Breaking circular dependencies automatically
- Validating dependency flow constraints

### 6. Batch Operations with Rollback

**Current Limitation**: Each command is independent; no batch operations or comprehensive rollback.

**Required Commands**:

```bash
# Execute multiple operations atomically
gorefactor batch \
  --operation "move-package internal/shared/command pkg/command" \
  --operation "move-package internal/shared/events pkg/events" \
  --operation "update-facades pkg/" \
  --rollback-on-failure

# Preview entire refactoring plan
gorefactor plan \
  --move-shared internal/shared pkg/ \
  --create-facades modules/ pkg/ \
  --output refactor-plan.json \
  --dry-run

# Execute previously planned refactoring
gorefactor execute refactor-plan.json

# Rollback last batch operation
gorefactor rollback --last-batch
```

**Use Cases**:
- Executing complex multi-step refactorings safely
- Previewing changes before execution
- Rolling back failed or undesired refactorings
- Coordinating multiple related changes

## Real-World Example

**Scenario**: Repository cleanup involving:
1. Moving `internal/shared/{command,events,log,correlation}` → `pkg/infrastructure/`
2. Creating module facades in `pkg/{commission,piece,deliverable}/`
3. Updating imports across 50+ files
4. Removing import aliases like `cev "blind-willie/internal/shared/events"`
5. Transforming command bus from reflection to direct calls

**Required Command Sequence**:
```bash
# 1. Bulk package movement
gorefactor move-packages \
  internal/shared/command,internal/shared/events,internal/shared/log \
  pkg/infrastructure/

# 2. Auto-generate facades  
gorefactor generate-module-facades \
  --modules modules/ \
  --target pkg/ \
  --export-commands,models,events

# 3. Clean up import aliases globally
gorefactor clean-import-aliases --workspace .

# 4. Transform architectural pattern
gorefactor transform-pattern command-bus \
  --from reflection-based \
  --to direct-calls \
  --bus-package pkg/infrastructure/command

# 5. Validate entire refactoring
gorefactor validate-refactoring \
  --ensure-no-cycles \
  --ensure-compilation \
  --run-tests
```

## Implementation Feasibility

### **High Feasibility**
- **Bulk package operations**: Extension of existing move logic
- **Import alias management**: Leverages existing import rewriting
- **Facade generation**: Generates Go files with re-exports

### **Medium Feasibility**
- **Dependency graph operations**: Requires sophisticated analysis but builds on existing AST parsing
- **Batch operations with rollback**: Needs transaction-like semantics and state management

### **High Complexity**
- **Architectural pattern transformation**: Requires deep understanding of Go patterns and significant template/generation capabilities

## Success Criteria

A successful implementation should enable:

1. **Repository-scale refactoring** in single commands rather than hundreds of individual symbol moves
2. **Safe execution** with preview, validation, and rollback capabilities
3. **Architectural evolution** by transforming patterns rather than just moving symbols
4. **Import management** that handles complex alias scenarios automatically
5. **Dependency awareness** that understands and preserves architectural constraints

## Conclusion

These enhancements would transform gorefactor from a **symbol-level refactoring tool** into a **comprehensive repository restructuring platform**. The missing functionality represents the difference between tactical code changes and strategic architectural evolution.

The requirements outlined here emerged from real-world repository cleanup scenarios and represent common challenges in maintaining large Go codebases with evolving architectural patterns.