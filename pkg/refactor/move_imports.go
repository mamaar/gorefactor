package refactor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// This file computes the import block a moved declaration needs in its new
// home file. Previously move_symbol copied only the raw declaration text
// (see generateSymbolAdditionChange in operations.go) and never looked at
// what identifiers that text actually referenced, so any moved code that
// used a package-qualified name (http.Client, sync.RWMutex, an aliased
// third-party import, ...) landed in the target file with zero imports and
// failed to compile.

// neededImport describes one import the moved code requires in its new file.
type neededImport struct {
	alias string // "" unless the source file used an explicit alias
	path  string
}

// importLocalName returns the identifier local Go code uses to refer to imp
// (e.g. "http" for `"net/http"`, or the explicit alias if one is given), and
// whether that name is an explicit alias (as opposed to derived from the
// import path). Blank (`_`) and dot (`.`) imports return ("", false) since
// neither produces a selector-qualified reference we can detect.
func importLocalName(imp *ast.ImportSpec) (string, bool) {
	if imp.Name != nil {
		switch imp.Name.Name {
		case "_", ".":
			return "", false
		default:
			return imp.Name.Name, true
		}
	}

	// Default local name: the last path segment. This is a simplification —
	// a minority of packages declare a different package name than their
	// import path's last segment (e.g. "gopkg.in/yaml.v2" -> "yaml") — but it
	// matches the convention this tool already relies on elsewhere
	// (packagePathToImportPath, lastPathComponent), and is right for the
	// overwhelming majority of stdlib and third-party packages.
	path := strings.Trim(imp.Path.Value, `"`)
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:], false
	}
	return path, false
}

// collectPackageSelectorNames parses code as a standalone file body and
// returns the set of identifiers used as the package-qualifier half of a
// selector expression (the "http" in "http.Client"). Parsing standalone
// (rather than reusing AST nodes from the original file) means this works
// regardless of how code was produced — AST-based extraction or the
// line/brace-counting fallback in extractSymbolSource.
//
// Returns (nil, err) only if code doesn't parse even as a loose statement
// list; callers should treat that as "don't know, skip import inference"
// rather than failing the whole move.
func collectPackageSelectorNames(code string) (map[string]bool, error) {
	fset := token.NewFileSet()
	synthetic, err := parser.ParseFile(fset, "", "package movedsymbols\n"+code, 0)
	if err != nil {
		return nil, err
	}

	names := make(map[string]bool)
	ast.Inspect(synthetic, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok {
			names[ident.Name] = true
		}
		return true
	})
	return names, nil
}

// unqualifySelfReferences rewrites code — a moved declaration's source text,
// still carrying whatever package qualifiers it had back in sourceFile — so
// that any reference to targetImportPath (the package code is being moved
// INTO) is left bare instead of self-qualified.
//
// This matters when a move lands a symbol in a package that already
// contains something the symbol's body refers to — typically because an
// earlier move already relocated that something there. Example: move #1
// relocates AsyncServer to pkg/asyncserver; since NewLoggedAsyncServer still
// lived in the old package at that point and called NewAsyncServer, move #1
// correctly qualified that call as asyncserver.NewAsyncServer and added the
// asyncserver import to the old package. Move #2 then relocates
// NewLoggedAsyncServer itself to pkg/asyncserver — but extractSymbolSource
// copies its body verbatim, qualifier and all, so pasting it into
// pkg/asyncserver produces a package that calls asyncserver.NewAsyncServer
// (illegal self-qualification) and, via resolveNeededImports below spotting
// that same qualifier, imports itself (an import cycle). Stripping the
// qualifier here — before resolveNeededImports ever sees the code — fixes
// both symptoms in one place.
//
// Returns code unchanged if sourceFile has no import of targetImportPath, or
// code doesn't parse as a standalone declaration; the latter is the same
// "don't know, don't touch it" fallback collectPackageSelectorNames uses.
func unqualifySelfReferences(sourceFile *types.File, code string, targetImportPath string) string {
	if sourceFile == nil || sourceFile.AST == nil || targetImportPath == "" {
		return code
	}

	// Find the local alias sourceFile used to refer to targetImportPath, if
	// any. Nothing to do if the source code never imported the destination
	// package in the first place.
	alias := ""
	for _, imp := range sourceFile.AST.Imports {
		if strings.Trim(imp.Path.Value, `"`) != targetImportPath {
			continue
		}
		localName, _ := importLocalName(imp)
		if localName != "" {
			alias = localName
		}
		break
	}
	if alias == "" {
		return code
	}

	const prefix = "package movedsymbols\n"
	fset := token.NewFileSet()
	synthetic, err := parser.ParseFile(fset, "", prefix+code, 0)
	if err != nil {
		return code
	}

	// Collect the byte ranges of "alias." prefixes to delete, keyed off the
	// synthetic file's own offsets (which are prefix-relative, hence the
	// -len(prefix) adjustment back to code's coordinate space).
	type span struct{ start, end int }
	var removals []span
	ast.Inspect(synthetic, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != alias {
			return true
		}
		start := fset.Position(sel.X.Pos()).Offset - len(prefix)
		end := fset.Position(sel.Sel.Pos()).Offset - len(prefix)
		if start < 0 || end < start || end > len(code) {
			return true // out of bounds shouldn't happen, but don't corrupt code if it does
		}
		removals = append(removals, span{start, end})
		return true
	})
	if len(removals) == 0 {
		return code
	}

	sort.Slice(removals, func(i, j int) bool { return removals[i].start > removals[j].start })
	result := code
	for _, r := range removals {
		result = result[:r.start] + result[r.end:]
	}
	return result
}

// resolveNeededImports maps the package-qualifier identifiers used in code
// back to the import specs that provide them in sourceFile, carrying over
// any explicit alias. Imports that can't be resolved (used name isn't any of
// sourceFile's import local names — e.g. the identifier turned out to be
// something else entirely) are simply omitted; this is best-effort inference,
// not authoritative.
func resolveNeededImports(sourceFile *types.File, code string) []neededImport {
	if sourceFile == nil || sourceFile.AST == nil {
		return nil
	}

	usedNames, err := collectPackageSelectorNames(code)
	if err != nil || len(usedNames) == 0 {
		return nil
	}

	var needed []neededImport
	seenPaths := make(map[string]bool)
	for _, imp := range sourceFile.AST.Imports {
		localName, explicit := importLocalName(imp)
		if localName == "" || !usedNames[localName] {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true

		alias := ""
		if explicit {
			alias = localName
		}
		needed = append(needed, neededImport{alias: alias, path: path})
	}
	return needed
}

// packageClauseEndOffset returns the byte offset in targetFile.OriginalContent
// immediately after the package clause's own line, i.e. where a new
// top-of-file import block should be inserted.
func packageClauseEndOffset(ws *types.Workspace, targetFile *types.File) int {
	content := targetFile.OriginalContent

	if targetFile.AST == nil || targetFile.AST.Name == nil || ws.FileSet == nil {
		// Fallback: assume the file starts with "package <name>\n".
		if idx := strings.IndexByte(string(content), '\n'); idx >= 0 {
			return idx + 1
		}
		return len(content)
	}

	pos := ws.FileSet.Position(targetFile.AST.Name.End())

	offset := 0
	line := 1
	for offset < len(content) && line < pos.Line {
		if content[offset] == '\n' {
			line++
		}
		offset++
	}
	for offset < len(content) && content[offset] != '\n' {
		offset++
	}
	if offset < len(content) {
		offset++ // move past the newline itself
	}
	return offset
}

// generateNeededImportsChange returns a Change that adds everything
// neededImports lists (minus whatever targetFile already imports) to
// targetFile. Returns nil if nothing needs to be added.
//
// When targetFile already has a parenthesized `import (...)` block, the new
// lines are inserted into that existing block rather than appended as a
// second, separate import declaration. A second block would be legal Go —
// the serializer runs OrganizeImports over every touched .go file after
// changes are applied (see serializer.go), which collapses multiple import
// declarations into one grouped, sorted block — but that pass only runs at
// apply time. A dry-run preview shows the change exactly as generated here,
// so a caller reviewing the diff would see two adjacent blocks for no
// visible reason. Merging into the existing block up front keeps the
// preview itself gofmt/goimports-stable, not just the eventually-applied
// file.
//
// Single-line `import "x"` form (no parentheses) falls through to the
// new-block path below instead of being converted in place; that mirrors
// generateAddImportChange's single-line handling being a separate code path
// rather than something this helper duplicates.
func generateNeededImportsChange(ws *types.Workspace, targetFile *types.File, needed []neededImport) *types.Change {
	if len(needed) == 0 {
		return nil
	}

	existing := make(map[string]bool)
	if targetFile.AST != nil {
		for _, imp := range targetFile.AST.Imports {
			existing[strings.Trim(imp.Path.Value, `"`)] = true
		}
	}

	var lines []string
	for _, n := range needed {
		if existing[n.path] {
			continue
		}
		if n.alias != "" {
			lines = append(lines, fmt.Sprintf("\t%s %q", n.alias, n.path))
		} else {
			lines = append(lines, fmt.Sprintf("\t%q", n.path))
		}
	}
	if len(lines) == 0 {
		return nil
	}

	if targetFile.AST != nil && ws.FileSet != nil && len(targetFile.AST.Imports) > 0 &&
		!isSingleLineImport(targetFile.AST, targetFile.OriginalContent) {
		lastImport := targetFile.AST.Imports[len(targetFile.AST.Imports)-1]
		_, insertAt := lineRange(ws, targetFile.OriginalContent, lastImport.End())
		return &types.Change{
			File:        targetFile.Path,
			Start:       insertAt,
			End:         insertAt,
			OldText:     "",
			NewText:     strings.Join(lines, "\n") + "\n",
			Description: fmt.Sprintf("Add imports to %s", targetFile.Path),
		}
	}

	offset := packageClauseEndOffset(ws, targetFile)
	return &types.Change{
		File:        targetFile.Path,
		Start:       offset,
		End:         offset,
		OldText:     "",
		NewText:     "\nimport (\n" + strings.Join(lines, "\n") + "\n)\n",
		Description: fmt.Sprintf("Add imports to %s", targetFile.Path),
	}
}

// applyChangesInMemory applies changes to content and returns the result,
// without touching disk. Mirrors serializer.applyChangesToFile's approach
// (apply in descending Start order so earlier offsets stay valid) but is
// used here purely to preview what a file will look like post-removal, so
// generateUnusedImportRemovalChanges can tell which of its imports are still
// needed.
func applyChangesInMemory(content []byte, changes []types.Change) []byte {
	sorted := make([]types.Change, len(changes))
	copy(sorted, changes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start > sorted[j].Start })

	result := string(content)
	for _, c := range sorted {
		if c.Start < 0 || c.End > len(result) || c.Start > c.End {
			continue // leave malformed changes for the real serializer to report
		}
		result = result[:c.Start] + c.NewText + result[c.End:]
	}
	return []byte(result)
}

// generateUnusedImportRemovalChanges returns changes that delete any import
// in sourceFile that removeChanges (the declarations being moved out) leaves
// with no remaining references. Go treats an unused import as a hard
// compile error, so a source file that loses the only code using one of its
// imports must have that import removed too, or it stops compiling — the
// same class of bug as the moved-to file missing imports it needs, just on
// the source side.
//
// Blank (`_`) and dot (`.`) imports are never removed: they're kept for
// side effects a plain identifier search can't see.
func generateUnusedImportRemovalChanges(ws *types.Workspace, sourceFile *types.File, removeChanges []types.Change) ([]types.Change, error) {
	if sourceFile == nil || sourceFile.AST == nil || len(removeChanges) == 0 || ws.FileSet == nil {
		return nil, nil
	}

	remaining := applyChangesInMemory(sourceFile.OriginalContent, removeChanges)

	remainingAST, err := parser.ParseFile(token.NewFileSet(), sourceFile.Path, remaining, 0)
	if err != nil {
		// If the post-removal content doesn't parse cleanly, don't try to be
		// clever about pruning imports — leave it for compilation to report.
		return nil, nil
	}

	usedNames := make(map[string]bool)
	ast.Inspect(remainingAST, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok {
			usedNames[ident.Name] = true
		}
		return true
	})

	content := sourceFile.OriginalContent
	var changes []types.Change
	for _, imp := range sourceFile.AST.Imports {
		localName, _ := importLocalName(imp)
		if localName == "" || usedNames[localName] {
			continue // blank/dot import, or still referenced elsewhere
		}

		lineStart, lineEnd := lineRange(ws, content, imp.Pos())
		changes = append(changes, types.Change{
			File:        sourceFile.Path,
			Start:       lineStart,
			End:         lineEnd,
			OldText:     string(content[lineStart:lineEnd]),
			NewText:     "",
			Description: fmt.Sprintf("Remove now-unused import %q from %s", strings.Trim(imp.Path.Value, `"`), sourceFile.Path),
		})
	}
	return changes, nil
}

// lineRange returns the [start, end) byte offsets of the whole line
// containing pos, end exclusive of the trailing newline's own next-line
// start (i.e. it includes the newline, so deleting [start, end) removes the
// entire line cleanly).
func lineRange(ws *types.Workspace, content []byte, pos token.Pos) (int, int) {
	line := ws.FileSet.Position(pos).Line

	offset := 0
	curLine := 1
	for offset < len(content) && curLine < line {
		if content[offset] == '\n' {
			curLine++
		}
		offset++
	}
	start := offset
	for offset < len(content) && content[offset] != '\n' {
		offset++
	}
	if offset < len(content) {
		offset++ // include the newline itself
	}
	return start, offset
}
