package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// ImportGroup represents the category of an import for ordering purposes.
type ImportGroup int

const (
	ImportGroupStdlib    ImportGroup = iota // stdlib: no dots in first path segment
	ImportGroupExternal                     // external: has dots, not module or workspace
	ImportGroupWorkspace                    // workspace: matches a go.work sibling module
	ImportGroupModule                       // module: matches the current module path
)

// importEntry holds one import spec's data for sorting and rendering.
type importEntry struct {
	alias   string // "", ".", "_", or a named alias
	path    string // the quoted import path (without quotes)
	comment string // any trailing inline comment
}

// organizeImports rewrites the import block(s) in src so that imports are
// grouped into stdlib / external / workspace / module sections separated by
// blank lines, with alphabetical sorting within each group.  On any error
// (parse failure, etc.) the original source is returned unchanged.
func organizeImports(src string, modulePath string, workspaceModules []string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return src
	}

	// Collect all import specs across all import decls.
	var entries []importEntry
	var cgoEntries []importEntry // import "C" blocks stay separate

	// Track the byte range of all import decls so we can replace them.
	var firstImportPos token.Pos
	var lastImportEnd token.Pos
	importDeclCount := 0

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.IMPORT {
			continue
		}
		importDeclCount++

		pos := genDecl.Pos()
		end := genDecl.End()

		if firstImportPos == 0 || pos < firstImportPos {
			firstImportPos = pos
		}
		if end > lastImportEnd {
			lastImportEnd = end
		}

		for _, spec := range genDecl.Specs {
			ispec, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}

			path := strings.Trim(ispec.Path.Value, `"`)
			alias := ""
			if ispec.Name != nil {
				alias = ispec.Name.Name
			}

			comment := ""
			if ispec.Comment != nil {
				for _, c := range ispec.Comment.List {
					comment = c.Text
				}
			}

			entry := importEntry{alias: alias, path: path, comment: comment}

			if path == "C" {
				cgoEntries = append(cgoEntries, entry)
			} else {
				entries = append(entries, entry)
			}
		}
	}

	if importDeclCount == 0 || len(entries) == 0 && len(cgoEntries) == 0 {
		return src
	}

	// Classify and group.
	groups := make(map[ImportGroup][]importEntry)
	for _, e := range entries {
		g := classifyImport(e.path, modulePath, workspaceModules)
		groups[g] = append(groups[g], e)
	}

	// Sort within each group.
	for g := range groups {
		sort.Slice(groups[g], func(i, j int) bool {
			return groups[g][i].path < groups[g][j].path
		})
	}

	// Render the new import block.
	newBlock := renderImportBlock(groups, cgoEntries)

	// Replace the old import declarations with the new block.
	startOff := fset.Position(firstImportPos).Offset
	endOff := fset.Position(lastImportEnd).Offset

	if startOff < 0 || endOff < 0 || startOff > len(src) || endOff > len(src) {
		return src
	}

	result := src[:startOff] + newBlock + src[endOff:]
	return result
}

// classifyImport determines which group an import path belongs to.
func classifyImport(importPath, modulePath string, workspaceModules []string) ImportGroup {
	// Module match: import path starts with the module path.
	if modulePath != "" && (importPath == modulePath || strings.HasPrefix(importPath, modulePath+"/")) {
		return ImportGroupModule
	}

	// Workspace match: import path starts with any workspace module path.
	for _, wm := range workspaceModules {
		if wm != "" && (importPath == wm || strings.HasPrefix(importPath, wm+"/")) {
			return ImportGroupWorkspace
		}
	}

	// Stdlib: first path segment has no dot.
	firstSeg := importPath
	if idx := strings.Index(importPath, "/"); idx >= 0 {
		firstSeg = importPath[:idx]
	}
	if !strings.Contains(firstSeg, ".") {
		return ImportGroupStdlib
	}

	return ImportGroupExternal
}

// renderImportBlock produces the text for a complete import(...) declaration
// with groups separated by blank lines.  cgoEntries (import "C") are placed
// first in their own group.
func renderImportBlock(groups map[ImportGroup][]importEntry, cgoEntries []importEntry) string {
	var b strings.Builder
	b.WriteString("import (\n")

	groupOrder := []ImportGroup{ImportGroupStdlib, ImportGroupExternal, ImportGroupWorkspace, ImportGroupModule}
	wroteGroup := false

	// Cgo imports come first, separated from everything else.
	if len(cgoEntries) > 0 {
		for _, e := range cgoEntries {
			b.WriteString("\t")
			writeImportLine(&b, e)
			b.WriteString("\n")
		}
		wroteGroup = true
	}

	for _, g := range groupOrder {
		entries, ok := groups[g]
		if !ok || len(entries) == 0 {
			continue
		}

		if wroteGroup {
			b.WriteString("\n")
		}

		for _, e := range entries {
			b.WriteString("\t")
			writeImportLine(&b, e)
			b.WriteString("\n")
		}
		wroteGroup = true
	}

	b.WriteString(")")
	return b.String()
}

// writeImportLine writes a single import line (alias + path + optional comment).
func writeImportLine(b *strings.Builder, e importEntry) {
	if e.alias != "" {
		b.WriteString(e.alias)
		b.WriteString(" ")
	}
	b.WriteString(`"`)
	b.WriteString(e.path)
	b.WriteString(`"`)
	if e.comment != "" {
		b.WriteString(" ")
		b.WriteString(e.comment)
	}
}
