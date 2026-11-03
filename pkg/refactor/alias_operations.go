package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"path/filepath"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// CleanAliasesOperation implements cleaning import aliases
type CleanAliasesOperation struct {
	Request types.CleanAliasesRequest
}

func (op *CleanAliasesOperation) Type() types.OperationType {
	return types.CleanAliasesOperation
}

func (op *CleanAliasesOperation) Description() string {
	return fmt.Sprintf("Clean import aliases in workspace %s", op.Request.Workspace)
}

func (op *CleanAliasesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	return nil
}

func (op *CleanAliasesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Analyze all files for import aliases
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			changes := op.cleanFileAliases(file)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *CleanAliasesOperation) cleanFileAliases(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Walk through import declarations
	ast.Inspect(file.AST, func(n ast.Node) bool {
		importSpec, ok := n.(*ast.ImportSpec)
		if ok && importSpec.Name != nil {
			// Found an aliased import
			alias := importSpec.Name.Name
			importPath := strings.Trim(importSpec.Path.Value, `"`)

			// Skip dot imports and blank imports
			if alias == "." || alias == "_" {
				return true
			}

			// Only remove aliases that match the default package name (redundant aliases).
			// An alias like `import log "log"` is unnecessary, but
			// `import mylog "log"` is intentional and must be kept.
			parts := strings.Split(strings.Trim(importPath, "/"), "/")
			defaultName := parts[len(parts)-1]
			if alias != defaultName {
				return true
			}

			if !op.Request.PreserveConflicts || !op.wouldCauseConflict(alias, importPath, file) {
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Name.Pos()),
					End:         int(importSpec.Name.End()),
					OldText:     alias,
					NewText:     "",
					Description: fmt.Sprintf("Remove import alias '%s' for package %s", alias, importPath),
				})
			}
		}
		return true
	})

	return changes
}

func (op *CleanAliasesOperation) wouldCauseConflict(alias, importPath string, file *types.File) bool {
	if file.AST == nil {
		return false
	}
	// Compute the default package name (last path component) that would be used
	// if the alias is removed.
	parts := strings.Split(strings.Trim(importPath, "/"), "/")
	defaultName := parts[len(parts)-1]

	// Check 1: another import in the file uses the same default name without an alias.
	for _, imp := range file.AST.Imports {
		if imp.Name != nil {
			continue // has its own alias, no collision
		}
		otherPath := strings.Trim(imp.Path.Value, `"`)
		if otherPath == importPath {
			continue // same import
		}
		otherParts := strings.Split(otherPath, "/")
		if otherParts[len(otherParts)-1] == defaultName {
			return true
		}
	}

	// Check 2: a top-level declared identifier uses the same name.
	conflictFound := false
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if conflictFound {
			return false
		}
		switch decl := n.(type) {
		case *ast.FuncDecl:
			if decl.Name != nil && decl.Name.Name == defaultName {
				conflictFound = true
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.Name == defaultName {
						conflictFound = true
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.Name == defaultName {
							conflictFound = true
						}
					}
				}
			}
		}
		return true
	})

	_ = alias // alias is the current alias being removed; defaultName is the replacement
	return conflictFound
}

// StandardizeImportsOperation implements standardizing import aliases
type StandardizeImportsOperation struct {
	Request types.StandardizeImportsRequest
}

func (op *StandardizeImportsOperation) Type() types.OperationType {
	return types.StandardizeImportsOperation
}

func (op *StandardizeImportsOperation) Description() string {
	return fmt.Sprintf("Standardize imports in workspace %s with %d rules", op.Request.Workspace, len(op.Request.Rules))
}

func (op *StandardizeImportsOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	if len(op.Request.Rules) == 0 {
		return fmt.Errorf("no alias rules specified")
	}

	for i, rule := range op.Request.Rules {
		if rule.PackagePattern == "" {
			return fmt.Errorf("package pattern at rule index %d cannot be empty", i)
		}
		if rule.Alias == "" {
			return fmt.Errorf("alias at rule index %d cannot be empty", i)
		}
	}

	return nil
}

func (op *StandardizeImportsOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	wanted := make(map[string]bool, len(op.Request.Files))
	for _, f := range op.Request.Files {
		wanted[f] = true
	}
	for _, pkg := range ws.Packages {
		for _, file := range packageAllFiles(pkg) {
			if len(op.Request.Files) > 0 {
				if !wanted[file.Path] {
					continue
				}
				delete(wanted, file.Path)
			}
			changes := op.standardizeFileImports(ws, pkg, file, plan)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}
	// A mistyped path would otherwise look like "nothing to change".
	for f := range wanted {
		return nil, fmt.Errorf("file %s is not in the workspace", f)
	}

	return plan, nil
}

// uncheckedReason says why file has no reliable type information, or "".
func uncheckedReason(pkg *types.Package, file *types.File) string {
	name := filepath.Base(file.Path)
	if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
		return "package is not type-checked"
	}
	err := pkg.ExcludedTestFiles[name]
	if err != nil {
		return "excluded from type-checking: " + err.Error()
	}
	err = pkg.ProductionErrs[name]
	if err != nil {
		return "type errors may hide uses: " + err.Error()
	}
	return ""
}

// standardizeFileImports renames each matching import to its rule's alias
// (dropping the alias when it equals the package name) and rewrites its uses.
// An import it cannot rename safely is recorded in plan.SkippedSites and left alone.
func (op *StandardizeImportsOperation) standardizeFileImports(ws *types.Workspace, pkg *types.Package, file *types.File, plan *types.RefactoringPlan) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}
	offset := func(pos token.Pos) int { return ws.FileSet.Position(pos).Offset }

	for _, importSpec := range file.AST.Imports {
		importPath := strings.Trim(importSpec.Path.Value, `"`)
		if importSpec.Name != nil && (importSpec.Name.Name == "." || importSpec.Name.Name == "_") {
			continue
		}
		rule := op.ruleForImport(importPath)
		if rule == nil {
			continue
		}

		skip := func(reason string) {
			plan.SkippedSites = append(plan.SkippedSites, types.SkippedSite{
				File:    file.Path,
				Line:    ws.FileSet.Position(importSpec.Pos()).Line,
				Reason:  reason,
				Snippet: importSpec.Path.Value,
			})
		}
		reason := uncheckedReason(pkg, file)
		if reason != "" {
			skip(reason)
			continue
		}
		obj := pkg.TypesInfo.Implicits[importSpec]
		if importSpec.Name != nil {
			obj = pkg.TypesInfo.Defs[importSpec.Name]
		}
		pkgName, ok := obj.(*gotypes.PkgName)
		if !ok {
			skip("import was not resolved by the type checker")
			continue
		}

		current, target := pkgName.Name(), rule.Alias
		explicit := target != pkgName.Imported().Name()
		if current == target && explicit == (importSpec.Name != nil) {
			continue
		}

		var uses []*ast.Ident
		if current != target {
			ast.Inspect(file.AST, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && pkg.TypesInfo.Uses[id] == pkgName {
					uses = append(uses, id)
				}
				return true
			})
			taken := nameTakenAt(pkg.TypesPkg, pkgName, target, uses)
			if taken != nil {
				skip(fmt.Sprintf("name %q is already declared at %s", target, ws.FileSet.Position(taken.Pos())))
				continue
			}
		}

		switch {
		case importSpec.Name != nil && !explicit:
			start, end := offset(importSpec.Name.Pos()), offset(importSpec.Path.Pos())
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       start,
				End:         end,
				OldText:     string(file.OriginalContent[start:end]),
				Description: fmt.Sprintf("Remove import alias '%s' for %s", current, importPath),
			})
		case importSpec.Name != nil:
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       offset(importSpec.Name.Pos()),
				End:         offset(importSpec.Name.End()),
				OldText:     current,
				NewText:     target,
				Description: fmt.Sprintf("Standardize import alias for %s from '%s' to '%s'", importPath, current, target),
			})
		default:
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       offset(importSpec.Path.Pos()),
				End:         offset(importSpec.Path.Pos()),
				NewText:     target + " ",
				Description: fmt.Sprintf("Add import alias '%s' for package %s", target, importPath),
			})
		}
		for _, id := range uses {
			changes = append(changes, types.Change{
				File:        file.Path,
				Start:       offset(id.Pos()),
				End:         offset(id.End()),
				OldText:     id.Name,
				NewText:     target,
				Description: fmt.Sprintf("Rename %s to %s to match the import alias", id.Name, target),
			})
		}
	}

	return changes
}

func (op *StandardizeImportsOperation) ruleForImport(importPath string) *types.AliasRule {
	var rule *types.AliasRule
	for i := range op.Request.Rules {
		if op.matchesPattern(importPath, op.Request.Rules[i].PackagePattern) {
			rule = &op.Request.Rules[i]
			break
		}
	}
	return rule
}

// nameTakenAt returns the declaration that name would clash with at one of the
// uses of pkgName, looked up through the scopes of tp.
func nameTakenAt(tp *gotypes.Package, pkgName *gotypes.PkgName, name string, uses []*ast.Ident) gotypes.Object {
	for _, id := range uses {
		scope := tp.Scope().Innermost(id.Pos())
		if scope == nil {
			continue
		}
		_, obj := scope.LookupParent(name, id.Pos())
		if obj != nil && obj != pkgName {
			return obj
		}
	}
	return nil
}

func (op *StandardizeImportsOperation) matchesPattern(importPath, pattern string) bool {
	// Simple pattern matching - could be enhanced with glob patterns
	return strings.Contains(importPath, pattern) || importPath == pattern
}

// ResolveAliasConflictsOperation implements resolving import alias conflicts
type ResolveAliasConflictsOperation struct {
	Request types.ResolveAliasConflictsRequest
}

func (op *ResolveAliasConflictsOperation) Type() types.OperationType {
	return types.ResolveAliasConflictsOperation
}

func (op *ResolveAliasConflictsOperation) Description() string {
	return fmt.Sprintf("Resolve alias conflicts in workspace %s", op.Request.Workspace)
}

func (op *ResolveAliasConflictsOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	return nil
}

func (op *ResolveAliasConflictsOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Detect and resolve conflicts in each file
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			conflicts := op.detectConflicts(file)
			changes := op.resolveConflicts(file, conflicts)
			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *ResolveAliasConflictsOperation) detectConflicts(file *types.File) map[string][]string {
	conflicts := make(map[string][]string) // alias -> []importPaths

	if file.AST == nil {
		return conflicts
	}

	// Find all aliases and group by alias name
	aliases := make(map[string][]string)
	ast.Inspect(file.AST, func(n ast.Node) bool {
		importSpec, ok := n.(*ast.ImportSpec)
		if ok && importSpec.Name != nil {
			alias := importSpec.Name.Name
			if alias != "." && alias != "_" {
				importPath := strings.Trim(importSpec.Path.Value, `"`)
				aliases[alias] = append(aliases[alias], importPath)
			}
		}
		return true
	})

	// Find conflicts (same alias, different import paths)
	for alias, paths := range aliases {
		if len(paths) > 1 {
			conflicts[alias] = paths
		}
	}

	return conflicts
}

func (op *ResolveAliasConflictsOperation) resolveConflicts(file *types.File, conflicts map[string][]string) []types.Change {
	if file.AST == nil {
		return nil
	}

	// Build a lookup: (alias, importPath) → [start, end] byte offsets of the alias token
	type specKey struct{ alias, importPath string }
	specPositions := make(map[specKey][2]int)
	ast.Inspect(file.AST, func(n ast.Node) bool {
		importSpec, ok := n.(*ast.ImportSpec)
		if !ok || importSpec.Name == nil {
			return true
		}
		a := importSpec.Name.Name
		importPath := strings.Trim(importSpec.Path.Value, `"`)
		if a == "." || a == "_" {
			return true
		}
		specPositions[specKey{a, importPath}] = [2]int{
			int(importSpec.Name.Pos()),
			int(importSpec.Name.End()),
		}
		return true
	})

	var changes []types.Change
	for alias, paths := range conflicts {
		switch op.Request.Strategy {
		case types.UseFullNames:
			for _, importPath := range paths {
				pos, ok := specPositions[specKey{alias, importPath}]
				if !ok {
					continue
				}
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       pos[0],
					End:         pos[1],
					OldText:     alias,
					NewText:     "",
					Description: fmt.Sprintf("Remove conflicting alias '%s' for %s", alias, importPath),
				})
			}
		case types.UseShortestUnique, types.UseCustomAlias:
			for i, importPath := range paths {
				parts := strings.Split(importPath, "/")
				newAlias := fmt.Sprintf("%s%d", parts[len(parts)-1], i+1)
				pos, ok := specPositions[specKey{alias, importPath}]
				if !ok {
					continue
				}
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       pos[0],
					End:         pos[1],
					OldText:     alias,
					NewText:     newAlias,
					Description: fmt.Sprintf("Rename alias '%s' → '%s' for %s", alias, newAlias, importPath),
				})
			}
		}
	}
	return changes
}

// ConvertAliasesOperation implements converting between aliased and non-aliased imports
type ConvertAliasesOperation struct {
	Request types.ConvertAliasesRequest
}

func (op *ConvertAliasesOperation) Type() types.OperationType {
	return types.ConvertAliasesOperation
}

func (op *ConvertAliasesOperation) Description() string {
	if op.Request.ToFullNames {
		return fmt.Sprintf("Convert aliases to full package names in workspace %s", op.Request.Workspace)
	}
	return fmt.Sprintf("Convert full package names to aliases in workspace %s", op.Request.Workspace)
}

func (op *ConvertAliasesOperation) Validate(ws *types.Workspace) error {
	if op.Request.Workspace == "" {
		return fmt.Errorf("workspace path cannot be empty")
	}
	if !op.Request.ToFullNames && !op.Request.FromFullNames {
		return fmt.Errorf("must specify either ToFullNames or FromFullNames")
	}
	return nil
}

func (op *ConvertAliasesOperation) Execute(ws *types.Workspace) (*types.RefactoringPlan, error) {
	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       make([]types.Change, 0),
		AffectedFiles: make([]string, 0),
		Reversible:    true,
	}

	// Convert aliases in all files
	for _, pkg := range ws.Packages {
		for _, file := range pkg.Files {
			var changes []types.Change
			if op.Request.ToFullNames {
				changes = op.convertToFullNames(file)
			} else {
				changes = op.convertToAliases(file)
			}

			plan.Changes = append(plan.Changes, changes...)
			if len(changes) > 0 {
				plan.AffectedFiles = append(plan.AffectedFiles, file.Path)
			}
		}
	}

	return plan, nil
}

func (op *ConvertAliasesOperation) convertToFullNames(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Find all aliased imports and convert references to full names
	ast.Inspect(file.AST, func(n ast.Node) bool {
		importSpec, ok := n.(*ast.ImportSpec)
		if ok && importSpec.Name != nil {
			alias := importSpec.Name.Name
			if alias != "." && alias != "_" {
				importPath := strings.Trim(importSpec.Path.Value, `"`)

				// Remove the alias from import
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Name.Pos()),
					End:         int(importSpec.Name.End() + token.Pos(len(" "))), // Include space after alias
					OldText:     alias + " ",
					NewText:     "",
					Description: fmt.Sprintf("Remove alias '%s' for package %s", alias, importPath),
				})

				// Would need additional logic to find and replace all references
				// from alias.Symbol to packageName.Symbol
				// This is a complex operation that requires symbol resolution
			}
		}
		return true
	})

	return changes
}

func (op *ConvertAliasesOperation) convertToAliases(file *types.File) []types.Change {
	var changes []types.Change

	if file.AST == nil {
		return changes
	}

	// Find all non-aliased imports and add appropriate aliases
	ast.Inspect(file.AST, func(n ast.Node) bool {
		importSpec, ok := n.(*ast.ImportSpec)
		if ok && importSpec.Name == nil {
			importPath := strings.Trim(importSpec.Path.Value, `"`)
			packageName := filepath.Base(importPath)

			// Generate a reasonable alias
			alias := op.generateAlias(importPath)

			if alias != packageName {
				// Add alias to import
				changes = append(changes, types.Change{
					File:        file.Path,
					Start:       int(importSpec.Path.Pos()),
					End:         int(importSpec.Path.Pos()),
					OldText:     "",
					NewText:     alias + " ",
					Description: fmt.Sprintf("Add alias '%s' for package %s", alias, importPath),
				})
			}
		}
		return true
	})

	return changes
}

func (op *ConvertAliasesOperation) generateAlias(importPath string) string {
	// Simple alias generation - could be enhanced with more sophisticated rules
	parts := strings.Split(importPath, "/")
	packageName := parts[len(parts)-1]

	// For common patterns, generate reasonable aliases
	if strings.Contains(importPath, "github.com") && len(parts) >= 3 {
		return strings.ToLower(parts[len(parts)-1][:3]) // First 3 characters
	}

	return packageName
}
