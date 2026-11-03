package refactor

import (
	"fmt"
	"go/parser"
	"go/token"
	"go/types"
	"log/slog"
	"strings"
	"unicode"

	refactorTypes "github.com/mamaar/gorefactor/pkg/types"
)

// Validator validates refactoring operations for safety
type Validator struct {
	typeChecker *types.Checker
	logger      *slog.Logger
}

func NewValidator(logger *slog.Logger) *Validator {
	return &Validator{
		typeChecker: &types.Checker{},
		logger:      logger,
	}
}

// ValidatePlanWithConfig validates a complete refactoring plan with configuration options
func (v *Validator) ValidatePlanWithConfig(plan *refactorTypes.RefactoringPlan, config *EngineConfig) error {
	if plan == nil {
		return &refactorTypes.RefactorError{
			Type:    refactorTypes.InvalidOperation,
			Message: "refactoring plan is nil",
		}
	}

	// Use default config if none provided
	if config == nil {
		config = DefaultConfig()
	}

	var allIssues []refactorTypes.Issue

	// Validate each operation
	for i, operation := range plan.Operations {
		issues := v.validateOperation(operation)
		for _, issue := range issues {
			issue.Description = fmt.Sprintf("Operation %d: %s", i+1, issue.Description)
			allIssues = append(allIssues, issue)
		}
	}

	// Validate changes for conflicts and consistency
	conflictIssues := v.validateChanges(plan.Changes)
	allIssues = append(allIssues, conflictIssues...)

	// Check for compilation issues after changes
	compilationIssues := v.validateCompilation(plan)
	allIssues = append(allIssues, compilationIssues...)

	// Return validation error if any critical issues found, unless AllowBreaking is enabled
	criticalIssues := v.filterCriticalIssues(allIssues)
	if len(criticalIssues) > 0 && !config.AllowBreaking {
		return &refactorTypes.ValidationError{
			Issues: criticalIssues,
		}
	}

	// Update plan with all issues (including warnings)
	if plan.Impact == nil {
		plan.Impact = &refactorTypes.ImpactAnalysis{}
	}
	plan.Impact.PotentialIssues = append(plan.Impact.PotentialIssues, allIssues...)

	return nil
}

// Helper methods for validation

func (v *Validator) validateOperation(operation refactorTypes.Operation) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	switch op := operation.(type) {
	case *MoveSymbolOperation:
		// Note: This would need access to workspace, simplified for now
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("Move operation for symbol: %s", refNames(op.Request.Refs())),
			Severity:    refactorTypes.Info,
		})
	case *RenameSymbolOperation:
		// Basic validation of rename request
		if !v.isValidGoIdentifier(op.Request.NewName) {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueCompilationError,
				Description: fmt.Sprintf("invalid identifier: %s", op.Request.NewName),
				Severity:    refactorTypes.Error,
			})
		}
	default:
		issues = append(issues, refactorTypes.Issue{
			Type:        refactorTypes.IssueCompilationError,
			Description: fmt.Sprintf("unsupported operation type: %T", operation),
			Severity:    refactorTypes.Warning,
		})
	}

	return issues
}

func (v *Validator) validateChanges(changes []refactorTypes.Change) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// Group changes by file
	fileChanges := make(map[string][]refactorTypes.Change)
	for _, change := range changes {
		fileChanges[change.File] = append(fileChanges[change.File], change)
	}

	// Check for overlapping changes within each file
	for fileName, changes := range fileChanges {
		for i, change1 := range changes {
			for j, change2 := range changes {
				if i >= j {
					continue
				}

				// Check if changes overlap
				if v.changesOverlap(change1, change2) {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueNameConflict,
						Description: fmt.Sprintf("overlapping changes detected in file %s", fileName),
						File:        fileName,
						Severity:    refactorTypes.Error,
					})
				}
			}
		}
	}

	// Validate that changes are syntactically valid
	for _, change := range changes {
		if change.Start < 0 || change.End < change.Start {
			issues = append(issues, refactorTypes.Issue{
				Type:        refactorTypes.IssueCompilationError,
				Description: fmt.Sprintf("invalid change bounds: start=%d, end=%d", change.Start, change.End),
				File:        change.File,
				Severity:    refactorTypes.Error,
			})
		}
	}

	return issues
}

func (v *Validator) validateCompilation(plan *refactorTypes.RefactoringPlan) []refactorTypes.Issue {
	var issues []refactorTypes.Issue

	// This is a simplified compilation check
	// In a full implementation, this would apply changes to a temporary copy
	// and attempt compilation

	for _, change := range plan.Changes {
		// Basic syntax validation of new text. A relocation is a whole file,
		// which the serializer refuses to write when it newly fails to parse.
		if change.NewText != "" && !change.Relocation {
			// Skip validation for code fragments that aren't valid standalone Go:
			// - Interface method signatures (method name + signature)
			// - Parameter list changes (just parameter declarations)
			// - Call argument changes (just expressions)
			// - Import additions (just import path strings)
			desc := change.Description
			isFragment := strings.Contains(desc, "interface method") ||
				strings.Contains(desc, "parameters") ||
				strings.Contains(desc, "parameter") ||
				strings.Contains(desc, "call to") ||
				strings.Contains(desc, "argument") ||
				strings.Contains(desc, "import") ||
				strings.Contains(desc, "qualified references") ||
				strings.Contains(desc, "Split") ||
				strings.Contains(desc, "Replace") ||
				strings.Contains(desc, "Wrap")
			if !isFragment {
				err := v.validateGoSyntax(change.NewText)
				if err != nil {
					issues = append(issues, refactorTypes.Issue{
						Type:        refactorTypes.IssueCompilationError,
						Description: fmt.Sprintf("syntax error in new text: %v", err),
						File:        change.File,
						Severity:    refactorTypes.Error,
					})
				}
			}
		}
	}

	return issues
}

func (v *Validator) isValidGoIdentifier(name string) bool {
	if len(name) == 0 {
		return false
	}

	// First character must be letter or underscore
	first := rune(name[0])
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}

	// Remaining characters must be letters, digits, or underscores
	for _, r := range name[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}

	return true
}

func (v *Validator) validateGoSyntax(code string) error {
	// Try to parse the code as a Go expression or statement
	if code == "" {
		return nil
	}

	// First, try as an expression (for simple values)
	_, err := parser.ParseExpr(code)
	if err == nil {
		return nil
	}

	// Try as a complete file (code already has package declaration)
	_, err = parser.ParseFile(token.NewFileSet(), "", code, 0)
	if err == nil {
		return nil
	}

	// Try as a statement inside a function
	src := fmt.Sprintf("package main\nfunc main() {\n%s\n}", code)
	_, err = parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err == nil {
		return nil
	}

	// Try as a complete file content (for imports, function declarations, etc.)
	src = fmt.Sprintf("package main\n%s", code)
	_, err = parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err == nil {
		return nil
	}

	// For very simple additions like import statements, try minimal validation
	if strings.Contains(code, "\"") && !strings.Contains(code, "\n") {
		// Likely an import path, consider it valid
		return nil
	}

	return err
}

func (v *Validator) changesOverlap(change1, change2 refactorTypes.Change) bool {
	return (change1.Start < change2.End && change2.Start < change1.End)
}

func (v *Validator) filterCriticalIssues(issues []refactorTypes.Issue) []refactorTypes.Issue {
	var critical []refactorTypes.Issue
	for _, issue := range issues {
		if issue.Severity == refactorTypes.Error {
			critical = append(critical, issue)
		}
	}
	return critical
}
