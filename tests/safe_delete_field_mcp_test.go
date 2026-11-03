package tests_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const sdfModel = `package model

type Widget struct {
	Name string
	// LongestFieldName is unused.
	LongestFieldName int // trailing
	ID               int
	Used             string
	Lit              string
	A, B             int
	Base
}

type Base struct {
	Promoted int
}

type Gadget struct {
	LongestFieldName int
}
`

const sdfUses = `package model

func UseGadget(g Gadget) int { return g.LongestFieldName }

func UseWidget(w Widget) string { return w.Used }

func MakeWidget() Widget { return Widget{Lit: "x"} }

func UsePromoted(w Widget) int { return w.Promoted }

func Strings() string { return "LongestFieldName" + ` + "`json:\"ID\"`" + ` }
`

func sdfSession(t *testing.T) (*mcptest.Session, string) {
	t.Helper()
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod":         "module example.com/sdf\n\ngo 1.21\n",
		"model/model.go": sdfModel,
		"model/uses.go":  sdfUses,
	})

	return mcptest.Dial(t.Context(), t, mcpTransport(), dir), filepath.Join(dir, "model", "model.go")
}

func TestSafeDelete_FieldUnreferenced_GofmtClean(t *testing.T) {
	sess, path := sdfSession(t)
	defer sess.Close()

	args := map[string]any{"symbol": "Widget.LongestFieldName", "source_file": "model/model.go"}

	dry := callJSON(t, sess, "safe_delete", map[string]any{"symbol": args["symbol"], "source_file": args["source_file"], "dry_run": true})

	b, ok := dry["blocking_issue"]
	if ok {
		t.Fatalf("unexpected blocking_issue: %v", b)
	}

	if readFile(t, path) != sdfModel {
		t.Fatal("dry run modified the file")
	}

	callJSON(t, sess, "safe_delete", args)

	want := strings.Replace(sdfModel, `	Name string
	// LongestFieldName is unused.
	LongestFieldName int // trailing
	ID               int
	Used             string
	Lit              string
	A, B             int
`, `	Name string
	ID   int
	Used string
	Lit  string
	A, B int
`, 1)

	got := readFile(t, path)
	if got != want {
		t.Errorf("after delete:\n%s\nwant:\n%s", got, want)
	}
}

func TestSafeDelete_FieldDryRunMatchesApply(t *testing.T) {
	sess, path := sdfSession(t)
	defer sess.Close()

	dry := callJSON(t, sess, "safe_delete", map[string]any{"symbol": "Widget.ID", "source_file": "model/model.go", "dry_run": true})

	diffs, _ := dry["diffs"].([]any)
	if len(diffs) != 1 {
		t.Fatalf("want one file diff, got %v", dry["diffs"])
	}

	callJSON(t, sess, "safe_delete", map[string]any{"symbol": "Widget.ID", "source_file": "model/model.go"})

	got := readFile(t, path)
	if strings.Contains(got, "\tID ") {
		t.Errorf("ID not removed:\n%s", got)
	}
}

func TestSafeDelete_FieldReferencedIsBlocked(t *testing.T) {
	for _, tc := range []struct{ symbol, name string }{
		{"Widget.Used", "selector"},
		{"Widget.Lit", "composite literal key"},
		{"Base.Promoted", "promoted access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, path := sdfSession(t)
			defer sess.Close()

			res, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": "model/model.go", "dry_run": true})
			if !res.IsError || !strings.Contains(text, "found 1 references") {
				t.Errorf("want blocked with 1 reference, got error=%v text=%s", res.IsError, text)
			}

			if readFile(t, path) != sdfModel {
				t.Fatal("file modified")
			}
		})
	}
}

func TestSafeDelete_FieldForceListsSurvivingSites(t *testing.T) {
	sess, path := sdfSession(t)
	defer sess.Close()

	out := callJSON(t, sess, "safe_delete", map[string]any{"symbol": "Widget.Used", "source_file": "model/model.go", "force": true})

	skipped, _ := out["skipped_sites"].([]any)
	if len(skipped) != 1 {
		t.Errorf("want 1 skipped site, got %v", out["skipped_sites"])
	}

	if strings.Contains(readFile(t, path), "Used") {
		t.Errorf("declaration not removed:\n%s", readFile(t, path))
	}
}

func TestSafeDelete_FieldSameNameOnOtherTypeNotCounted(t *testing.T) {
	sess, _ := sdfSession(t)
	defer sess.Close()
	// Gadget.LongestFieldName is used; Widget's is not.
	_, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": "Widget.LongestFieldName", "source_file": "model/model.go", "dry_run": true})
	if strings.Contains(text, "references") {
		t.Errorf("Widget.LongestFieldName wrongly blocked: %s", text)
	}

	res, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": "Gadget.LongestFieldName", "source_file": "model/model.go", "dry_run": true})
	if !res.IsError || !strings.Contains(text, "found 1 references") {
		t.Errorf("Gadget.LongestFieldName: want blocked with 1 reference, got %v %s", res.IsError, text)
	}
}

func TestSafeDelete_MultiNameFieldRemovesOnlyThatName(t *testing.T) {
	for _, tc := range []struct{ symbol, want string }{
		{"Widget.A", "\tB                int\n"},
		{"Widget.B", "\tA                int\n"},
	} {
		t.Run(tc.symbol, func(t *testing.T) {
			sess, path := sdfSession(t)
			defer sess.Close()

			callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": "model/model.go"})

			got := readFile(t, path)
			if strings.Contains(got, "A, B") || !strings.Contains(got, tc.want) {
				t.Errorf("wrong content after deleting %s:\n%s", tc.symbol, got)
			}
		})
	}
}

func TestSafeDelete_FieldUnknownRejected(t *testing.T) {
	sess, _ := sdfSession(t)
	defer sess.Close()

	res, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": "Widget.Nope", "source_file": "model/model.go", "dry_run": true})
	if !res.IsError || !strings.Contains(text, "no field or method") {
		t.Errorf("want clear error, got %v %s", res.IsError, text)
	}
}
