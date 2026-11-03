package tests_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func goVet(t *testing.T, dir string) {
	t.Helper()

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
}

func TestFixErrorWrapping_AddsFmtImportToFileWithoutIt(t *testing.T) {
	dir, _ := callToolOnFixture(t, "fix_error_wrapping_nofmt", "fix_error_wrapping", map[string]any{})

	got := readFile(t, filepath.Join(dir, "main.go"))
	if strings.Count(got, `"fmt"`) != 1 || !strings.Contains(got, `fmt.Errorf("run: %w", err)`) {
		t.Errorf("want one fmt import and the wrapped returns:\n%s", got)
	}

	goVet(t, dir)
}

func TestFixErrorWrapping_ExcludeFunctionsListsThemAsSkipped(t *testing.T) {
	dir, result := callToolOnFixture(t, "fix_error_wrapping_nofmt", "fix_error_wrapping",
		map[string]any{"exclude_functions": []string{"skipMe", "T.run"}})

	got := readFile(t, filepath.Join(dir, "main.go"))
	if !strings.Contains(got, "\t\treturn err\n\t}\n\treturn nil\n}\n\ntype T") || strings.Contains(got, `"run: `) || strings.Contains(got, `"skip me: `) {
		t.Errorf("excluded functions were modified:\n%s", got)
	}

	if !strings.Contains(got, `fmt.Errorf("parse: %w", err)`) {
		t.Errorf("parse was not wrapped:\n%s", got)
	}

	skipped, _ := result["skipped"].([]any)
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want the two excluded functions", result["skipped"])
	}

	for _, s := range skipped {
		reason := s.(map[string]any)["skip_reason"]
		if reason != "excluded" {
			t.Errorf("skip_reason = %v, want excluded", reason)
		}
	}

	goVet(t, dir)
}

func TestFixErrorWrapping_DryRunReportsTypeErrorAsBlockingIssue(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_error_wrapping_typeerr", "fix_error_wrapping", map[string]any{"dry_run": true})

	issue, _ := result["blocking_issue"].(string)
	if !strings.Contains(issue, "type errors") {
		t.Errorf("blocking_issue = %q, want the introduced type error", issue)
	}
}

func TestFixErrorWrapping_ApplyRefusesPlanThatWouldNotTypeCheck(t *testing.T) {
	dir := copyFixture(t, "fix_error_wrapping_typeerr")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "fix_error_wrapping", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || !strings.Contains(errText(result), "type errors") {
		t.Fatalf("expected the apply refused, got IsError=%v: %s", result.IsError, errText(result))
	}

	got := readFile(t, filepath.Join(dir, "main.go"))
	if got != readFile(t, filepath.Join("testdata", "fix_error_wrapping_typeerr", "main.go")) {
		t.Errorf("refused apply modified the file:\n%s", got)
	}
}
