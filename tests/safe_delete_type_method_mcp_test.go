package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const sdModel = `package model

// Folder groups services.
type Folder struct {
	Services []Service
	Error    *Err
}

// AgsError reports the folder error.
func (f Folder) AgsError() error { return nil }

type Service struct {
	Folder string
}

func (s Service) AgsError() error { return nil }

type Err struct{}

func (e Err) Error() string { return "Folder/Svc" }

// Used is referenced from another file.
type Used struct{}
`

const sdOther = `package model

type Holder struct{ Folder int }

func Use(h Holder, u Used) int {
	_ = u
	_ = "Folder/Svc"
	return h.Folder
}
`

const sdPad = `package model

// padding so later files sit at large token offsets in the shared FileSet.
var pad1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
var pad2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
`

func writeSafeDeleteFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":           "module example.com/del\n\ngo 1.21\n",
		"model/a_pad.go":   sdPad,
		"model/b_pad.go":   sdPad[:len(sdPad)-1] + "\nvar pad3 = 3\n",
		"model/model.go":   sdModel,
		"model/other.go":   sdOther,
		"cmd/main/main.go": "package main\n\nimport \"example.com/del/model\"\n\nfunc main() { _ = model.Use(model.Holder{}, model.Used{}) }\n",
	} {
		p := filepath.Join(dir, name)

		err := os.MkdirAll(filepath.Dir(p), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(p, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}
	// a_pad.go and b_pad.go both declare pad1/pad2; make them compile.
	err := os.WriteFile(filepath.Join(dir, "model/b_pad.go"), []byte("package model\n\nvar pad3 = \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func sdCall(t *testing.T, sess *mcptest.Session, tool string, args map[string]any) (*mcpsdk.CallToolResult, string) {
	t.Helper()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}

	return res, errText(res)
}

func TestAnalyzeSymbol_CountsTypeCheckedReferencesOnly(t *testing.T) {
	dir := writeSafeDeleteFixture(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	// Used: one parameter use in other.go, one in main.go.
	used := callJSON(t, sess, "analyze_symbol", map[string]any{"symbol": "Used", "package": "model"})

	n, _ := used["reference_count"].(float64)
	if n != 2 {
		t.Errorf("Used reference_count = %v, want 2", used["reference_count"])
	}
	// Folder: only the receiver of its own method; the same-named field,
	// selector and string literal must not count.
	folder := callJSON(t, sess, "analyze_symbol", map[string]any{"symbol": "Folder", "package": "model"})

	n, _ = folder["reference_count"].(float64)
	if n != 1 {
		t.Errorf("Folder reference_count = %v, want 1 (own receiver)", folder["reference_count"])
	}
}

func TestSafeDelete_TypeDeletesItsMethods(t *testing.T) {
	dir := writeSafeDeleteFixture(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	path := filepath.Join(dir, "model", "model.go")

	dry := callJSON(t, sess, "safe_delete", map[string]any{"symbol": "Folder", "source_file": "model/model.go", "dry_run": true})

	b, ok := dry["blocking_issue"]
	if ok {
		t.Fatalf("unexpected blocking_issue: %v", b)
	}

	joined := ""

	diffs, _ := dry["diffs"].([]any)
	for _, d := range diffs {
		hunks, _ := d.(map[string]any)["hunks"].([]any)
		for _, h := range hunks {
			joined += h.(map[string]any)["old"].(string)
		}
	}

	for _, want := range []string{"type Folder struct", "func (f Folder) AgsError()"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dry-run diff missing %q:\n%s", want, joined)
		}
	}

	if strings.Contains(joined, "func (s Service) AgsError()") {
		t.Errorf("dry-run diff removes Service.AgsError:\n%s", joined)
	}

	if readFile(t, path) != sdModel {
		t.Fatal("dry run modified the file")
	}

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "Folder", "source_file": "model/model.go"})

	want := strings.Replace(sdModel, `// Folder groups services.
type Folder struct {
	Services []Service
	Error    *Err
}

// AgsError reports the folder error.
func (f Folder) AgsError() error { return nil }

`, "", 1)

	got := readFile(t, path)
	if got != want {
		t.Errorf("after delete:\n%s\nwant:\n%s", got, want)
	}
}

func TestSafeDelete_TypeWithExternalReferenceStillBlocked(t *testing.T) {
	dir := writeSafeDeleteFixture(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": "Used", "source_file": "model/model.go", "dry_run": true})
	if !res.IsError || !strings.Contains(text, "found 2 references") {
		t.Errorf("want blocked with 2 references, got error=%v text=%s", res.IsError, text)
	}
}

func TestSafeDelete_MethodBySelector(t *testing.T) {
	for _, tc := range []struct{ symbol, gone, kept string }{
		{"Folder.AgsError", "func (f Folder) AgsError()", "func (s Service) AgsError()"},
		{"Service.AgsError", "func (s Service) AgsError()", "func (f Folder) AgsError()"},
	} {
		t.Run(tc.symbol, func(t *testing.T) {
			dir := writeSafeDeleteFixture(t)

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			path := filepath.Join(dir, "model", "model.go")

			dry := callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": "model/model.go", "dry_run": true})

			b, ok := dry["blocking_issue"]
			if ok {
				t.Fatalf("unexpected blocking_issue: %v", b)
			}

			if readFile(t, path) != sdModel {
				t.Fatal("dry run modified the file")
			}

			callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": "model/model.go"})

			got := readFile(t, path)
			if strings.Contains(got, tc.gone) || !strings.Contains(got, tc.kept) || !strings.Contains(got, "type Folder struct") {
				t.Errorf("wrong content after deleting %s:\n%s", tc.symbol, got)
			}

			if strings.Count(sdModel, "\n")-strings.Count(got, "\n") > 3 {
				t.Errorf("removed too much:\n%s", got)
			}
		})
	}
}
