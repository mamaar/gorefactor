package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type unusedItemJSON struct {
	Name        string   `json:"name"`
	File        string   `json:"file"`
	Configs     []string `json:"configs"`
	PossibleUse *struct {
		File    string   `json:"file"`
		Line    int      `json:"line"`
		Configs []string `json:"configs"`
		Reason  string   `json:"reason"`
	} `json:"possible_use"`
}

type unusedJSON struct {
	Unused       []unusedItemJSON `json:"unused_symbols"`
	Count        int              `json:"count"`
	PossiblyUsed []unusedItemJSON `json:"possibly_used"`
	BuildConfigs []string         `json:"build_configs"`
}

func callUnused(t *testing.T, files map[string]string) (unusedJSON, map[string]any) {
	t.Helper()
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "unused", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("unused: %v %s", err, errText(res))
	}
	var out unusedJSON
	var raw map[string]any
	for _, v := range []any{&out, &raw} {
		if err := json.Unmarshal([]byte(errText(res)), v); err != nil {
			t.Fatalf("decode: %v\n%s", err, errText(res))
		}
	}
	return out, raw
}

// declarations returns "file:name" for each item, sorted.
func declarations(items []unusedItemJSON) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, filepath.Base(it.File)+":"+it.Name)
	}
	sort.Strings(out)
	return out
}

func TestUnused_FunctionUsedOnlyFromTaggedFileIsNotReported(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/f.go":        "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n",
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
	})

	if got := declarations(out.Unused); !reflect.DeepEqual(got, []string{"f.go:dead"}) {
		t.Errorf("unused = %v, want only dead", got)
	}
	if len(out.BuildConfigs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out.BuildConfigs)
	}
}

func TestUnused_FunctionDeclaredAndUnusedOnlyUnderTagIsReportedWithItsConfig(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/plain.go":  sdcPlain,
		"p/f_tagx.go": ctxFTagx,
	})

	if got := declarations(out.Unused); !reflect.DeepEqual(got, []string{"f_tagx.go:f"}) {
		t.Fatalf("unused = %v, want f in f_tagx.go", got)
	}
	if got := out.Unused[0].Configs; !reflect.DeepEqual(got, []string{"tags=tagx"}) {
		t.Errorf("configs = %v, want [tags=tagx]", got)
	}
}

func TestUnused_SiblingUsedUnderItsTagKeepsBothUnreported(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use_tagx.go":  sdcUseTagx,
	})

	if got := declarations(out.Unused); len(got) != 0 {
		t.Errorf("unused = %v, want none: f is used under tagx", got)
	}
}

func TestUnused_UnusedSiblingsAreReportedPerDeclaration(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/plain.go":     sdcPlain,
	})

	if got := declarations(out.Unused); !reflect.DeepEqual(got, []string{"f_default.go:f", "f_tagx.go:f"}) {
		t.Fatalf("unused = %v, want both declarations of f", got)
	}
	for _, it := range out.Unused {
		if len(it.Configs) != 1 {
			t.Errorf("%s: configs = %v, want the one configuration declaring it", it.File, it.Configs)
		}
	}
}

func TestUnused_NameMentionedOnlyInUncheckedFileIsFlaggedPossiblyUsed(t *testing.T) {
	out, _ := callUnused(t, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n",
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\tf(3)\n}\n",
	})

	if got := declarations(out.Unused); !reflect.DeepEqual(got, []string{"f.go:dead"}) {
		t.Errorf("unused = %v, want only dead", got)
	}
	if got := declarations(out.PossiblyUsed); !reflect.DeepEqual(got, []string{"f.go:f"}) {
		t.Fatalf("possibly_used = %v, want f", got)
	}
	use := out.PossiblyUsed[0].PossibleUse
	if use == nil || filepath.Base(use.File) != "bad_tagx_test.go" || use.Line != 12 || !reflect.DeepEqual(use.Configs, []string{"tags=tagx"}) {
		t.Errorf("possible_use = %+v, want bad_tagx_test.go:12 under tags=tagx", use)
	}
}

func TestUnused_SingleConfigOutputHasNoConfigFields(t *testing.T) {
	out, raw := callUnused(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n\nvar idle = 1\n\nfunc Use() int { return f(1) }\n",
	})

	if got := declarations(out.Unused); !reflect.DeepEqual(got, []string{"f.go:dead", "f.go:idle"}) || out.Count != 2 {
		t.Errorf("unused = %v (count %d), want dead and idle", got, out.Count)
	}
	for _, key := range []string{"build_configs", "possibly_used"} {
		if _, ok := raw[key]; ok {
			t.Errorf("%s = %v, want the field omitted", key, raw[key])
		}
	}
	for _, item := range raw["unused_symbols"].([]any) {
		want := []string{"exported", "file", "kind", "line", "name", "package", "reason", "safe_to_delete"}
		var got []string
		for k := range item.(map[string]any) {
			got = append(got, k)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("item fields = %v, want %v", got, want)
		}
	}
}
