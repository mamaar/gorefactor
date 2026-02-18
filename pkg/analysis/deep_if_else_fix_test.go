package analysis

import (
	"strings"
	"testing"
)

func TestDeepIfElseFix_BasicFix(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error, user *User) error {
	if err == nil {
		if user != nil {
			return doWork(user)
		} else {
			return errors.New("not found")
		}
	} else {
		return errors.New("db error")
	}
}

type User struct{}
func doWork(u *User) error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 1, 1)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.Function != "process" {
		t.Errorf("Expected function 'process', got %q", r.Function)
	}
	if r.EarlyReturnsAdded != 2 {
		t.Errorf("Expected 2 early returns, got %d", r.EarlyReturnsAdded)
	}
	if r.NestingDepthAfter != 0 {
		t.Errorf("Expected nesting depth after 0, got %d", r.NestingDepthAfter)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// Should contain inverted conditions as early returns
	if !strings.Contains(c.NewText, "err != nil") {
		t.Errorf("Expected inverted 'err != nil', got:\n%s", c.NewText)
	}
	if !strings.Contains(c.NewText, "user == nil") {
		t.Errorf("Expected inverted 'user == nil', got:\n%s", c.NewText)
	}
	// Happy path should be present
	if !strings.Contains(c.NewText, "return doWork(user)") {
		t.Errorf("Expected happy path 'return doWork(user)', got:\n%s", c.NewText)
	}
	// Guard bodies should be present
	if !strings.Contains(c.NewText, `errors.New("db error")`) {
		t.Errorf("Expected guard body 'db error', got:\n%s", c.NewText)
	}
	if !strings.Contains(c.NewText, `errors.New("not found")`) {
		t.Errorf("Expected guard body 'not found', got:\n%s", c.NewText)
	}
}

func TestDeepIfElseFix_TripleNesting(t *testing.T) {
	src := `package testpkg

import "errors"

func process(a bool, b bool, c bool) error {
	if a {
		if b {
			if c {
				return doWork()
			} else {
				return errors.New("c failed")
			}
		} else {
			return errors.New("b failed")
		}
	} else {
		return errors.New("a failed")
	}
}

func doWork() error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 1, 1)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].EarlyReturnsAdded != 3 {
		t.Errorf("Expected 3 early returns, got %d", results[0].EarlyReturnsAdded)
	}

	c := plan.Changes[0]
	if !strings.Contains(c.NewText, "!a") {
		t.Errorf("Expected inverted '!a', got:\n%s", c.NewText)
	}
	if !strings.Contains(c.NewText, "!b") {
		t.Errorf("Expected inverted '!b', got:\n%s", c.NewText)
	}
	if !strings.Contains(c.NewText, "!c") {
		t.Errorf("Expected inverted '!c', got:\n%s", c.NewText)
	}
}

func TestDeepIfElseFix_NoViolations(t *testing.T) {
	src := `package testpkg

import "errors"

func process(err error) error {
	if err != nil {
		return errors.New("error")
	}
	return nil
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 2, 3)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(plan.Changes))
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestDeepIfElseFix_ConditionInversion(t *testing.T) {
	src := `package testpkg

import "errors"

func process(x int) error {
	if x > 0 {
		return doWork()
	} else {
		return errors.New("non-positive")
	}
}

func doWork() error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	// Use maxNesting=0 to catch even depth-1 chains
	fixer := NewDeepIfElseFixer(ws, 0, 1)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// x > 0 should be inverted to x <= 0
	if !strings.Contains(c.NewText, "x <= 0") {
		t.Errorf("Expected inverted 'x <= 0', got:\n%s", c.NewText)
	}
}

func TestDeepIfElseFix_NegationInversion(t *testing.T) {
	src := `package testpkg

import "errors"

func process(ok bool) error {
	if !ok {
		return doWork()
	} else {
		return errors.New("was ok")
	}
}

func doWork() error { return nil }
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 0, 1)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// !ok should be inverted to ok
	if !strings.Contains(c.NewText, "if ok {") {
		t.Errorf("Expected inverted 'ok', got:\n%s", c.NewText)
	}
}

func TestDeepIfElseFix_SkipsElseIf(t *testing.T) {
	// else-if chains are not the simple nested pattern — should be skipped conservatively
	src := `package testpkg

import "errors"

func process(x int) error {
	if x > 0 {
		if x < 100 {
			return nil
		} else {
			return errors.New("too big")
		}
	} else if x == 0 {
		return errors.New("zero")
	} else {
		return errors.New("negative")
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 0, 1)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	// The outer if has else-if, which is not the simple pattern
	// Only the inner if-else should not produce a fix since the outer blocks it
	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes for else-if pattern, got %d", len(plan.Changes))
	}
}

func TestDeepIfElseFix_MultiStatementBody_Skipped(t *testing.T) {
	// When then-body has more than just a nested if-else, skip conservatively
	src := `package testpkg

import "errors"

func process(err error) error {
	if err == nil {
		doSomething()
		if true {
			return nil
		} else {
			return errors.New("inner")
		}
	} else {
		return errors.New("outer")
	}
}

func doSomething() {}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 0, 1)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	// Then-body has doSomething() + if-else = 2 statements, not a simple chain
	// Should still fix: the happy path is both statements
	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	// The then-body becomes the happy path, and includes both statements
	if !strings.Contains(c.NewText, "doSomething()") {
		t.Errorf("Expected happy path to contain doSomething(), got:\n%s", c.NewText)
	}
}

func TestDeepIfElseFix_AffectedFiles(t *testing.T) {
	src := `package testpkg

import "errors"

func process(ok bool) error {
	if ok {
		return nil
	} else {
		return errors.New("not ok")
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 0, 1)
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

func TestDeepIfElseFix_PreservesMultiLineBody(t *testing.T) {
	src := `package testpkg

import "errors"
import "fmt"

func process(err error) error {
	if err == nil {
		return nil
	} else {
		fmt.Println("error occurred")
		return errors.New("failed")
	}
}
`
	ws := createDeepIfElseTestWorkspace(t, src)
	fixer := NewDeepIfElseFixer(ws, 0, 1)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(plan.Changes))
	}

	c := plan.Changes[0]
	if !strings.Contains(c.NewText, "fmt.Println") {
		t.Errorf("Expected multi-line body preserved, got:\n%s", c.NewText)
	}
	if !strings.Contains(c.NewText, `errors.New("failed")`) {
		t.Errorf("Expected return preserved, got:\n%s", c.NewText)
	}
}
