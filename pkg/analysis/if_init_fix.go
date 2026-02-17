package analysis

import (
	"bytes"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// IfInitFixer fixes if-init assignment violations by splitting them into
// separate assignment + if-check statements.
type IfInitFixer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
}

// NewIfInitFixer creates a new if-init fixer
func NewIfInitFixer(ws *types.Workspace) *IfInitFixer {
	return &IfInitFixer{
		workspace: ws,
		fileSet:   ws.FileSet,
	}
}

// Fix analyzes the workspace (or a specific package) and returns a plan to fix all violations.
func (f *IfInitFixer) Fix(pkg string) (*types.RefactoringPlan, error) {
	analyzer := NewIfInitAnalyzer(f.workspace)

	var violations []*IfInitViolation
	if pkg != "" {
		resolved := types.ResolvePackagePath(f.workspace, pkg)
		p, ok := f.workspace.Packages[resolved]
		if !ok {
			return nil, nil
		}
		violations = analyzer.AnalyzePackage(p)
	} else {
		violations = analyzer.AnalyzeWorkspace()
	}

	if len(violations) == 0 {
		return &types.RefactoringPlan{}, nil
	}

	var changes []types.Change
	affectedSet := make(map[string]bool)

	for _, v := range violations {
		file := f.findFile(v.File)
		if file == nil {
			continue
		}

		change, ok := f.buildChange(file, v)
		if !ok {
			continue
		}
		changes = append(changes, change)
		affectedSet[v.File] = true
	}

	var affected []string
	for f := range affectedSet {
		affected = append(affected, f)
	}

	return &types.RefactoringPlan{
		Changes:       changes,
		AffectedFiles: affected,
	}, nil
}

// buildChange computes a single Change for transforming an if-init statement.
func (f *IfInitFixer) buildChange(file *types.File, v *IfInitViolation) (types.Change, bool) {
	node := v.Node
	if node == nil || node.Init == nil {
		return types.Change{}, false
	}

	content := file.OriginalContent

	// Get byte offsets for the region: from "if" keyword to (including) "{"
	startOffset := f.fileSet.Position(node.Pos()).Offset
	lbraceOffset := f.fileSet.Position(node.Body.Lbrace).Offset
	endOffset := lbraceOffset + 1 // include the "{"

	if startOffset < 0 || endOffset > len(content) || startOffset >= endOffset {
		return types.Change{}, false
	}

	oldText := string(content[startOffset:endOffset])

	// Detect indentation from the original line
	indent := extractIndentation(content, startOffset)

	// Get the init assignment text from source
	initStart := f.fileSet.Position(node.Init.Pos()).Offset
	initEnd := f.fileSet.Position(node.Init.End()).Offset
	if initStart < 0 || initEnd > len(content) || initStart >= initEnd {
		return types.Change{}, false
	}
	assignment := string(content[initStart:initEnd])

	// Get the condition text from source
	condStart := f.fileSet.Position(node.Cond.Pos()).Offset
	condEnd := f.fileSet.Position(node.Cond.End()).Offset
	if condStart < 0 || condEnd > len(content) || condStart >= condEnd {
		return types.Change{}, false
	}
	condition := string(content[condStart:condEnd])

	// Build new text: assignment + newline + indent + if condition {
	var sb strings.Builder
	sb.WriteString(assignment)
	sb.WriteByte('\n')
	sb.WriteString(indent)
	sb.WriteString("if ")
	sb.WriteString(condition)
	sb.WriteString(" {")

	return types.Change{
		File:        file.Path,
		Start:       startOffset,
		End:         endOffset,
		OldText:     oldText,
		NewText:     sb.String(),
		Description: "Split if-init assignment into separate assignment and if-check",
	}, true
}

// extractIndentation returns the leading whitespace of the line containing offset.
func extractIndentation(content []byte, offset int) string {
	// Walk backwards to find the start of the line
	lineStart := offset
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}

	// Collect leading whitespace
	var buf bytes.Buffer
	for i := lineStart; i < len(content); i++ {
		if content[i] == ' ' || content[i] == '\t' {
			buf.WriteByte(content[i])
		} else {
			break
		}
	}
	return buf.String()
}

// findFile locates a file by path across all packages.
func (f *IfInitFixer) findFile(path string) *types.File {
	for _, pkg := range f.workspace.Packages {
		if file, ok := pkg.Files[path]; ok {
			return file
		}
	}
	return nil
}
