package tests_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type unusedItemJSON struct {
	Name           string   `json:"name"`
	File           string   `json:"file"`
	Reason         string   `json:"reason"`
	SafeToDelete   bool     `json:"safe_to_delete"`
	Configs        []string `json:"configs"`
	ReferencedFrom []struct {
		Name string `json:"name"`
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"referenced_from"`
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

// callUnused calls the unused tool with args, none when nil.
func callUnused(t *testing.T, args map[string]any, files map[string]string) (unusedJSON, map[string]any) {
	t.Helper()

	if args == nil {
		args = map[string]any{}
	}

	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "unused", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("unused: %v %s", err, errText(res))
	}
	var out unusedJSON

	var raw map[string]any
	for _, v := range []any{&out, &raw} {
		err := json.Unmarshal([]byte(errText(res)), v)
		if err != nil {
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
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go":        "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n",
		"p/use_tagx.go": "//go:build tagx\n\npackage p\n\nfunc Use() int { return f(1) }\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f.go:dead"}) {
		t.Errorf("unused = %v, want only dead", got)
	}

	if len(out.BuildConfigs) != 2 {
		t.Errorf("build_configs = %v, want 2 entries", out.BuildConfigs)
	}
}

func TestUnused_FunctionDeclaredAndUnusedOnlyUnderTagIsReportedWithItsConfig(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/plain.go":  sdcPlain,
		"p/f_tagx.go": ctxFTagx,
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f_tagx.go:f"}) {
		t.Fatalf("unused = %v, want f in f_tagx.go", got)
	}

	got = out.Unused[0].Configs
	if !reflect.DeepEqual(got, []string{"tags=tagx"}) {
		t.Errorf("configs = %v, want [tags=tagx]", got)
	}
}

func TestUnused_SiblingUsedUnderItsTagKeepsBothUnreported(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/use_tagx.go":  sdcUseTagx,
	})

	got := declarations(out.Unused)
	if len(got) != 0 {
		t.Errorf("unused = %v, want none: f is used under tagx", got)
	}
}

func TestUnused_UnusedSiblingsAreReportedPerDeclaration(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f_default.go": ctxFDefault,
		"p/f_tagx.go":    ctxFTagx,
		"p/plain.go":     sdcPlain,
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f_default.go:f", "f_tagx.go:f"}) {
		t.Fatalf("unused = %v, want both declarations of f", got)
	}

	for _, it := range out.Unused {
		if len(it.Configs) != 1 {
			t.Errorf("%s: configs = %v, want the one configuration declaring it", it.File, it.Configs)
		}
	}
}

func TestUnused_NameMentionedOnlyInUncheckedFileIsFlaggedPossiblyUsed(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go":             "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n",
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nfunc TestBad(t *testing.T) {\n\tf(3)\n}\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f.go:dead"}) {
		t.Errorf("unused = %v, want only dead", got)
	}

	got = declarations(out.PossiblyUsed)
	if !reflect.DeepEqual(got, []string{"f.go:f"}) {
		t.Fatalf("possibly_used = %v, want f", got)
	}

	use := out.PossiblyUsed[0].PossibleUse
	if use == nil || filepath.Base(use.File) != "bad_tagx_test.go" || use.Line != 12 || !reflect.DeepEqual(use.Configs, []string{"tags=tagx"}) {
		t.Errorf("possible_use = %+v, want bad_tagx_test.go:12 under tags=tagx", use)
	}
}

func TestUnused_SingleConfigOutputHasNoConfigFields(t *testing.T) {
	out, raw := callUnused(t, nil, map[string]string{
		"p/f.go": "package p\n\nfunc f(n int) int { return n }\n\nfunc dead() {}\n\nvar idle = 1\n\nfunc Use() int { return f(1) }\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f.go:dead", "f.go:idle"}) || out.Count != 2 {
		t.Errorf("unused = %v (count %d), want dead and idle", got, out.Count)
	}

	for _, key := range []string{"build_configs", "possibly_used"} {
		_, ok := raw[key]
		if ok {
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

func TestUnused_BlankIdentifierIsNotReported(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go": "package p\n\ntype T struct{}\n\nvar _ = T{}\n\nconst _ = 1\n\nfunc dead() {}\n\nvar V T\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f.go:dead"}) {
		t.Errorf("unused = %v, want only dead", got)
	}
}

func TestUnused_DeclarationInUncheckedFileIsNotItsOwnPossibleUse(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go":             "package p\n\nfunc F() {}\n",
		"p/bad_tagx_test.go": "//go:build tagx\n\npackage p\n\nimport (\n\t\"testing\"\n\n\t_ \"example.com/this-module-does-not-exist/thirdparty\"\n)\n\nvar lonely = 1\n\nvar twice = 2\n\nfunc TestBad(t *testing.T) {\n\t_ = twice\n}\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"bad_tagx_test.go:lonely"}) {
		t.Errorf("unused = %v, want only lonely", got)
	}

	for _, it := range out.PossiblyUsed {
		if it.Name == "lonely" {
			t.Errorf("lonely is possibly used at %+v, want its declaration not to count", it.PossibleUse)
		}
	}
}

const testOnlyChain = `package p

type Doer interface{ Do() }

type Requester struct{}

func (r *Requester) Do() {}

var defaultRequester = NewRequester()

func NewRequester() *Requester { return &Requester{} }

func Fetch() {
	var d Doer = defaultRequester
	d.Do()
}
`

func itemNamed(items []unusedItemJSON, name string) *unusedItemJSON {
	for i := range items {
		if items[i].Name == name {
			return &items[i]
		}
	}

	return nil
}

func TestUnused_ExportedFunctionCalledOnlyFromTestIsReportedWithIncludeExported(t *testing.T) {
	files := map[string]string{
		"p/f.go":      "package p\n\nfunc Helper() {}\n",
		"p/f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { Helper() }\n",
	}

	byDefault, _ := callUnused(t, nil, files)
	withExported, _ := callUnused(t, map[string]any{"include_exported": true}, files)

	got := declarations(byDefault.Unused)
	if len(got) != 0 {
		t.Errorf("default unused = %v, want none", got)
	}

	helper := itemNamed(withExported.Unused, "Helper")
	if helper == nil {
		t.Fatalf("unused = %v, want Helper", declarations(withExported.Unused))
	}

	if helper.Reason != "Only referenced from tests" || helper.SafeToDelete {
		t.Errorf("Helper: reason %q, safe %v, want only tests and not safe", helper.Reason, helper.SafeToDelete)
	}
}

func TestUnused_RequesterTypeConstructorDefaultVarMethodAndInterfaceBehindTestOnlyFunctionAreAllReported(t *testing.T) {
	out, _ := callUnused(t, map[string]any{"include_exported": true}, map[string]string{
		"p/f.go":      testOnlyChain,
		"p/f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestFetch(t *testing.T) { Fetch() }\n",
	})

	want := []string{"f.go:Do", "f.go:Doer", "f.go:Fetch", "f.go:NewRequester", "f.go:Requester", "f.go:defaultRequester"}

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unused = %v, want %v", got, want)
	}

	for _, it := range out.Unused {
		if it.SafeToDelete {
			t.Errorf("%s is safe to delete, want not safe", it.Name)
		}
	}
}

func TestUnused_UnexportedFunctionCalledOnlyFromTestAndItsCalleeAreReportedByDefault(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go":      "package p\n\nfunc helper() { leaf() }\n\nfunc leaf() {}\n",
		"p/f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { helper() }\n",
	})

	got := declarations(out.Unused)
	if !reflect.DeepEqual(got, []string{"f.go:helper", "f.go:leaf"}) {
		t.Fatalf("unused = %v, want helper and leaf", got)
	}

	if got := itemNamed(out.Unused, "helper").Reason; got != "Only referenced from tests" {
		t.Errorf("helper reason = %q", got)
	}

	if got := itemNamed(out.Unused, "leaf").Reason; got != "Only referenced from unused code" {
		t.Errorf("leaf reason = %q", got)
	}
}

func TestUnused_ReferencedFromListsTheTestDeclarationThatUsesTheSymbol(t *testing.T) {
	out, _ := callUnused(t, nil, map[string]string{
		"p/f.go":      "package p\n\nfunc helper() {}\n",
		"p/f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) { helper() }\n",
	})

	helper := itemNamed(out.Unused, "helper")
	if helper == nil || len(helper.ReferencedFrom) != 1 {
		t.Fatalf("helper = %+v, want one referenced_from entry", helper)
	}

	ref := helper.ReferencedFrom[0]
	if ref.Name != "TestHelper" || filepath.Base(ref.File) != "f_test.go" || ref.Line != 5 {
		t.Errorf("referenced_from = %+v, want TestHelper at f_test.go:5", ref)
	}
}
