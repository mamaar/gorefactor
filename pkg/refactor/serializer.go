package refactor

import (
	"bufio"
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

	// Group changes by file
	fileChanges := make(map[string][]refactorTypes.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Apply changes to each file
	for filePath, changesForFile := range fileChanges {
		if err := s.applyChangesToFile(filePath, changesForFile); err != nil {
			return &refactorTypes.RefactorError{
				Type:    refactorTypes.FileSystemError,
				Message: fmt.Sprintf("failed to apply changes to file %s: %v", filePath, err),
			}
		}
	}

	return nil
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

	preview.WriteString(fmt.Sprintf("Preview of %d changes across %d files:\n\n", len(changes), len(files)))

	for _, file := range files {
		changesForFile := fileChanges[file]
		preview.WriteString(fmt.Sprintf("File: %s\n", file))
		preview.WriteString(strings.Repeat("-", len(file)+6) + "\n")

		// Sort changes by position for logical order
		sort.Slice(changesForFile, func(i, j int) bool {
			return changesForFile[i].Start < changesForFile[j].Start
		})

		for i, change := range changesForFile {
			preview.WriteString(fmt.Sprintf("%d. %s\n", i+1, change.Description))
			preview.WriteString(fmt.Sprintf("   Position: %d-%d\n", change.Start, change.End))
			
			if change.OldText != "" {
				preview.WriteString(fmt.Sprintf("   - %s\n", s.truncateText(change.OldText)))
			}
			if change.NewText != "" {
				preview.WriteString(fmt.Sprintf("   + %s\n", s.truncateText(change.NewText)))
			}
			preview.WriteString("\n")
		}
		preview.WriteString("\n")
	}

	return preview.String(), nil
}

// applyChangesToFile applies changes to a single file
func (s *Serializer) applyChangesToFile(filePath string, changes []refactorTypes.Change) error {
	// Read the current file content, or start with empty content for new files
	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - it's a new file, start with empty content
			content = []byte("")
		} else {
			return fmt.Errorf("failed to read file: %v", err)
		}
	}

	// Sort changes by position in reverse order so we can apply them without affecting positions
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Start > changes[j].Start
	})

	// Validate that changes don't overlap
	if err := s.validateChangePositions(changes); err != nil {
		return fmt.Errorf("invalid change positions: %v", err)
	}

	// Apply changes
	modifiedContent := string(content)
	for _, change := range changes {
		modifiedContent, err = s.applyChange(modifiedContent, change)
		if err != nil {
			return fmt.Errorf("failed to apply change: %v", err)
		}
	}

	// Organize imports and format the modified content if it's Go code
	if strings.HasSuffix(filePath, ".go") {
		if s.modulePath != "" {
			modifiedContent = organizeImports(modifiedContent, s.modulePath, s.workspaceModules)
		}

		formatted, err := s.formatGoCode(modifiedContent)
		if err != nil {
			// If formatting fails, we still want to save the changes
			// but log a warning
			fmt.Fprintf(os.Stderr, "Warning: failed to format %s: %v\n", filePath, err)
		} else {
			modifiedContent = formatted
		}
	}

	// Write the modified content back to the file
	if err := os.WriteFile(filePath, []byte(modifiedContent), 0644); err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}

	return nil
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
	for i := 0; i < len(changes); i++ {
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

// BackupFile creates a backup of a file before modifications
func (s *Serializer) BackupFile(filePath string) (string, error) {
	backupPath := filePath + ".backup"
	
	// Ensure the directory exists for both file and backup
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %v", err)
	}
	
	// Read original file
	content, err := os.ReadFile(filePath)
	if err != nil {
		// If file doesn't exist, it's a new file - no backup needed
		if os.IsNotExist(err) {
			// Create empty backup to indicate this was a new file
			if err := os.WriteFile(backupPath, []byte(""), 0644); err != nil {
				return "", fmt.Errorf("failed to create empty backup for new file: %v", err)
			}
			return backupPath, nil
		}
		return "", fmt.Errorf("failed to read original file: %v", err)
	}

	// Write backup
	if err := os.WriteFile(backupPath, content, 0644); err != nil {
		return "", fmt.Errorf("failed to create backup: %v", err)
	}

	return backupPath, nil
}

// RestoreFromBackup restores a file from its backup
func (s *Serializer) RestoreFromBackup(filePath, backupPath string) error {
	// Read backup content
	content, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup file: %v", err)
	}

	// Restore original file
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return fmt.Errorf("failed to restore file: %v", err)
	}

	return nil
}

// GenerateDiff generates a unified diff between original and modified content
func (s *Serializer) GenerateDiff(filePath string, originalContent, modifiedContent string) (string, error) {
	var diff strings.Builder
	
	diff.WriteString(fmt.Sprintf("--- %s\n", filePath))
	diff.WriteString(fmt.Sprintf("+++ %s\n", filePath))

	// Split content into lines
	originalLines := strings.Split(originalContent, "\n")
	modifiedLines := strings.Split(modifiedContent, "\n")

	// Simple line-by-line diff (in a production system, you'd use a proper diff algorithm)
	maxLines := len(originalLines)
	if len(modifiedLines) > maxLines {
		maxLines = len(modifiedLines)
	}

	lineNum := 1
	for i := 0; i < maxLines; i++ {
		var originalLine, modifiedLine string
		
		if i < len(originalLines) {
			originalLine = originalLines[i]
		}
		if i < len(modifiedLines) {
			modifiedLine = modifiedLines[i]
		}

		if originalLine != modifiedLine {
			if originalLine != "" {
				diff.WriteString(fmt.Sprintf("@@ -%d,1 +%d,1 @@\n", lineNum, lineNum))
				diff.WriteString(fmt.Sprintf("-%s\n", originalLine))
			}
			if modifiedLine != "" {
				if originalLine == "" {
					diff.WriteString(fmt.Sprintf("@@ -%d,0 +%d,1 @@\n", lineNum, lineNum))
				}
				diff.WriteString(fmt.Sprintf("+%s\n", modifiedLine))
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