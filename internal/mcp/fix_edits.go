package mcp

import (
	"bytes"
	"go/ast"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/types"
)

// blankLinesAfter matches the rest of a line and the blank lines below it.
var blankLinesAfter = regexp.MustCompile(`^[ \t]*\r?\n(?:[ \t]*\r?\n)*`)

// unusedImport matches the go/types message for an import left unused.
var unusedImport = regexp.MustCompile(`^(.+?):\d+:\d+: "(.+)" imported( as \S+)? and not used$`)

type deletion struct {
	edit       *analysis.TextEdit
	file       string
	start, end int
}

type fileEnd struct {
	file string
	end  int
}

// widenLeadingDeletions extends each deletion of statements at the start of a
// block so it also takes the line break and blank lines after them; otherwise
// the block would open with a blank line. It edits the diagnostics in place.
func widenLeadingDeletions(ws *types.Workspace, diags []analysis.Diagnostic) {
	var dels []deletion

	startOf := make(map[fileEnd]int)

	for i := range diags {
		if len(diags[i].SuggestedFixes) == 0 {
			continue
		}

		edits := diags[i].SuggestedFixes[0].TextEdits
		for j := range edits {
			start, end := analyzers.EditRange(ws.FileSet, edits[j])
			if len(edits[j].NewText) != 0 || start.Offset >= end.Offset {
				continue
			}

			dels = append(dels, deletion{&edits[j], start.Filename, start.Offset, end.Offset})

			key := fileEnd{start.Filename, end.Offset}
			if prev, ok := startOf[key]; !ok || start.Offset < prev {
				startOf[key] = start.Offset
			}
		}
	}

	if len(dels) == 0 {
		return
	}

	contents := analyzers.FileContents(ws)
	braces := blockBraces(ws)

	for _, d := range dels {
		src := contents[d.file]
		if d.end > len(src) {
			continue
		}

		// Walk back over whitespace and other deleted spans to the opening brace.
		p := d.start

		for {
			if s, ok := startOf[fileEnd{d.file, p}]; ok {
				p = s
				continue
			}

			if p > 0 && strings.IndexByte(" \t\r\n", src[p-1]) >= 0 {
				p--
				continue
			}

			break
		}

		if p == 0 || !braces[d.file][p-1] {
			continue
		}

		blank := blankLinesAfter.Find(src[d.end:])
		if blank == nil {
			continue
		}

		d.edit.End = ws.FileSet.File(d.edit.Pos).Pos(d.end + len(blank))

		// A statement that shares its line with the brace must not pull the next one up.
		lineStart := bytes.LastIndexByte(src[:d.start], '\n') + 1
		if len(bytes.TrimSpace(src[lineStart:d.start])) > 0 {
			d.edit.NewText = []byte("\n")
		}
	}
}

// blockBraces maps each file to the offsets of its block statements' opening braces.
func blockBraces(ws *types.Workspace) map[string]map[int]bool {
	braces := make(map[string]map[int]bool)

	for _, pkg := range ws.Packages {
		for _, files := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			for _, f := range files {
				if f.AST == nil {
					continue
				}

				offsets := make(map[int]bool)

				ast.Inspect(f.AST, func(n ast.Node) bool {
					if b, ok := n.(*ast.BlockStmt); ok {
						offsets[ws.FileSet.PositionFor(b.Lbrace, false).Offset] = true
					}

					return true
				})

				braces[f.Path] = offsets
			}
		}
	}

	return braces
}

// unusedImportRemovals returns deletions for the imports that the errors
// report as newly unused. An import whose lines other changes edit is left.
func unusedImportRemovals(ws *types.Workspace, changes []types.Change, errs []string) []types.Change {
	files := make(map[string]*types.File)

	for _, pkg := range ws.Packages {
		for _, group := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			for _, f := range group {
				files[f.Path] = f
			}
		}
	}

	unused := make(map[*ast.ImportSpec]bool)

	for _, e := range errs {
		m := unusedImport.FindStringSubmatch(e)
		if m == nil || files[m[1]] == nil || files[m[1]].AST == nil {
			continue
		}

		for _, spec := range files[m[1]].AST.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err == nil && path == m[2] {
				unused[spec] = true
			}
		}
	}

	offset := func(p token.Pos) int { return ws.FileSet.PositionFor(p, false).Offset }

	var removals []types.Change

	for _, f := range files {
		if f.AST == nil {
			continue
		}

		for _, decl := range f.AST.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}

			var gone []*ast.ImportSpec

			for _, spec := range gd.Specs {
				if is, ok := spec.(*ast.ImportSpec); ok && unused[is] {
					gone = append(gone, is)
				}
			}

			// Another change inserting into a parenthesized decl must keep its parentheses.
			if len(gone) == len(gd.Specs) && (!gd.Lparen.IsValid() || !insertedInside(f.Path, offset(gd.Pos()), offset(gd.End()), changes)) {
				removals = append(removals, removalRange(f.Path, offset(gd.Pos()), offset(gd.End()), slices.Concat(changes, removals))...)
				continue
			}

			for _, spec := range gone {
				end := spec.End()
				if spec.Comment != nil {
					end = spec.Comment.End()
				}

				// Take the line break before the spec so the group keeps no gap.
				first := spec.Pos()
				if spec.Doc != nil {
					first = spec.Doc.Pos()
				}

				start := offset(first)
				if nl := bytes.LastIndexByte(f.OriginalContent[:start], '\n'); len(bytes.TrimSpace(f.OriginalContent[nl+1:start])) == 0 {
					start = nl
				}

				removals = append(removals, removalRange(f.Path, start, offset(end), slices.Concat(changes, removals))...)
			}
		}
	}

	return removals
}

// insertedInside reports whether a change of file inserts strictly inside [a, b).
func insertedInside(file string, a, b int, changes []types.Change) bool {
	return slices.ContainsFunc(changes, func(c types.Change) bool {
		return c.File == file && c.Start == c.End && a < c.Start && c.Start < b
	})
}

// removalRange deletes [a, b) of file around any insertions strictly inside it,
// or returns nothing if another change edits that text.
func removalRange(file string, a, b int, existing []types.Change) []types.Change {
	cuts := []int{a, b}

	for _, c := range existing {
		switch {
		case c.File != file:
		case c.Start == c.End && a < c.Start && c.Start < b:
			cuts = append(cuts, c.Start)
		case c.Start < b && a < c.End:
			return nil
		}
	}

	slices.Sort(cuts)

	var out []types.Change

	for i := 0; i+1 < len(cuts); i++ {
		if cuts[i] == cuts[i+1] {
			continue
		}

		out = append(out, types.Change{File: file, Start: cuts[i], End: cuts[i+1], Description: "Remove unused import", Fragment: true})
	}

	return out
}
