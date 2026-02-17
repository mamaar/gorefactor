package analysis

import (
	"strings"
	"testing"
)

func TestErrorWrappingFix_BareReturn(t *testing.T) {
	src := `package testpkg

import "fmt"

func CreateOrder() error {
	err := doWork()
	if err != nil {
		return err
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if result.ErrorsWrapped != 1 {
		t.Errorf("Expected 1 error wrapped, got %d", result.ErrorsWrapped)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if c.OldText != "err" {
		t.Errorf("Expected old text 'err', got %q", c.OldText)
	}
	if !strings.Contains(c.NewText, `fmt.Errorf("create order: %w", err)`) {
		t.Errorf("Expected wrapped error, got %q", c.NewText)
	}
}

func TestErrorWrappingFix_BareReturnMultiReturn(t *testing.T) {
	src := `package testpkg

import "fmt"

func GetUser() (*User, error) {
	u, err := fetchUser()
	if err != nil {
		return nil, err
	}
	return u, nil
}

type User struct{}
func fetchUser() (*User, error) { return nil, nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if result.ErrorsWrapped != 1 {
		t.Errorf("Expected 1 error wrapped, got %d", result.ErrorsWrapped)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// Should only replace the err identifier, not the whole return
	if c.OldText != "err" {
		t.Errorf("Expected old text 'err', got %q", c.OldText)
	}
	if !strings.Contains(c.NewText, `fmt.Errorf(`) {
		t.Errorf("Expected fmt.Errorf wrapping, got %q", c.NewText)
	}
	if !strings.Contains(c.NewText, "%w") {
		t.Errorf("Expected %%w verb, got %q", c.NewText)
	}
}

func TestErrorWrappingFix_FormatVerbV(t *testing.T) {
	src := `package testpkg

import "fmt"

func GetUser() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("query failed: %v", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if result.FormatVerbsFixed != 1 {
		t.Errorf("Expected 1 format verb fixed, got %d", result.FormatVerbsFixed)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if !strings.Contains(c.OldText, "%v") {
		t.Errorf("Expected old text with %%v, got %q", c.OldText)
	}
	if !strings.Contains(c.NewText, "%w") {
		t.Errorf("Expected new text with %%w, got %q", c.NewText)
	}
	// Should preserve the existing message
	if !strings.Contains(c.NewText, "query failed") {
		t.Errorf("Expected preserved message, got %q", c.NewText)
	}
}

func TestErrorWrappingFix_NoContext(t *testing.T) {
	src := `package testpkg

import "fmt"

func SaveData() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("error: %w", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityWarning)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if result.ContextsAdded != 1 {
		t.Errorf("Expected 1 context added, got %d", result.ContextsAdded)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if !strings.Contains(c.NewText, "save data") {
		t.Errorf("Expected context 'save data', got %q", c.NewText)
	}
	if !strings.Contains(c.NewText, "%w") {
		t.Errorf("Expected %%w preserved, got %q", c.NewText)
	}
}

func TestErrorWrappingFix_NoViolations(t *testing.T) {
	src := `package testpkg

import "fmt"

func CreateOrder() error {
	err := doWork()
	if err != nil {
		return fmt.Errorf("create order in database: %w", err)
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityInfo)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(plan.Changes))
	}
	if result.ErrorsWrapped != 0 || result.FormatVerbsFixed != 0 || result.ContextsAdded != 0 {
		t.Error("Expected all counters to be 0")
	}
}

func TestErrorWrappingFix_MultipleViolations(t *testing.T) {
	src := `package testpkg

import "fmt"

func Process() error {
	err := step1()
	if err != nil {
		return err
	}
	err = step2()
	if err != nil {
		return fmt.Errorf("failed: %v", err)
	}
	return nil
}

func step1() error { return nil }
func step2() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, result, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 2 {
		t.Fatalf("Expected 2 changes, got %d", len(plan.Changes))
	}
	if result.ErrorsWrapped != 1 {
		t.Errorf("Expected 1 error wrapped, got %d", result.ErrorsWrapped)
	}
	if result.FormatVerbsFixed != 1 {
		t.Errorf("Expected 1 format verb fixed, got %d", result.FormatVerbsFixed)
	}
}

func TestErrorWrappingFix_AffectedFiles(t *testing.T) {
	src := `package testpkg

func Bad() error {
	err := doWork()
	if err != nil {
		return err
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.AffectedFiles) != 1 {
		t.Errorf("Expected 1 affected file, got %d", len(plan.AffectedFiles))
	}
	if len(plan.AffectedFiles) > 0 && plan.AffectedFiles[0] != "testpkg.go" {
		t.Errorf("Expected 'testpkg.go', got %q", plan.AffectedFiles[0])
	}
}

func TestErrorWrappingFix_ContextFromFunctionName(t *testing.T) {
	src := `package testpkg

func FetchUserProfile() error {
	err := doWork()
	if err != nil {
		return err
	}
	return nil
}

func doWork() error { return nil }
`
	ws := createErrorWrappingTestWorkspace(t, src)
	fixer := NewErrorWrappingFixer(ws, SeverityCritical)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if !strings.Contains(c.NewText, "fetch user profile") {
		t.Errorf("Expected context from function name, got %q", c.NewText)
	}
}
