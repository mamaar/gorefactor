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

	err := serializer.ApplyChanges([]refactorTypes.Change{})
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
	err = serializer.ApplyChanges(changes)
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
	err = serializer.ApplyChanges(changes)
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
	err = serializer.ApplyChanges(changes)
	if err == nil {
		t.Error("Expected error when applying invalid change")
	}
}

func TestSerializer_PreviewChanges_NoChanges(t *testing.T) {
	serializer := NewSerializer()

	preview, err := serializer.PreviewChanges([]refactorTypes.Change{})
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

	preview, err := serializer.PreviewChanges(changes)
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

			if tc.expectError {
				return
			}
			// Check that result is valid Go code
			if !strings.HasPrefix(result, "package main") {
				t.Error("Expected formatted result to start with package declaration")
			}
		})
	}
}

func TestSerializer_truncateText(t *testing.T) {
	serializer := NewSerializer()

	testCases := []struct {
		name     string
		text     string
		expected string
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
			result := serializer.truncateText(tc.text)
			if result != tc.expected {
				t.Errorf("Expected truncateText result '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

// TestApplyChanges_RejectsRelativePaths is the backstop against the whole
// class of bug where an operation fails to resolve its destination against the
// workspace root. A relative Change.File is never intentional: os.WriteFile
// resolves it against the server process's working directory, so the write
// succeeds somewhere nobody asked for and no error is ever reported.
func TestApplyChanges_RejectsRelativePaths(t *testing.T) {
	serializer := NewSerializer()

	err := serializer.ApplyChanges([]refactorTypes.Change{
		{
			File:        filepath.Join("scratch", "pkg", "wms", "wms.go"),
			NewText:     "package wms\n",
			Description: "Move file",
		},
	})
	if err == nil {
		t.Fatal("expected ApplyChanges to reject a relative change path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should explain the absolute-path requirement, got: %v", err)
	}
}

// TestApplyChanges_RejectsBeforeMutating pins the ordering that turned the
// move_package bug into a half-move rather than a rejected plan. ApplyChanges
// iterates a map, so without an up-front check a deletion can be applied
// before the unresolvable write is ever reached — destroying the sources for a
// move that then fails to land.
func TestApplyChanges_RejectsBeforeMutating(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.go")
	err := os.WriteFile(source, []byte("package p\n"), 0644)
	if err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	// The shape of a move: write the destination, delete the source. The
	// destination path is relative (the bug), the deletion is not.
	err = serializerApplyBoth(t, []refactorTypes.Change{
		{
			File:        filepath.Join("elsewhere", "source.go"),
			NewText:     "package p\n",
			Description: "Move file to elsewhere",
		},
		{
			File:        source,
			Delete:      true,
			Description: "Remove file (moved)",
		},
	})
	if err == nil {
		t.Fatal("expected the batch to be rejected")
	}

	_, statErr := os.Stat(source)
	if statErr != nil {
		t.Errorf("source was deleted despite the batch being rejected: %v", statErr)
	}
	_, statErr = os.Stat("elsewhere")
	if statErr == nil {
		t.Error(`a relative "elsewhere" directory was created in the test's working directory`)
	}
}

func serializerApplyBoth(t *testing.T, changes []refactorTypes.Change) error {
	t.Helper()
	return NewSerializer().ApplyChanges(changes)
}

const generatedHeader = "// Code generated by stringer; DO NOT EDIT.\n\npackage p\n\nvar X = 1\n"

func writeFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func requireContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s changed:\n%s", path, got)
	}
}

func replaceXChange(file string) refactorTypes.Change {
	return refactorTypes.Change{File: file, Start: 0, End: 0, NewText: "// edited\n", Description: "Replace text"}
}

func TestApplyChanges_RefusesGeneratedFile(t *testing.T) {
	gen := writeFixture(t, t.TempDir(), "gen.go", generatedHeader)

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{replaceXChange(gen)})
	if err == nil {
		t.Fatal("expected an edit to a generated file to be refused")
	}
	if !strings.Contains(err.Error(), gen) {
		t.Errorf("error should name the file, got: %v", err)
	}
	requireContent(t, gen, generatedHeader)
}

func TestApplyChanges_GeneratedFileRefusalLeavesPlanUntouched(t *testing.T) {
	dir := t.TempDir()
	a := writeFixture(t, dir, "a.go", "package p\n")
	b := writeFixture(t, dir, "b.go", "package p\n")
	c := writeFixture(t, dir, "c.go", generatedHeader)

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		replaceXChange(a),
		{File: b, Delete: true, Description: "Remove file"},
		replaceXChange(c),
	})
	if err == nil {
		t.Fatal("expected the plan to be refused")
	}
	requireContent(t, a, "package p\n")
	requireContent(t, b, "package p\n")
	requireContent(t, c, generatedHeader)
}

func TestApplyChanges_AppliesRelocationOfGeneratedFile(t *testing.T) {
	dir := t.TempDir()
	gen := writeFixture(t, dir, "gen.go", generatedHeader)
	old := writeFixture(t, dir, "old.go", generatedHeader)

	edit := replaceXChange(gen)
	edit.Relocation = true
	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		edit,
		{File: old, Delete: true, Relocation: true, Description: "Remove file"},
	})
	if err != nil {
		t.Fatalf("relocation of a generated file should pass, got: %v", err)
	}
	requireContent(t, gen, "// edited\n"+generatedHeader)
	_, statErr := os.Stat(old)
	if statErr == nil {
		t.Error("relocated source was not deleted")
	}
}

func TestApplyChanges_AllowsCreatingFileWithGeneratedHeader(t *testing.T) {
	created := filepath.Join(t.TempDir(), "new.go")

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		{File: created, NewText: generatedHeader, Description: "Create file"},
	})
	if err != nil {
		t.Fatalf("creating a file should pass, got: %v", err)
	}
	requireContent(t, created, generatedHeader)
}

func TestApplyChanges_AllowsFileGeneratedByGorefactor(t *testing.T) {
	src := "// This file was generated by gorefactor.\n\npackage p\n"
	f := writeFixture(t, t.TempDir(), "facade.go", src)

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{replaceXChange(f)})
	if err != nil {
		t.Fatalf("gorefactor-generated file should stay editable, got: %v", err)
	}
}

func TestApplyChanges_AddedImportKeepsNeighbourComment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	src := "package a\n\nimport (\n\t\"bytes\"\n\t//nolint:gosec\n\t\"crypto/md5\"\n\t\"encoding/json\"\n)\n"
	err := os.WriteFile(path, []byte(src), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	insert := strings.Index(src, "\t\"bytes\"\n") + len("\t\"bytes\"\n")
	s := NewSerializer()
	s.SetModuleInfo("example.com/m", nil)
	err = s.ApplyChanges([]refactorTypes.Change{{File: path, Start: insert, End: insert, NewText: "\t\"context\"\n"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "package a\n\nimport (\n\t\"bytes\"\n\t\"context\"\n\t//nolint:gosec\n\t\"crypto/md5\"\n\t\"encoding/json\"\n)\n"
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyChanges_ApplyFailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	a := writeFixture(t, dir, "a.go", "package p\n")
	z := writeFixture(t, dir, "z.go", "package p\n")

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		replaceXChange(a),
		{File: z, Start: 0, End: 1, OldText: "x", NewText: "y", Description: "Replace text"},
	})
	if err == nil {
		t.Fatal("expected the plan to fail")
	}
	requireContent(t, a, "package p\n")
	requireContent(t, z, "package p\n")
}

func TestApplyChanges_RefusesResultThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	a := writeFixture(t, dir, "a.go", "package p\n")
	z := writeFixture(t, dir, "z.go", "package p\n")

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		{File: a, Start: 9, End: 9, NewText: "\nfunc {", Description: "Replace text"},
		replaceXChange(z),
	})
	if err == nil || !strings.Contains(err.Error(), a) {
		t.Fatalf("expected an error naming %s, got: %v", a, err)
	}
	requireContent(t, a, "package p\n")
	requireContent(t, z, "package p\n")
}

func TestApplyChanges_RefusesNewFileThatDoesNotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.go")

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		{File: path, NewText: "package p\n\nfunc {\n", Description: "Create file"},
	})
	if err == nil {
		t.Fatal("expected the plan to fail")
	}
	_, statErr := os.Stat(path)
	if !os.IsNotExist(statErr) {
		t.Errorf("%s was written", path)
	}
}

func TestApplyChanges_WritesEditToFileThatAlreadyDidNotParse(t *testing.T) {
	broken := "package p\n\nimport \"old/path\"\n\nfunc {\n"
	path := writeFixture(t, t.TempDir(), "broken.go", broken)
	start := strings.Index(broken, "old/path")

	err := NewSerializer().ApplyChanges([]refactorTypes.Change{
		{File: path, Start: start, End: start + len("old/path"), OldText: "old/path", NewText: "new/path", Description: "Update import path"},
	})
	if err != nil {
		t.Fatalf("ApplyChanges: %v", err)
	}
	requireContent(t, path, strings.Replace(broken, "old/path", "new/path", 1))
}

func TestApplyChanges_WriteFailureRestoresWrittenFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	for _, sub := range []string{"a", "b", "d"} {
		err := os.Mkdir(filepath.Join(dir, sub), 0o755)
		if err != nil {
			t.Fatal(err)
		}
	}
	x := writeFixture(t, dir, "a/x.go", "package a\n")
	y := writeFixture(t, dir, "b/y.go", "package b\n")
	bin := writeFixture(t, dir, "b/run.bin", "\x00\xff#!")
	err := os.Chmod(bin, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	movedBin := filepath.Join(dir, "c", "new", "run.bin")
	z := filepath.Join(dir, "c", "new", "z.go")
	w := filepath.Join(dir, "d", "w.go")
	err = os.Chmod(filepath.Join(dir, "d"), 0o555)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "d"), 0o755) })

	err = NewSerializer().ApplyChanges([]refactorTypes.Change{
		replaceXChange(x),
		{File: y, Delete: true, Description: "Remove file"},
		{File: movedBin, NewText: "\x00\xff#!", Mode: 0o755, Description: "Move file"},
		{File: bin, Delete: true, Description: "Remove file"},
		{File: z, NewText: "package new\n", Description: "Create file"},
		{File: w, NewText: "package d\n", Description: "Create file"},
	})
	if err == nil {
		t.Fatal("expected the write into the read-only directory to fail")
	}
	if !strings.Contains(err.Error(), w) {
		t.Errorf("error should name the failing file, got: %v", err)
	}
	requireContent(t, x, "package a\n")
	requireContent(t, y, "package b\n")
	requireContent(t, bin, "\x00\xff#!")
	info, statErr := os.Stat(bin)
	if statErr != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("deleted binary file not restored with its mode: %v %v", info, statErr)
	}
	_, statErr = os.Stat(filepath.Join(dir, "c"))
	if !os.IsNotExist(statErr) {
		t.Errorf("directory created for the rolled-back file was left behind: %v", statErr)
	}
}

func TestApplyChanges_NewFileGetsRequestedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.sh")
	err := NewSerializer().ApplyChanges([]refactorTypes.Change{{File: path, NewText: "#!/bin/sh\n", Mode: 0o755}})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, err %v, want 0755", info, err)
	}
}

func TestSerializer_PreviewChanges_CarriedFileIsSummarised(t *testing.T) {
	got, err := NewSerializer().PreviewChanges([]refactorTypes.Change{
		{File: "/x/blob.bin", NewText: "\x00\xff\xfe", Mode: 0o644, Relocation: true, Description: "Move file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "file moved (3 bytes)") || strings.ContainsRune(got, 0) {
		t.Errorf("preview should summarise the binary file:\n%q", got)
	}
}

func TestApplyChanges_LeavesTestdataGoFileUnformatted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testdata", "a.go")
	src := "package a\nfunc  f( ) {\n"
	err := NewSerializer().ApplyChanges([]refactorTypes.Change{{File: path, NewText: src}})
	if err != nil {
		t.Fatal(err)
	}
	requireContent(t, path, src)
}
