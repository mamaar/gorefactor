package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
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
	comment string // trailing comments on the spec's line
	leading []commentBlock
}

// commentBlock is a comment group rendered on its own lines above a spec.
// blankBefore and blankAfter record the blank lines that separated it from its
// neighbours, so free-standing groups keep their spacing.
type commentBlock struct {
	lines       []string
	blankBefore bool
	blankAfter  bool
}

// OrganizeImports rewrites the import block(s) in src so that imports are
// grouped into stdlib / external / workspace / module sections separated by
// blank lines, with alphabetical sorting within each group.  On any error
// (parse failure, etc.) the original source is returned unchanged.
func OrganizeImports(src string, modulePath string, workspaceModules []string) string {
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

	var specs []*ast.ImportSpec
	attached := make(map[*ast.CommentGroup]bool)

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

			specs = append(specs, ispec)
			attached[ispec.Doc] = true
			attached[ispec.Comment] = true
		}
	}

	if importDeclCount == 0 || len(specs) == 0 {
		return src
	}

	lines := strings.Split(src, "\n")
	free := freeStandingComments(f, firstImportPos, lastImportEnd, attached)
	for _, ispec := range specs {
		entry := newImportEntry(ispec)
		entry.leading, free = takeLeadingBlocks(fset, lines, ispec, free)
		if entry.path == "C" {
			cgoEntries = append(cgoEntries, entry)
		} else {
			entries = append(entries, entry)
		}
	}
	tail := commentBlocks(fset, lines, free)

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
	newBlock := renderImportBlock(groups, cgoEntries, tail)

	// Replace the old import declarations with the new block.
	startOff := fset.Position(firstImportPos).Offset
	endOff := fset.Position(lastImportEnd).Offset

	if startOff < 0 || endOff < 0 || startOff > len(src) || endOff > len(src) {
		return src
	}

	result := src[:startOff] + newBlock + src[endOff:]
	return result
}

// newImportEntry returns the sortable entry for spec, without leading comments.
func newImportEntry(spec *ast.ImportSpec) importEntry {
	// The parser accepted the literal, so it unquotes; raw strings included.
	path, _ := strconv.Unquote(spec.Path.Value)
	e := importEntry{path: path}
	if spec.Name != nil {
		e.alias = spec.Name.Name
	}
	if spec.Comment != nil {
		texts := make([]string, len(spec.Comment.List))
		for i, c := range spec.Comment.List {
			texts[i] = c.Text
		}
		e.comment = strings.Join(texts, " ")
	}
	return e
}

// freeStandingComments returns the comment groups inside [start, end] that are
// not a spec's doc or trailing comment.
func freeStandingComments(f *ast.File, start, end token.Pos, attached map[*ast.CommentGroup]bool) []*ast.CommentGroup {
	var free []*ast.CommentGroup
	for _, g := range f.Comments {
		if g.Pos() >= start && g.End() <= end && !attached[g] {
			free = append(free, g)
		}
	}
	return free
}

// takeLeadingBlocks splits off the free-standing groups that precede spec and
// returns them followed by spec's own doc comment, plus the groups left over.
func takeLeadingBlocks(fset *token.FileSet, lines []string, spec *ast.ImportSpec, free []*ast.CommentGroup) ([]commentBlock, []*ast.CommentGroup) {
	n := 0
	for n < len(free) && free[n].End() <= spec.Pos() {
		n++
	}
	blocks := commentBlocks(fset, lines, free[:n])
	if spec.Doc != nil {
		doc := commentBlocks(fset, lines, []*ast.CommentGroup{spec.Doc})[0]
		doc.blankAfter = false
		blocks = append(blocks, doc)
	}
	return blocks, free[n:]
}

// commentBlocks converts comment groups to blocks, recording from the source
// lines whether a blank line sat directly before or after each group.
func commentBlocks(fset *token.FileSet, lines []string, groups []*ast.CommentGroup) []commentBlock {
	blocks := make([]commentBlock, len(groups))
	for i, g := range groups {
		for _, c := range g.List {
			blocks[i].lines = append(blocks[i].lines, c.Text)
		}
		first := fset.Position(g.Pos()).Line
		last := fset.Position(g.End()).Line
		blocks[i].blankBefore = isBlankLine(lines, first-1)
		blocks[i].blankAfter = isBlankLine(lines, last+1)
	}
	return blocks
}

// isBlankLine reports whether the 1-based line n exists and is blank.
func isBlankLine(lines []string, n int) bool {
	return n >= 1 && n <= len(lines) && strings.TrimSpace(lines[n-1]) == ""
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
	if before, _, ok := strings.Cut(importPath, "/"); ok {
		firstSeg = before
	}
	if !strings.Contains(firstSeg, ".") {
		return ImportGroupStdlib
	}

	return ImportGroupExternal
}

// renderImportBlock produces the text for a complete import(...) declaration
// with groups separated by blank lines.  cgoEntries (import "C") are placed
// first in their own group; tail holds comments that followed the last spec.
func renderImportBlock(groups map[ImportGroup][]importEntry, cgoEntries []importEntry, tail []commentBlock) string {
	var b strings.Builder
	b.WriteString("import (\n")

	groupOrder := []ImportGroup{ImportGroupStdlib, ImportGroupExternal, ImportGroupWorkspace, ImportGroupModule}
	wroteGroup := false

	// Cgo imports come first, separated from everything else.
	if len(cgoEntries) > 0 {
		writeImportGroup(&b, cgoEntries)
		wroteGroup = true
	}

	for _, g := range groupOrder {
		entries := groups[g]
		if len(entries) == 0 {
			continue
		}

		if wroteGroup {
			b.WriteString("\n")
		}
		writeImportGroup(&b, entries)
		wroteGroup = true
	}

	for _, blk := range tail {
		writeCommentBlock(&b, blk)
	}

	b.WriteString(")")
	return b.String()
}

// writeImportGroup writes the entries of one group, one per line.
func writeImportGroup(b *strings.Builder, entries []importEntry) {
	for _, e := range entries {
		for _, blk := range e.leading {
			writeCommentBlock(b, blk)
		}
		b.WriteString("\t")
		writeImportLine(b, e)
		b.WriteString("\n")
	}
}

// writeCommentBlock writes blk on its own lines, keeping the blank lines that
// surrounded it unless the output already has one there.
func writeCommentBlock(b *strings.Builder, blk commentBlock) {
	if blk.blankBefore {
		writeBlankLine(b)
	}
	for _, l := range blk.lines {
		b.WriteString("\t")
		b.WriteString(l)
		b.WriteString("\n")
	}
	if blk.blankAfter {
		writeBlankLine(b)
	}
}

// writeBlankLine ends the current line with an empty one, unless the output
// already ends in a blank line or the opening "import (".
func writeBlankLine(b *strings.Builder) {
	out := b.String()
	if strings.HasSuffix(out, "\n\n") || strings.HasSuffix(out, "(\n") {
		return
	}
	b.WriteString("\n")
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
