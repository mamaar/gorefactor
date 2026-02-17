package analysis

import (
	"strings"
	"testing"
)

func TestIfInitFix_BasicFix(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		return err
	}
	_ = x
	return nil
}

func bar() (int, error) { return 0, nil }
`
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if c.File != "testpkg.go" {
		t.Errorf("Expected file 'testpkg.go', got %q", c.File)
	}
	if !strings.Contains(c.OldText, "if x, err := bar(); err != nil {") {
		t.Errorf("OldText doesn't contain expected if-init: %q", c.OldText)
	}
	if !strings.Contains(c.NewText, "x, err := bar()") {
		t.Errorf("NewText doesn't contain assignment: %q", c.NewText)
	}
	if !strings.Contains(c.NewText, "if err != nil {") {
		t.Errorf("NewText doesn't contain if-check: %q", c.NewText)
	}
	// Verify assignment comes before if-check
	assignIdx := strings.Index(c.NewText, "x, err := bar()")
	ifIdx := strings.Index(c.NewText, "if err != nil {")
	if assignIdx >= ifIdx {
		t.Errorf("Assignment should come before if-check in NewText: %q", c.NewText)
	}
}

func TestIfInitFix_MultipleViolations(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		return err
	}
	_ = x

	if y, err := baz(); err != nil {
		return err
	}
	_ = y
	return nil
}

func bar() (int, error) { return 0, nil }
func baz() (int, error) { return 0, nil }
`
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 2 {
		t.Fatalf("Expected 2 changes, got %d", len(plan.Changes))
	}

	// Verify changes don't overlap
	if plan.Changes[0].End > plan.Changes[1].Start {
		t.Error("Changes overlap")
	}

	if len(plan.AffectedFiles) != 1 {
		t.Errorf("Expected 1 affected file, got %d", len(plan.AffectedFiles))
	}
}

func TestIfInitFix_IndentationPreservation(t *testing.T) {
	src := "package testpkg\n\nfunc foo() error {\n\tif x, err := bar(); err != nil {\n\t\treturn err\n\t}\n\t_ = x\n\treturn nil\n}\n\nfunc bar() (int, error) { return 0, nil }\n"
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// The new text should preserve the tab indentation
	lines := strings.Split(c.NewText, "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 lines in NewText, got %d: %q", len(lines), c.NewText)
	}
	// First line: assignment (no leading indent since it replaces from the "if" position)
	if !strings.HasPrefix(lines[0], "x, err := bar()") {
		t.Errorf("First line should be assignment, got %q", lines[0])
	}
	// Second line: indented if-check
	if !strings.HasPrefix(lines[1], "\tif err != nil {") {
		t.Errorf("Second line should be tab-indented if-check, got %q", lines[1])
	}
}

func TestIfInitFix_NoViolations(t *testing.T) {
	src := `package testpkg

func foo() error {
	x, err := bar()
	if err != nil {
		return err
	}
	_ = x
	return nil
}

func bar() (int, error) { return 0, nil }
`
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(plan.Changes))
	}
}

func TestIfInitFix_NestedIfInit(t *testing.T) {
	src := `package testpkg

func foo() error {
	if x, err := bar(); err != nil {
		if y, err2 := baz(); err2 != nil {
			_ = y
			return err2
		}
		_ = x
		return err
	}
	return nil
}

func bar() (int, error) { return 0, nil }
func baz() (int, error) { return 0, nil }
`
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 2 {
		t.Fatalf("Expected 2 changes (nested), got %d", len(plan.Changes))
	}

	// Verify both changes produce valid transformations
	for i, c := range plan.Changes {
		if !strings.Contains(c.NewText, "if ") {
			t.Errorf("Change %d NewText missing if-check: %q", i, c.NewText)
		}
		if !strings.Contains(c.NewText, ":=") {
			t.Errorf("Change %d NewText missing assignment: %q", i, c.NewText)
		}
	}
}

func TestIfInitFix_MultilineExpression(t *testing.T) {
	src := `package testpkg

func foo() error {
	if result, err := someFunc(
		arg1, arg2,
	); err != nil {
		return err
	}
	_ = result
	return nil
}

func someFunc(a, b int) (int, error) { return 0, nil }
`
	ws := createIfInitTestWorkspace(t, src)
	fixer := NewIfInitFixer(ws)
	plan, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if !strings.Contains(c.NewText, "result, err := someFunc") {
		t.Errorf("NewText doesn't contain multiline assignment: %q", c.NewText)
	}
	if !strings.Contains(c.NewText, "if err != nil {") {
		t.Errorf("NewText doesn't contain if-check: %q", c.NewText)
	}
}
