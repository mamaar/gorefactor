package refactor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// This file decides the imports a moved declaration needs in its new home
// file and the qualifiers its code uses for them.

// neededImport describes one import the moved code requires in its new file.
type neededImport struct {
	alias string // "" when the import is under the package's own name
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
	idx := strings.LastIndex(path, "/")
	if idx >= 0 {
		return path[idx+1:], false
	}
	return path, false
}

// chooseImportNames decides, once for the whole target file, the local name of
// every package the moved code refers to through a qualifier, plus the source
// packages whose left-behind names it qualifies. A name is, in order: the
// target's own named import of the path, the name the source used, or a fresh
// one; each only when nothing else in the target file, the moved code or an
// earlier choice claims it. It returns the name per import path and the
// imports to add.
func chooseImportNames(ws *types.Workspace, targetFile *types.File, moving []*movingSymbol, uses map[*movingSymbol]*movedUses, targetImportPath string) (map[string]string, []neededImport) {
	type want struct {
		path, realName, preferred string
		inserted                  bool
		used                      []string
	}
	var wants []*want
	byPath := make(map[string]*want)
	get := func(path, realName string) *want {
		w, ok := byPath[path]
		if ok {
			return w
		}
		w = &want{path: path, realName: realName}
		byPath[path] = w
		wants = append(wants, w)
		return w
	}
	avoid := make(map[string]bool)
	shadow := make(map[string]bool)
	for _, m := range moving {
		if m.typ == nil {
			avoid[m.sym.Name] = true
		}
		maps.Copy(shadow, uses[m].idents)
		if len(uses[m].leftBehind) > 0 {
			get(packagePathToImportPath(ws, m.pkg.Path), m.pkg.Name).inserted = true
		}
		for _, u := range uses[m].pkgUses {
			if u.path == targetImportPath {
				continue
			}
			w := get(u.path, u.pkgName)
			if w.preferred == "" {
				w.preferred = u.name
			}
			w.used = append(w.used, u.name)
		}
	}

	taken := namesInFileScope(ws, targetFile, nil)
	chosen := make(map[string]bool)
	// fits reports whether w may go by name: unclaimed, and not shadowed by an
	// identifier of the moved code unless every qualifier already says name.
	fits := func(w *want, name string) bool {
		if chosen[name] || avoid[name] {
			return false
		}
		return !shadow[name] || !w.inserted && !slices.ContainsFunc(w.used, func(u string) bool { return u != name })
	}

	names := make(map[string]string, len(wants))
	var needed []neededImport
	for _, w := range wants {
		name := ""
		if targetFile.AST != nil {
			for _, imp := range targetFile.AST.Imports {
				if strings.Trim(imp.Path.Value, `"`) != w.path {
					continue
				}
				existing := w.realName
				if imp.Name != nil {
					existing, _ = importLocalName(imp)
				}
				if existing != "" && fits(w, existing) {
					name = existing
					break
				}
			}
		}
		if name == "" && w.preferred != "" && !taken[w.preferred] && fits(w, w.preferred) {
			name = w.preferred
		}
		if name == "" {
			blocked := maps.Clone(chosen)
			maps.Copy(blocked, avoid)
			maps.Copy(blocked, shadow)
			name = chooseImportName(ws, targetFile, nil, w.path, w.realName, blocked)
		}
		chosen[name] = true
		names[w.path] = name
		imp := neededImport{path: w.path}
		if name != w.realName {
			imp.alias = name
		}
		needed = append(needed, imp)
	}
	return names, needed
}

// qualifierEdits returns the edits that make m's code use the chosen import
// names: a use of the target package loses its qualifier, any other qualifier
// is renamed when the name differs, and each left-behind name gets the source
// package's qualifier.
func qualifierEdits(ws *types.Workspace, m *movingSymbol, u *movedUses, names map[string]string, targetImportPath string) []textEdit {
	content := m.file.OriginalContent
	var edits []textEdit
	for _, use := range u.pkgUses {
		end := use.offset + use.length
		switch {
		case use.path == targetImportPath:
			for end < len(content) && (content[end] == ' ' || content[end] == '\t') {
				end++
			}
			if end < len(content) && content[end] == '.' {
				end++
			}
			edits = append(edits, textEdit{start: use.offset, end: end})
		case names[use.path] != use.name:
			edits = append(edits, textEdit{start: use.offset, end: end, text: names[use.path]})
		}
	}
	if len(u.leftBehind) > 0 {
		qualifier := names[packagePathToImportPath(ws, m.pkg.Path)] + "."
		for _, offset := range u.leftBehind {
			edits = append(edits, textEdit{start: offset, end: offset, text: qualifier})
		}
	}
	return edits
}

// packageClauseEndOffset returns the byte offset in targetFile.OriginalContent
// immediately after the package clause's own line, i.e. where a new
// top-of-file import block should be inserted.
func packageClauseEndOffset(ws *types.Workspace, targetFile *types.File) int {
	content := targetFile.OriginalContent

	if targetFile.AST == nil || targetFile.AST.Name == nil || ws.FileSet == nil {
		// Fallback: assume the file starts with "package <name>\n".
		idx := bytes.IndexByte(content, '\n')
		if idx >= 0 {
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
// GenerateAddImportChange's single-line handling being a separate code path
// rather than something this helper duplicates.
func generateNeededImportsChange(ws *types.Workspace, targetFile *types.File, needed []neededImport) *types.Change {
	if len(needed) == 0 {
		return nil
	}

	var lines []string
	for _, n := range needed {
		if importsUnder(targetFile, n) {
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

// importsUnder reports whether file imports n's path under n's name; a blank
// or dot import does not count.
func importsUnder(file *types.File, n neededImport) bool {
	if file.AST == nil {
		return false
	}
	return slices.ContainsFunc(file.AST.Imports, func(imp *ast.ImportSpec) bool {
		if strings.Trim(imp.Path.Value, `"`) != n.path {
			return false
		}
		if imp.Name == nil {
			return n.alias == ""
		}
		return imp.Name.Name == n.alias
	})
}

// applyChangesInMemory applies changes to content and returns the result,
// without touching disk. Mirrors Serializer.ApplyChangesToContent's approach
// (apply in descending Start order so earlier offsets stay valid) but is
// used here purely to preview what a file will look like post-removal, so
// unusedImportsAfterRemoval can tell which of its imports are still
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

// emptiedFileDeletion returns the change deleting file when applying removals
// leaves it with nothing but a package clause and plain imports. A file with
// any comment (package doc, build constraint, //go: directive), a blank or "C"
// import stays: those say something on their own.
func emptiedFileDeletion(file *types.File, removals []types.Change) (types.Change, bool) {
	rest := applyChangesInMemory(file.OriginalContent, removals)
	parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, rest, parser.ParseComments)
	if err != nil || len(parsed.Comments) > 0 {
		return types.Change{}, false
	}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			return types.Change{}, false
		}
	}
	for _, imp := range parsed.Imports {
		if imp.Name != nil && imp.Name.Name == "_" || imp.Path.Value == `"C"` {
			return types.Change{}, false
		}
	}
	return types.Change{File: file.Path, Delete: true, Description: fmt.Sprintf("Remove file %s: no declarations left", file.Path)}, true
}

// unusedImportsAfterRemoval returns the imports of sourceFile that nothing
// uses once removeChanges (the ranges being deleted) are applied. An
// unused import is a compile error, so the source file must drop them.
//
// A type-checked file is judged by its package-name uses outside the removed
// ranges. Without type info only an explicitly named import can be judged, by
// its alias; an unnamed one is kept, as its name is a guess.
//
// Blank (`_`) and dot (`.`) imports are never returned: they're kept for
// side effects a plain identifier search can't see.
func unusedImportsAfterRemoval(ws *types.Workspace, sourceFile *types.File, removeChanges []types.Change) []*ast.ImportSpec {
	if sourceFile == nil || sourceFile.AST == nil || len(removeChanges) == 0 || ws.FileSet == nil {
		return nil
	}

	// remaining reports whether pos lies in sourceFile outside the removed ranges.
	remaining := func(pos token.Pos) bool {
		at := ws.FileSet.Position(pos)
		return at.Filename == sourceFile.Path && !slices.ContainsFunc(removeChanges, func(c types.Change) bool {
			return c.File == at.Filename && at.Offset >= c.Start && at.Offset < c.End
		})
	}
	var stillUsed func(imp *ast.ImportSpec) bool
	info := typeInfoOf(ws, sourceFile)
	if info != nil {
		stillUsed = func(imp *ast.ImportSpec) bool {
			path := strings.Trim(imp.Path.Value, `"`)
			for ident, obj := range info.Uses {
				pkgName, ok := obj.(*gotypes.PkgName)
				if !ok || pkgName.Imported().Path() != path || !remaining(ident.Pos()) {
					continue
				}
				if imp.Name == nil && pkgName.Name() == pkgName.Imported().Name() || imp.Name != nil && pkgName.Name() == imp.Name.Name {
					return true
				}
			}
			return false
		}
	} else {
		stillUsed = func(imp *ast.ImportSpec) bool {
			if imp.Name == nil {
				return true
			}
			used := false
			ast.Inspect(sourceFile.AST, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if ok {
					id, ok := sel.X.(*ast.Ident)
					if ok && id.Name == imp.Name.Name && remaining(id.Pos()) {
						used = true
					}
				}
				return !used
			})
			return used
		}
	}

	var unused []*ast.ImportSpec
	for _, imp := range sourceFile.AST.Imports {
		if imp.Name != nil && (imp.Name.Name == "_" || imp.Name.Name == ".") || stillUsed(imp) {
			continue
		}
		unused = append(unused, imp)
	}
	return unused
}

// typeInfoOf returns the type info of file's package when file was
// type-checked with it, else nil.
func typeInfoOf(ws *types.Workspace, file *types.File) *gotypes.Info {
	pkg := findPackageForFile(ws, file.Path)
	if pkg == nil || pkg.TypesInfo == nil {
		return nil
	}
	_, ok := pkg.TypesInfo.Defs[file.AST.Name]
	if !ok {
		return nil
	}
	return pkg.TypesInfo
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
