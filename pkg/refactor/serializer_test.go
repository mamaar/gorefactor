package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

func TestNewSerializer(t *testing.T) {
	serializer := NewSerializer()
	if serializer == nil {
		t.Fatal("Expected NewSerializer to return a non-nil serializer")
	}

	if serializer.fileSet == nil {
		t.Error("Expected serializer to have a non-nil fileSet")
	}
}

func TestSerializer_ApplyChanges_NoChanges(t *testing.T) {
	serializer := NewSerializer()

	err := serializer.ApplyChanges(nil, []refactorTypes.Change{})
	if err != nil {
		t.Errorf("Expected no error with empty changes, got %v", err)
	}
}

func TestSerializer_ApplyChanges_SingleFile(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func Original() {
	// original function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create a change to rename the function
	changes := []refactorTypes.Change{
		{
			File:        testFile,
			Start:       strings.Index(originalContent, "Original"),
			End:         strings.Index(originalContent, "Original") + len("Original"),
			OldText:     "Original",
			NewText:     "Modified",
			Description: "Rename function",
		},
	}

	// Apply changes
	err = serializer.ApplyChanges(nil, changes)
	if err != nil {
		t.Fatalf("Failed to apply changes: %v", err)
	}

	// Verify the file was modified
	modifiedContent, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read modified file: %v", err)
	}

	if !strings.Contains(string(modifiedContent), "Modified") {
		t.Error("Expected file to contain 'Modified' after applying changes")
	}

	if strings.Contains(string(modifiedContent), "Original") {
		t.Error("Expected 'Original' to be replaced in the file")
	}
}

func TestSerializer_ApplyChanges_MultipleChanges(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := `package test

func FirstFunc() {
	// first function
}

func SecondFunc() {
	// second function
}
`

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create multiple changes
	changes := []refactorTypes.Change{
		{
			File:        testFile,
			Start:       strings.Index(originalContent, "FirstFunc"),
			End:         strings.Index(originalContent, "FirstFunc") + len("FirstFunc"),
			OldText:     "FirstFunc",
			NewText:     "RenamedFirst",
			Description: "Rename first function",
		},
		{
			File:        testFile,
			Start:       strings.Index(originalContent, "SecondFunc"),
			End:         strings.Index(originalContent, "SecondFunc") + len("SecondFunc"),
			OldText:     "SecondFunc",
			NewText:     "RenamedSecond",
			Description: "Rename second function",
		},
	}

	// Apply changes
	err = serializer.ApplyChanges(nil, changes)
	if err != nil {
		t.Fatalf("Failed to apply changes: %v", err)
	}

	// Verify the file was modified
	modifiedContent, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read modified file: %v", err)
	}

	contentStr := string(modifiedContent)
	if !strings.Contains(contentStr, "RenamedFirst") {
		t.Error("Expected file to contain 'RenamedFirst' after applying changes")
	}

	if !strings.Contains(contentStr, "RenamedSecond") {
		t.Error("Expected file to contain 'RenamedSecond' after applying changes")
	}

	if strings.Contains(contentStr, "FirstFunc") || strings.Contains(contentStr, "SecondFunc") {
		t.Error("Expected original function names to be replaced")
	}
}

func TestSerializer_ApplyChanges_InvalidChange(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := "package test\n"

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create an invalid change (bounds exceed file length)
	changes := []refactorTypes.Change{
		{
			File:        testFile,
			Start:       0,
			End:         1000, // Exceeds file length
			NewText:     "replacement",
			Description: "Invalid change",
		},
	}

	// Apply changes - should fail
	err = serializer.ApplyChanges(nil, changes)
	if err == nil {
		t.Error("Expected error when applying invalid change")
	}
}

func TestSerializer_PreviewChanges_NoChanges(t *testing.T) {
	serializer := NewSerializer()

	preview, err := serializer.PreviewChanges(nil, []refactorTypes.Change{})
	if err != nil {
		t.Errorf("Expected no error with empty changes, got %v", err)
	}

	if preview != "No changes to preview" {
		t.Errorf("Expected 'No changes to preview', got '%s'", preview)
	}
}

func TestSerializer_PreviewChanges_WithChanges(t *testing.T) {
	serializer := NewSerializer()

	changes := []refactorTypes.Change{
		{
			File:        "test.go",
			Start:       10,
			End:         20,
			OldText:     "old",
			NewText:     "new",
			Description: "test change",
		},
		{
			File:        "other.go",
			Start:       5,
			End:         15,
			OldText:     "old2",
			NewText:     "new2",
			Description: "another test change",
		},
	}

	preview, err := serializer.PreviewChanges(nil, changes)
	if err != nil {
		t.Errorf("Expected no error with changes, got %v", err)
	}

	// Check that preview contains expected information
	if !strings.Contains(preview, "2 changes across 2 files") {
		t.Error("Expected preview to mention 2 changes across 2 files")
	}

	if !strings.Contains(preview, "test.go") {
		t.Error("Expected preview to mention test.go")
	}

	if !strings.Contains(preview, "other.go") {
		t.Error("Expected preview to mention other.go")
	}

	if !strings.Contains(preview, "test change") {
		t.Error("Expected preview to mention test change description")
	}
}

func TestSerializer_applyChange(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name        string
		content     string
		change      refactorTypes.Change
		expected    string
		expectError bool
	}{
		{
			name:    "Simple replacement",
			content: "Hello World",
			change: refactorTypes.Change{
				Start:   0,
				End:     5,
				OldText: "Hello",
				NewText: "Hi",
			},
			expected:    "Hi World",
			expectError: false,
		},
		{
			name:    "Insert at beginning",
			content: "World",
			change: refactorTypes.Change{
				Start:   0,
				End:     0,
				NewText: "Hello ",
			},
			expected:    "Hello World",
			expectError: false,
		},
		{
			name:    "Delete text",
			content: "Hello World",
			change: refactorTypes.Change{
				Start:   5,
				End:     11,
				OldText: " World",
				NewText: "",
			},
			expected:    "Hello",
			expectError: false,
		},
		{
			name:    "Invalid bounds",
			content: "Hello",
			change: refactorTypes.Change{
				Start: 0,
				End:   10, // Exceeds content length
			},
			expectError: true,
		},
		{
			name:    "Old text mismatch",
			content: "Hello World",
			change: refactorTypes.Change{
				Start:   0,
				End:     5,
				OldText: "Hi", // Doesn't match actual content
				NewText: "Hey",
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := serializer.applyChange(tc.content, tc.change)
			hasError := err != nil

			if hasError != tc.expectError {
				t.Errorf("Expected error status %v, got %v (error: %v)", tc.expectError, hasError, err)
				return
			}

			if !tc.expectError && result != tc.expected {
				t.Errorf("Expected result '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestSerializer_validateChangePositions(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name        string
		changes     []refactorTypes.Change
		expectError bool
	}{
		{
			name: "Non-overlapping changes",
			changes: []refactorTypes.Change{
				{Start: 0, End: 10},
				{Start: 20, End: 30},
			},
			expectError: false,
		},
		{
			name: "Overlapping changes",
			changes: []refactorTypes.Change{
				{Start: 0, End: 15},
				{Start: 10, End: 25},
			},
			expectError: true,
		},
		{
			name: "Adjacent changes",
			changes: []refactorTypes.Change{
				{Start: 0, End: 10},
				{Start: 10, End: 20},
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := serializer.validateChangePositions(tc.changes)
			hasError := err != nil

			if hasError != tc.expectError {
				t.Errorf("Expected error status %v, got %v (error: %v)", tc.expectError, hasError, err)
			}
		})
	}
}

func TestSerializer_changesOverlap(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name     string
		change1  refactorTypes.Change
		change2  refactorTypes.Change
		expected bool
	}{
		{
			name:     "Non-overlapping",
			change1:  refactorTypes.Change{Start: 0, End: 10},
			change2:  refactorTypes.Change{Start: 20, End: 30},
			expected: false,
		},
		{
			name:     "Adjacent",
			change1:  refactorTypes.Change{Start: 0, End: 10},
			change2:  refactorTypes.Change{Start: 10, End: 20},
			expected: false,
		},
		{
			name:     "Overlapping",
			change1:  refactorTypes.Change{Start: 0, End: 15},
			change2:  refactorTypes.Change{Start: 10, End: 25},
			expected: true,
		},
		{
			name:     "Contained",
			change1:  refactorTypes.Change{Start: 0, End: 30},
			change2:  refactorTypes.Change{Start: 10, End: 20},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := serializer.changesOverlap(tc.change1, tc.change2)
			if result != tc.expected {
				t.Errorf("Expected changesOverlap to be %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestSerializer_formatGoCode(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name        string
		code        string
		expectError bool
	}{
		{
			name: "Valid Go code",
			code: `package main

import "fmt"

func main() {
fmt.Println("hello")
}`,
			expectError: false,
		},
		{
			name:        "Invalid Go code",
			code:        "package main\nfunc invalid( {",
			expectError: true,
		},
		{
			name: "Already formatted code",
			code: `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := serializer.formatGoCode(tc.code)
			hasError := err != nil

			if hasError != tc.expectError {
				t.Errorf("Expected error status %v, got %v (error: %v)", tc.expectError, hasError, err)
				return
			}

			if !tc.expectError {
				// Check that result is valid Go code
				if !strings.HasPrefix(result, "package main") {
					t.Error("Expected formatted result to start with package declaration")
				}
			}
		})
	}
}

func TestSerializer_truncateText(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name      string
		text      string
		maxLength []int
		expected  string
	}{
		{
			name:     "Short text",
			text:     "Hello",
			expected: "Hello",
		},
		{
			name:     "Long text",
			text:     strings.Repeat("a", 100),
			expected: strings.Repeat("a", 77) + "...",
		},
		{
			name:      "Custom length",
			text:      "Hello World",
			maxLength: []int{5},
			expected:  "He...",
		},
		{
			name:     "Text with newlines",
			text:     "Hello\nWorld\nTest",
			expected: "Hello World Test",
		},
		{
			name:     "Text with tabs and spaces",
			text:     "Hello\t\t  World   Test",
			expected: "Hello World Test",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var result string
			if len(tc.maxLength) > 0 {
				result = serializer.truncateText(tc.text, tc.maxLength[0])
			} else {
				result = serializer.truncateText(tc.text)
			}

			if result != tc.expected {
				t.Errorf("Expected truncateText result '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestSerializer_BackupFile(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := "package test\n"

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create backup
	backupPath, err := serializer.BackupFile(testFile)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Verify backup exists
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("Expected backup file to exist")
	}

	// Verify backup content
	backupContent, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("Failed to read backup file: %v", err)
	}

	if string(backupContent) != originalContent {
		t.Error("Expected backup content to match original")
	}
}

func TestSerializer_RestoreFromBackup(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	originalContent := "package test\n"

	err := os.WriteFile(testFile, []byte(originalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create backup
	backupPath, err := serializer.BackupFile(testFile)
	if err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Modify the original file
	modifiedContent := "package modified\n"
	err = os.WriteFile(testFile, []byte(modifiedContent), 0644)
	if err != nil {
		t.Fatalf("Failed to modify test file: %v", err)
	}

	// Restore from backup
	err = serializer.RestoreFromBackup(testFile, backupPath)
	if err != nil {
		t.Fatalf("Failed to restore from backup: %v", err)
	}

	// Verify file was restored
	restoredContent, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read restored file: %v", err)
	}

	if string(restoredContent) != originalContent {
		t.Error("Expected restored content to match original")
	}
}

func TestSerializer_ValidateFileStructure(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file with valid Go code
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	validContent := `package test

func Test() {
	// test function
}
`

	err := os.WriteFile(testFile, []byte(validContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Validate valid file
	err = serializer.ValidateFileStructure(testFile)
	if err != nil {
		t.Errorf("Expected no error for valid Go file, got %v", err)
	}

	// Test with invalid Go code
	invalidFile := filepath.Join(tempDir, "invalid.go")
	invalidContent := "package test\nfunc invalid( {\n"

	err = os.WriteFile(invalidFile, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create invalid test file: %v", err)
	}

	err = serializer.ValidateFileStructure(invalidFile)
	if err == nil {
		t.Error("Expected error for invalid Go file")
	}

	// Test with non-Go file (should not validate)
	nonGoFile := filepath.Join(tempDir, "test.txt")
	err = os.WriteFile(nonGoFile, []byte("not go code"), 0644)
	if err != nil {
		t.Fatalf("Failed to create non-Go test file: %v", err)
	}

	err = serializer.ValidateFileStructure(nonGoFile)
	if err != nil {
		t.Errorf("Expected no error for non-Go file, got %v", err)
	}
}

func TestSerializer_GetFileLines(t *testing.T) {
	serializer := NewSerializer()

	// Create a temporary test file
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.go")
	content := `line 1
line 2
line 3
line 4
line 5
`

	err := os.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Get specific lines
	lines, err := serializer.GetFileLines(testFile, 2, 4)
	if err != nil {
		t.Fatalf("Failed to get file lines: %v", err)
	}

	if len(lines) != 3 {
		t.Errorf("Expected 3 lines, got %d", len(lines))
	}

	expected := []string{"2: line 2", "3: line 3", "4: line 4"}
	for i, expectedLine := range expected {
		if i >= len(lines) || lines[i] != expectedLine {
			t.Errorf("Expected line %d to be '%s', got '%s'", i, expectedLine, lines[i])
		}
	}
}