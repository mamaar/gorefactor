package refactor

import (
	"bufio"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

// Serializer applies refactoring changes to files while preserving formatting
type Serializer struct {
	fileSet          *token.FileSet
	modulePath       string
	workspaceModules []string
}

func NewSerializer() *Serializer {
	return &Serializer{
		fileSet: token.NewFileSet(),
	}
}

// SetModuleInfo configures the module path and workspace modules used for
// import ordering when writing Go files.
func (s *Serializer) SetModuleInfo(modulePath string, workspaceModules []string) {
	s.modulePath = modulePath
	s.workspaceModules = workspaceModules
}

// ApplyChanges applies a list of changes to the workspace files
func (s *Serializer) ApplyChanges(ws *refactorTypes.Workspace, changes []refactorTypes.Change) error {
	if len(changes) == 0 {
		return nil // No changes to apply
	}

	// Every Change.File must be an absolute path. A relative one is always an
	// operation that failed to resolve its target against the workspace root,
	// and os.WriteFile would resolve it against the server process's working
	// directory instead — writing real files somewhere nobody asked for, with
	// no error to show for it.
	//
	// This runs before anything is touched: rollback restores what a failed
	// plan wrote, but a stray write outside the workspace is not undone.
	if err := validateChangePathsAbsolute(changes); err != nil {
		return err
	}

	// Generated files are read from disk, not the workspace: ExecutePlan passes
	// none, and a file regenerated since load would carry a stale flag.
	if err := GeneratedFileEdits(changes); err != nil {
		return err
	}

	// Group changes by file
	fileChanges := make(map[string][]refactorTypes.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Compute every file's new content before touching the disk, so a change
	// that fails to apply leaves no file written.
	paths := make([]string, 0, len(fileChanges))
	for filePath := range fileChanges {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)

	steps := make([]fileStep, 0, len(paths))
	for _, filePath := range paths {
		step, err := s.prepareFile(filePath, fileChanges[filePath])
		if err != nil {
			return &refactorTypes.RefactorError{
				Type:    refactorTypes.FileSystemError,
				Message: fmt.Sprintf("failed to apply changes to file %s: %v", filePath, err),
			}
		}
		steps = append(steps, step)
	}

	for i := range steps {
		if err := steps[i].commit(); err != nil {
			return errors.Join(&refactorTypes.RefactorError{
				Type:    refactorTypes.FileSystemError,
				Message: err.Error(),
			}, rollback(steps[:i], &steps[i]))
		}
	}

	return nil
}

// fileStep is one file's prepared write or deletion, with the pre-image
// needed to undo it.
type fileStep struct {
	path    string
	delete  bool
	content []byte

	existed bool
	old     []byte
	mode    os.FileMode
	// createdDir is the outermost directory the write had to create, if any.
	createdDir string
}

// prepareFile reads filePath's pre-image and computes its new content,
// without writing anything. A file marked for deletion gets no new content.
func (s *Serializer) prepareFile(filePath string, changes []refactorTypes.Change) (fileStep, error) {
	step := fileStep{path: filePath, delete: fileHasDeletion(changes), mode: 0644}
	info, err := os.Stat(filePath)
	switch {
	case err == nil:
		old, err := os.ReadFile(filePath)
		if err != nil {
			return step, fmt.Errorf("failed to read file: %v", err)
		}
		step.existed, step.old, step.mode = true, old, info.Mode().Perm()
	case !os.IsNotExist(err):
		return step, fmt.Errorf("failed to read file: %v", err)
	}
	if step.delete {
		return step, nil
	}
	step.content, err = s.computeContent(filePath, step.old, changes)
	return step, err
}

// commit writes or deletes the step's file.
func (f *fileStep) commit() error {
	if f.delete {
		if err := deleteFile(f.path); err != nil {
			return fmt.Errorf("failed to delete file %s: %v", f.path, err)
		}
		return nil
	}
	if !f.existed {
		f.createdDir = outermostMissingDir(filepath.Dir(f.path))
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
		return fmt.Errorf("failed to apply changes to file %s: failed to create directory: %v", f.path, err)
	}
	if err := os.WriteFile(f.path, f.content, 0644); err != nil {
		return fmt.Errorf("failed to apply changes to file %s: failed to write file: %v", f.path, err)
	}
	return nil
}

// undo puts the step's file back as it was before commit.
func (f *fileStep) undo() error {
	if !f.existed {
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		removeCreatedDirs(filepath.Dir(f.path), f.createdDir)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(f.path, f.old, f.mode)
}

// rollback undoes the failed step and the committed steps before it in reverse
// order, and reports every file it could not restore. A failed delete left its
// file untouched, so it is not rewritten.
func rollback(committed []fileStep, failed *fileStep) error {
	if !failed.delete {
		committed = append(committed, *failed)
	}
	var errs []error
	for i := len(committed) - 1; i >= 0; i-- {
		if err := committed[i].undo(); err != nil {
			errs = append(errs, fmt.Errorf("failed to restore %s: %v", committed[i].path, err))
		}
	}
	return errors.Join(errs...)
}

// outermostMissingDir returns the outermost ancestor of dir (or dir itself)
// that does not exist, or "" when dir exists.
func outermostMissingDir(dir string) string {
	missing := ""
	for {
		if _, err := os.Stat(dir); err == nil {
			return missing
		}
		missing = dir
		parent := filepath.Dir(dir)
		if parent == dir {
			return missing
		}
		dir = parent
	}
}

// removeCreatedDirs removes dir and its ancestors up to and including top
// while each is empty. It does nothing when top is "".
func removeCreatedDirs(dir, top string) {
	if top == "" {
		return
	}
	for {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) || dir == top {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// validateChangePathsAbsolute rejects a change set containing any non-absolute
// file path. See ApplyChanges for why this is checked up front.
func validateChangePathsAbsolute(changes []refactorTypes.Change) error {
	for _, change := range changes {
		if change.File == "" {
			return &refactorTypes.RefactorError{
				Type:    refactorTypes.FileSystemError,
				Message: fmt.Sprintf("refusing to apply a change with no file path (%s)", change.Description),
			}
		}
		if !filepath.IsAbs(change.File) {
			return &refactorTypes.RefactorError{
				Type: refactorTypes.FileSystemError,
				Message: fmt.Sprintf(
					"refusing to apply a change to the relative path %q (%s): "+
						"change paths must be absolute, or they resolve against the server's working directory "+
						"instead of the workspace",
					change.File, change.Description),
				File: change.File,
			}
		}
	}
	return nil
}

// fileHasDeletion reports whether any change in the group marks the file for
// deletion.
func fileHasDeletion(changes []refactorTypes.Change) bool {
	for _, c := range changes {
		if c.Delete {
			return true
		}
	}
	return false
}

// deleteFile removes filePath from disk (tolerating an already-absent file)
// and prunes parent directories that become empty as a result, so a moved
// package leaves no empty husk behind. Pruning stops at the first non-empty
// directory.
func deleteFile(filePath string) error {
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	pruneEmptyDirs(filepath.Dir(filePath))
	return nil
}

// pruneEmptyDirs removes dir and its ancestors while each is empty, stopping
// at the first non-empty directory (or on any error). It never ascends past a
// directory that still contains entries, so it cannot delete a sibling's
// files or climb out of the workspace.
func pruneEmptyDirs(dir string) {
	for {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// PreviewChanges generates a preview of what changes would be applied
func (s *Serializer) PreviewChanges(ws *refactorTypes.Workspace, changes []refactorTypes.Change) (string, error) {
	if len(changes) == 0 {
		return "No changes to preview", nil
	}

	var preview strings.Builder

	// Group changes by file for better organization
	fileChanges := make(map[string][]refactorTypes.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Sort files for consistent output
	var files []string
	for file := range fileChanges {
		files = append(files, file)
	}
	sort.Strings(files)

	fmt.Fprintf(&preview, "Preview of %d changes across %d files:\n\n", len(changes), len(files))

	for _, file := range files {
		changesForFile := fileChanges[file]
		fmt.Fprintf(&preview, "File: %s\n", file)
		preview.WriteString(strings.Repeat("-", len(file)+6) + "\n")

		// Sort changes by position for logical order
		sort.Slice(changesForFile, func(i, j int) bool {
			return changesForFile[i].Start < changesForFile[j].Start
		})

		for i, change := range changesForFile {
			fmt.Fprintf(&preview, "%d. %s\n", i+1, change.Description)
			if change.Delete {
				preview.WriteString("   Action: delete file\n\n")
				continue
			}
			fmt.Fprintf(&preview, "   Position: %d-%d\n", change.Start, change.End)

			if change.OldText != "" {
				fmt.Fprintf(&preview, "   - %s\n", s.truncateText(change.OldText))
			}
			if change.NewText != "" {
				fmt.Fprintf(&preview, "   + %s\n", s.truncateText(change.NewText))
			}
			preview.WriteString("\n")
		}
		preview.WriteString("\n")
	}

	return preview.String(), nil
}

// ApplyChangesToContent applies changes (already known to target the same
// logical file) to in-memory content and returns the result, without
// touching disk. It sorts changes into descending Start order and validates
// they don't overlap before applying each one via applyChange — the same
// offset-application semantics ApplyChanges uses, factored out here so
// callers that never had a real file to read from disk (VerifyPlanTypes'
// in-memory type-check verification, in particular) can reuse them exactly
// rather than re-implementing the sort/validate/apply sequence.
func (s *Serializer) ApplyChangesToContent(content []byte, changes []refactorTypes.Change) ([]byte, error) {
	// Copy before sorting: callers may hold their own reference to changes
	// (e.g. a shared slice from a plan) and must not see it reordered.
	sorted := make([]refactorTypes.Change, len(changes))
	copy(sorted, changes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Start > sorted[j].Start
	})

	if err := s.validateChangePositions(sorted); err != nil {
		return nil, fmt.Errorf("invalid change positions: %v", err)
	}

	modifiedContent := string(content)
	var err error
	for _, change := range sorted {
		modifiedContent, err = s.applyChange(modifiedContent, change)
		if err != nil {
			return nil, fmt.Errorf("failed to apply change: %v", err)
		}
	}
	return []byte(modifiedContent), nil
}

// computeContent applies changes to content, the current bytes of filePath
// (empty for a new file), and organises imports and formats Go files.
func (s *Serializer) computeContent(filePath string, content []byte, changes []refactorTypes.Change) ([]byte, error) {
	modifiedBytes, err := s.ApplyChangesToContent(content, changes)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(filePath, ".go") {
		return modifiedBytes, nil
	}
	modifiedContent := string(modifiedBytes)
	if s.modulePath != "" {
		modifiedContent = OrganizeImports(modifiedContent, s.modulePath, s.workspaceModules)
	}
	formatted, err := s.formatGoCode(modifiedContent)
	if err == nil {
		return []byte(formatted), nil
	}
	// A file that already failed to parse can't be made worse by the plan.
	if len(content) > 0 {
		if _, parseErr := parser.ParseFile(token.NewFileSet(), "", content, parser.SkipObjectResolution); parseErr != nil {
			return []byte(modifiedContent), nil
		}
	}
	return nil, fmt.Errorf("%s: result is not valid Go: %w", filePath, err)
}

// applyChange applies a single change to the content
func (s *Serializer) applyChange(content string, change refactorTypes.Change) (string, error) {
	if change.Start < 0 || change.End > len(content) || change.Start > change.End {
		return "", fmt.Errorf("invalid change bounds: start=%d, end=%d, content length=%d",
			change.Start, change.End, len(content))
	}

	// Extract the parts before and after the change
	before := content[:change.Start]
	after := content[change.End:]

	// Verify that the old text matches what we expect (if provided)
	if change.OldText != "" {
		actualOldText := content[change.Start:change.End]
		if actualOldText != change.OldText {
			return "", fmt.Errorf("old text mismatch: expected '%s', found '%s'",
				change.OldText, actualOldText)
		}
	}

	// Construct the new content
	newContent := before + change.NewText + after
	return newContent, nil
}

// validateChangePositions ensures changes don't overlap
func (s *Serializer) validateChangePositions(changes []refactorTypes.Change) error {
	for i := range changes {
		for j := i + 1; j < len(changes); j++ {
			change1 := changes[i]
			change2 := changes[j]

			// Check for overlap
			if s.changesOverlap(change1, change2) {
				return fmt.Errorf("overlapping changes detected: [%d-%d] and [%d-%d]",
					change1.Start, change1.End, change2.Start, change2.End)
			}
		}
	}
	return nil
}

// changesOverlap checks if two changes overlap
func (s *Serializer) changesOverlap(change1, change2 refactorTypes.Change) bool {
	return change1.Start < change2.End && change2.Start < change1.End
}

// formatGoCode formats Go source code using the standard formatter
func (s *Serializer) formatGoCode(code string) (string, error) {
	// First, try to parse to ensure it's valid Go code
	_, err := parser.ParseFile(s.fileSet, "", code, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("invalid Go syntax: %v", err)
	}

	// Format the code
	formatted, err := format.Source([]byte(code))
	if err != nil {
		return "", fmt.Errorf("formatting failed: %v", err)
	}

	return string(formatted), nil
}

// truncateText truncates text for display in previews
func (s *Serializer) truncateText(text string, maxLength ...int) string {
	length := 80 // default max length
	if len(maxLength) > 0 {
		length = maxLength[0]
	}

	// Replace newlines with spaces for single-line display
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")

	// Collapse multiple spaces
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	text = strings.TrimSpace(text)

	if len(text) <= length {
		return text
	}

	return text[:length-3] + "..."
}

// GenerateDiff generates a unified diff between original and modified content
func (s *Serializer) GenerateDiff(filePath string, originalContent, modifiedContent string) (string, error) {
	var diff strings.Builder

	fmt.Fprintf(&diff, "--- %s\n", filePath)
	fmt.Fprintf(&diff, "+++ %s\n", filePath)

	// Split content into lines
	originalLines := strings.Split(originalContent, "\n")
	modifiedLines := strings.Split(modifiedContent, "\n")

	// Simple line-by-line diff (in a production system, you'd use a proper diff algorithm)
	maxLines := max(len(modifiedLines), len(originalLines))

	lineNum := 1
	for i := range maxLines {
		var originalLine, modifiedLine string

		if i < len(originalLines) {
			originalLine = originalLines[i]
		}
		if i < len(modifiedLines) {
			modifiedLine = modifiedLines[i]
		}

		if originalLine != modifiedLine {
			if originalLine != "" {
				fmt.Fprintf(&diff, "@@ -%d,1 +%d,1 @@\n", lineNum, lineNum)
				fmt.Fprintf(&diff, "-%s\n", originalLine)
			}
			if modifiedLine != "" {
				if originalLine == "" {
					fmt.Fprintf(&diff, "@@ -%d,0 +%d,1 @@\n", lineNum, lineNum)
				}
				fmt.Fprintf(&diff, "+%s\n", modifiedLine)
			}
		}
		lineNum++
	}

	return diff.String(), nil
}

// ValidateFileStructure ensures that the file structure is valid after changes
func (s *Serializer) ValidateFileStructure(filePath string) error {
	if !strings.HasSuffix(filePath, ".go") {
		return nil // Only validate Go files
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file for validation: %v", err)
	}

	// Try to parse the file to ensure it's valid Go code
	_, err = parser.ParseFile(s.fileSet, filePath, content, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("file structure validation failed: %v", err)
	}

	return nil
}

// GetFileLines reads a file and returns its lines with line numbers
func (s *Serializer) GetFileLines(filePath string, startLine, endLine int) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer func() { _ = file.Close() }()

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 1

	for scanner.Scan() {
		if lineNum >= startLine && lineNum <= endLine {
			lines = append(lines, fmt.Sprintf("%d: %s", lineNum, scanner.Text()))
		}
		lineNum++

		if lineNum > endLine {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %v", err)
	}

	return lines, nil
}
