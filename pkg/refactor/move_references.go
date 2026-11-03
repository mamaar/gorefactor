package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// fileReferences are the references to moved symbols within one file.
type fileReferences struct {
	file string
	refs []*types.Reference
}

// referenceFileEdit is how one referencing file must change so its references
// resolve to the moved symbols: the qualifier to spell them with and the
// import edits that make that qualifier valid.
type referenceFileEdit struct {
	qualifier     string
	importChanges []types.Change
}

func groupReferencesByFile(refs []*types.Reference) []fileReferences {
	var groups []fileReferences
	index := make(map[string]int)
	for _, ref := range refs {
		i, ok := index[ref.File]
		if !ok {
			i = len(groups)
			index[ref.File] = i
			groups = append(groups, fileReferences{file: ref.File})
		}
		groups[i].refs = append(groups[i].refs, ref)
	}
	return groups
}

func importSpecText(alias, path string) string {
	if alias != "" {
		return fmt.Sprintf("%s %q", alias, path)
	}
	return fmt.Sprintf("%q", path)
}

// referenceSource is the references in one file to symbols moved out of the
// package imported at importPath, whose own name is name.
type referenceSource struct {
	importPath string
	name       string
	refs       []*types.Reference
}

// planReferenceFile decides how filePath refers to the target package after
// the move. It reuses an existing import of the target (and its alias); else it
// imports the target under a name that collides with nothing in the file. A
// source import is dropped when the moved references were its last users.
// unused are imports the file loses its last use of for another reason; they
// are removed here so the edits to the file's imports do not overlap.
func planReferenceFile(ws *types.Workspace, filePath string, sources []referenceSource, unused []*ast.ImportSpec, targetImportPath, targetName string) referenceFileEdit {
	file := findParsedFile(ws, filePath)
	if file == nil || file.AST == nil {
		return referenceFileEdit{qualifier: targetName}
	}

	dropped := slices.Clone(unused)
	for _, src := range sources {
		spec := unusedSourceImport(file, src.refs, src.importPath, src.name)
		if spec != nil && !slices.Contains(dropped, spec) {
			dropped = append(dropped, spec)
		}
	}
	removeDropped := func(specs []*ast.ImportSpec) []types.Change {
		return removeImportSpecs(ws, file, specs)
	}

	for _, imp := range file.AST.Imports {
		if strings.Trim(imp.Path.Value, `"`) != targetImportPath {
			continue
		}
		qualifier := targetName
		if imp.Name != nil {
			qualifier, _ = importLocalName(imp)
		}
		if qualifier == "" {
			continue
		}
		// The rewritten references use this import, so it stays.
		return referenceFileEdit{qualifier: qualifier, importChanges: removeDropped(slices.DeleteFunc(dropped, func(s *ast.ImportSpec) bool { return s == imp }))}
	}

	alias := chooseImportName(ws, file, dropped, targetImportPath, targetName, nil)
	explicit := ""
	if alias != lastPathComponent(targetImportPath) {
		explicit = alias
	}

	edit := referenceFileEdit{qualifier: alias}
	if len(dropped) > 0 {
		edit.importChanges = append([]types.Change{replaceImportSpec(ws, file, dropped[0], explicit, targetImportPath)}, removeDropped(dropped[1:])...)
	} else {
		change := generateAddImportChange(ws, filePath, explicit, targetImportPath)
		if change != nil {
			edit.importChanges = []types.Change{*change}
		}
	}
	return edit
}

// unusedSourceImport returns the file's import of the source package, whose
// own name is sourceName, when the moved references are all of its uses, so it
// is unused once they are rewritten; otherwise nil. Blank and dot imports are
// never returned.
func unusedSourceImport(file *types.File, refs []*types.Reference, sourceImportPath, sourceName string) *ast.ImportSpec {
	for _, imp := range file.AST.Imports {
		if strings.Trim(imp.Path.Value, `"`) != sourceImportPath {
			continue
		}
		name := sourceName
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		moved := 0
		for _, ref := range refs {
			if qualifierBefore(file.OriginalContent, ref.Offset) == name {
				moved++
			}
		}
		if moved > 0 && moved == countQualifierUses(file.AST, name) {
			return imp
		}
		return nil
	}
	return nil
}

// qualifierBefore returns the identifier qualifying the name at offset
// ("model" for "model.Name"), or "" when the name is unqualified.
func qualifierBefore(content []byte, offset int) string {
	if offset < 2 || offset > len(content) || content[offset-1] != '.' {
		return ""
	}
	start := offset - 1
	for start > 0 && (isIdentChar(content[start-1])) {
		start--
	}
	return string(content[start : offset-1])
}

func countQualifierUses(f *ast.File, name string) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if ok {
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == name {
				n++
			}
		}
		return true
	})
	return n
}

// chooseImportName picks the local name for the target import: the package
// name if free in the file, else the package name prefixed with preceding path
// segments, else numbered. dropped imports are being removed and so do not
// occupy their names, and avoid lists names that are unavailable besides.
func chooseImportName(ws *types.Workspace, file *types.File, dropped []*ast.ImportSpec, targetImportPath, targetName string, avoid map[string]bool) string {
	taken := namesInFileScope(ws, file, dropped)
	maps.Copy(taken, avoid)

	segments := strings.Split(targetImportPath, "/")
	candidates := []string{targetName}
	suffix := targetName
	for i := len(segments) - 2; i >= 0; i-- {
		suffix = sanitizeIdent(segments[i]) + suffix
		candidates = append(candidates, suffix)
	}
	for _, c := range candidates {
		if isValidGoIdentifier(c) && !taken[c] {
			return c
		}
	}
	for n := 2; ; n++ {
		c := fmt.Sprintf("%s%d", targetName, n)
		if !taken[c] {
			return c
		}
	}
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if isIdentChar(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// namesInFileScope lists identifiers an import name in file could collide
// with: other imports' names, package-level declarations, and every identifier
// used in the file. Uses of a dropped import's qualifier are excluded, as that
// import disappears.
func namesInFileScope(ws *types.Workspace, file *types.File, dropped []*ast.ImportSpec) map[string]bool {
	taken := make(map[string]bool)
	dropNames := make(map[string]bool)
	for _, spec := range dropped {
		name, _ := importLocalName(spec)
		if name != "" {
			dropNames[name] = true
		}
	}
	if file.AST == nil {
		return taken
	}
	for _, imp := range file.AST.Imports {
		if slices.Contains(dropped, imp) {
			continue
		}
		name, _ := importLocalName(imp)
		if name != "" {
			taken[name] = true
		}
	}
	pkg := findPackageForFile(ws, file.Path)
	if pkg != nil && pkg.Symbols != nil {
		for _, group := range []map[string]*types.Symbol{pkg.Symbols.Types, pkg.Symbols.Functions, pkg.Symbols.Variables, pkg.Symbols.Constants} {
			for name := range group {
				taken[name] = true
			}
		}
	}
	ast.Inspect(file.AST, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.SelectorExpr:
			// Sel is a member name, not a scope identifier; only X can collide.
			id, ok := node.X.(*ast.Ident)
			if ok {
				if !dropNames[id.Name] {
					taken[id.Name] = true
				}
			} else {
				ast.Inspect(node.X, func(n ast.Node) bool {
					id, ok := n.(*ast.Ident)
					if ok {
						taken[id.Name] = true
					}
					return true
				})
			}
			return false
		case *ast.Ident:
			taken[node.Name] = true
		}
		return true
	})
	return taken
}

// replaceImportSpec swaps spec's line for an import of path under alias.
func replaceImportSpec(ws *types.Workspace, file *types.File, spec *ast.ImportSpec, alias, path string) types.Change {
	content := file.OriginalContent
	start, end := lineRange(ws, content, spec.Pos())
	old := string(content[start:end])
	text := "\t" + importSpecText(alias, path) + "\n"
	if strings.HasPrefix(strings.TrimSpace(old), "import ") {
		text = "import " + importSpecText(alias, path) + "\n"
	}
	return types.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     old,
		NewText:     text,
		Description: fmt.Sprintf("Replace import of %s with %s in %s", strings.Trim(spec.Path.Value, `"`), path, file.Path),
	}
}

// removeImportSpec deletes spec's line.
func removeImportSpec(ws *types.Workspace, file *types.File, spec *ast.ImportSpec) types.Change {
	content := file.OriginalContent
	start, end := lineRange(ws, content, spec.Pos())
	path := strings.Trim(spec.Path.Value, `"`)
	return types.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     string(content[start:end]),
		Description: fmt.Sprintf("Remove now-unused import %q from %s", path, file.Path),
	}
}

// removeImportSpecs deletes each of specs from file, taking a whole import
// declaration when none of its specs would remain so no empty `import ()` stays.
func removeImportSpecs(ws *types.Workspace, file *types.File, specs []*ast.ImportSpec) []types.Change {
	if len(specs) == 0 {
		return nil
	}
	var changes []types.Change
	for _, decl := range file.AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		var mine []*ast.ImportSpec
		for _, s := range gen.Specs {
			spec := s.(*ast.ImportSpec)
			if slices.Contains(specs, spec) {
				mine = append(mine, spec)
			}
		}
		if len(mine) < len(gen.Specs) {
			for _, spec := range mine {
				changes = append(changes, removeImportSpec(ws, file, spec))
			}
			continue
		}
		start, _ := lineRange(ws, file.OriginalContent, gen.Pos())
		_, end := lineRange(ws, file.OriginalContent, gen.End()-1)
		// Of the blank lines around the declaration, keep one.
		content := file.OriginalContent
		if start >= 2 && content[start-2] == '\n' && end < len(content) && content[end] == '\n' {
			end++
		}
		paths := make([]string, len(mine))
		for i, spec := range mine {
			paths[i] = spec.Path.Value
		}
		changes = append(changes, types.Change{
			File:        file.Path,
			Start:       start,
			End:         end,
			OldText:     string(file.OriginalContent[start:end]),
			Description: fmt.Sprintf("Remove now-unused import %s from %s", strings.Join(paths, ", "), file.Path),
		})
	}
	return changes
}
