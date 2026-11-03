package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestRenameSymbol_ReportsProductionImportCycleAndSessionSurvives(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/threadmcp/b\"\n\nfunc F() { b.G() }\n",
		"b/b.go": "package b\n\nimport \"example.com/threadmcp/a\"\n\nfunc G() { a.F() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for i := range 2 {
		res := renameSymbol(t, sess, map[string]any{"symbol": "F", "new_name": "H", "dry_run": true})
		if res.IsError {
			t.Fatalf("call %d: rename_symbol failed: %s", i, errText(res))
		}
		var out typeErrorsJSON

		err := json.Unmarshal([]byte(errText(res)), &out)
		if err != nil {
			t.Fatal(err)
		}

		found := false
		for _, te := range out.TypeErrors {
			found = found || strings.Contains(te.Error, "import cycle not allowed")
		}

		if !found {
			t.Fatalf("call %d: type_errors = %+v, want the import cycle", i, out.TypeErrors)
		}
	}
}

func TestRenameSymbol_ProductionSelfImportIsReportedAndSessionSurvives(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/threadmcp/a\"\n\nfunc F() { a.F() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for i := range 2 {
		res := renameSymbol(t, sess, map[string]any{"symbol": "F", "new_name": "H", "dry_run": true})
		if res.IsError {
			t.Fatalf("call %d: rename_symbol failed: %s", i, errText(res))
		}
		var out typeErrorsJSON

		err := json.Unmarshal([]byte(errText(res)), &out)
		if err != nil {
			t.Fatal(err)
		}

		found := false
		for _, te := range out.TypeErrors {
			found = found || strings.Contains(te.Error, "import cycle not allowed: example.com/threadmcp/a -> example.com/threadmcp/a")
		}

		if !found {
			t.Fatalf("call %d: type_errors = %+v, want the self-import cycle", i, out.TypeErrors)
		}
	}
}
