package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const derefTarget = "package p\n\nfunc sum(b *[]int, k int) int {\n\ttotal := 0\n\tfor _, v := range *b {\n\t\ttotal += v + k\n\t}\n\treturn total + len(*b)\n}\n"

func derefArgs(extra map[string]any) map[string]any {
	args := map[string]any{
		"function_name": "sum",
		"source_file":   "p/f.go",
		"subcommand":    "deref_param",
		"params": []map[string]any{
			{"name": "b", "type": "[]int"},
			{"name": "k", "type": "int"},
		},
	}
	maps.Copy(args, extra)
	return args
}

func TestChangeSignature_DerefParamRewritesDeclBodyAndCallSites(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   derefTarget,
		"p/use.go": "package p\n\nfunc Use(ptr *[]int) int {\n\tx := []int{1}\n\ta := sum(&x, 1)\n\tb := sum(ptr, 2)\n\tc := sum(&[]int{3}, 3)\n\treturn a + b + c\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	f := fileText(t, dir, "p/f.go")
	for _, want := range []string{"func sum(b []int, k int) int", "range b {", "len(b)"} {
		if !strings.Contains(f, want) {
			t.Errorf("f.go missing %q:\n%s", want, f)
		}
	}
	if strings.Contains(f, "*b") {
		t.Errorf("f.go still dereferences b:\n%s", f)
	}
	for _, want := range []string{"sum(x, 1)", "sum(*ptr, 2)", "sum([]int{3}, 3)"} {
		assertFileContains(t, dir, "p/use.go", want)
	}
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamParenthesizesNonPrimaryArgument(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   derefTarget,
		"p/use.go": "package p\n\nfunc pick(a, b *[]int, c bool) int {\n\treturn sum(map[bool]*[]int{true: a, false: b}[c], 1)\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	assertFileContains(t, dir, "p/use.go", "sum(*map[bool]*[]int{true: a, false: b}[c], 1)")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamRefusesNonDerefUseOfParam(t *testing.T) {
	cases := map[string]string{
		"passed on":  "package p\n\nfunc other(b *[]int) int { return len(*b) }\n\nfunc sum(b *[]int, k int) int { return other(b) }\n",
		"assigned":   "package p\n\nfunc sum(b *[]int, k int) int {\n\tb = nil\n\treturn k\n}\n",
		"address":    "package p\n\nfunc sum(b *[]int, k int) int {\n\tpp := &b\n\treturn len(**pp)\n}\n",
		"write thru": "package p\n\nfunc sum(b *[]int, k int) int {\n\t*b = nil\n\treturn k\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"p/f.go":   src,
				"p/use.go": "package p\n\nfunc Use() int {\n\tx := []int{1}\n\treturn sum(&x, 1)\n}\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, derefArgs(nil))

			if !res.IsError || !strings.Contains(errText(res), "deref_param") {
				t.Errorf("want deref_param refusal, got IsError=%v %q", res.IsError, errText(res))
			}
			if got := fileText(t, dir, "p/f.go"); got != src {
				t.Errorf("f.go changed:\n%s", got)
			}
			assertFileContains(t, dir, "p/use.go", "sum(&x, 1)")
		})
	}
}

func TestChangeSignature_DerefParamIgnoresShadowingLocal(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc sum(b *[]int, k int) int {\n\tt := len(*b)\n\tif true {\n\t\tb := &k\n\t\tt += *b\n\t}\n\treturn t\n}\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\tx := []int{1}\n\treturn sum(&x, 1)\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	assertFileContains(t, dir, "p/f.go", "len(b)")
	assertFileContains(t, dir, "p/f.go", "t += *b")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamReportsFuncValueReferenceAsSkipped(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   derefTarget,
		"p/use.go": "package p\n\nvar fn = sum\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := changeSigOK(t, sess, derefArgs(nil))

	if sites, _ := out["skipped_sites"].([]any); len(sites) != 1 {
		t.Errorf("skipped_sites = %v, want 1 entry", out["skipped_sites"])
	}
}

func derefNamed(fn string, params ...map[string]any) map[string]any {
	return map[string]any{"function_name": fn, "source_file": "p/f.go", "subcommand": "deref_param", "params": params}
}

func param(name, typ string) map[string]any { return map[string]any{"name": name, "type": typ} }

func runDeref(t *testing.T, files map[string]string, args map[string]any) (dir string, res *mcpsdk.CallToolResult) {
	t.Helper()
	dir = writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	t.Cleanup(func() { sess.Close() })
	return dir, changeSig(t, sess, args)
}

func wantDerefRefusal(t *testing.T, dir string, res *mcpsdk.CallToolResult, files map[string]string, contains string) {
	t.Helper()
	if !res.IsError || !strings.Contains(errText(res), "deref_param") || !strings.Contains(errText(res), contains) {
		t.Errorf("want deref_param refusal containing %q, got IsError=%v %q", contains, res.IsError, errText(res))
	}
	for path, src := range files {
		if got := fileText(t, dir, path); got != src {
			t.Errorf("%s changed:\n%s", path, got)
		}
	}
}

func TestChangeSignature_DerefParamFoldsNilChecks(t *testing.T) {
	cases := []struct {
		name, params, cond, caller, want string
	}{
		{"map nil or nil", "m *map[string]int", "m == nil || *m == nil", "&x", "if m == nil {"},
		{"slice nonnil and nonnil", "m *[]int", "m != nil && *m != nil", "&x", "if m != nil {"},
		{"empty or nil", "m *[]int", "m == nil || len(*m) == 0", "&x", "if len(m) == 0 {"},
		{"nonempty gt", "m *[]int", "m != nil && len(*m) > 0", "&x", "if len(m) > 0 {"},
		{"nonempty ne", "m *[]int", "m != nil && len(*m) != 0", "&x", "if len(m) != 0 {"},
		{"in longer or chain", "m *[]int", "k == 1 || m == nil || len(*m) == 0", "&x", "if k == 1 || len(m) == 0 {"},
		{"in longer and chain", "m *[]int", "k == 1 && m != nil && len(*m) > 0 && k < 5", "&x", "if k == 1 && len(m) > 0 && k < 5 {"},
		{"nil literal caller", "m *[]int", "m == nil || len(*m) == 0", "nil", "if len(m) == 0 {"},
		{"composite caller", "m *[]int", "m == nil || *m == nil", "&[]int{1}", "if m == nil {"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typ := strings.TrimPrefix(strings.Fields(c.params)[1], "*")
			f := "package p\n\nfunc g(" + c.params + ", k int) int {\n\tif " + c.cond + " {\n\t\treturn 0\n\t}\n\treturn len(*m)\n}\n"
			use := "package p\n\nfunc Use() int {\n\tvar x " + typ + "\n\t_ = x\n\treturn g(" + c.caller + ", 1)\n}\n"
			dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("g", param("m", typ), param("k", "int")))
			if res.IsError {
				t.Fatalf("unexpected error: %s", errText(res))
			}
			assertFileContains(t, dir, "p/f.go", c.want)
			assertFileContains(t, dir, "p/f.go", "return len(m)")
			want := "g(x, 1)"
			if c.caller == "nil" {
				want = "g(nil, 1)"
			} else if strings.HasPrefix(c.caller, "&[]") {
				want = "g([]int{1}, 1)"
			}
			assertFileContains(t, dir, "p/use.go", want)
			goVets(t, dir, "")
		})
	}
}

func TestChangeSignature_DerefParamNilCheckRealCases(t *testing.T) {
	t.Run("getPlantCounts", func(t *testing.T) {
		f := "package p\n\nfunc getPlantCounts(report *map[string]interface{}) float64 {\n\tif report == nil || *report == nil {\n\t\treturn 0\n\t}\n\tvar t float64\n\tfor k := range *report {\n\t\tif v, ok := (*report)[k].(float64); ok {\n\t\t\tt += v\n\t\t}\n\t}\n\treturn t\n}\n"
		use := "package p\n\nfunc Use() float64 {\n\treportI := map[string]interface{}{}\n\treturn getPlantCounts(&reportI)\n}\n"
		dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("getPlantCounts", param("report", "map[string]interface{}")))
		if res.IsError {
			t.Fatalf("unexpected error: %s", errText(res))
		}
		assertFileContains(t, dir, "p/f.go", "if report == nil {")
		assertFileContains(t, dir, "p/f.go", "report[k]")
		assertFileContains(t, dir, "p/use.go", "getPlantCounts(reportI)")
		goVets(t, dir, "")
	})
	t.Run("isPersistedPoEqual", func(t *testing.T) {
		f := "package p\n\nimport \"bytes\"\n\ntype Planner struct{}\n\nfunc f() ([]byte, error) { return nil, nil }\n\nfunc (p *Planner) isPersistedPoEqual(headID string, order *[]byte) bool {\n\tcached, err := f()\n\tif err != nil || cached == nil || order == nil {\n\t\treturn false\n\t}\n\treturn bytes.Equal(cached, *order)\n}\n"
		use := "package p\n\nfunc (p *Planner) Use(order []byte) bool {\n\treturn p.isPersistedPoEqual(\"h\", &order)\n}\n"
		dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("Planner.isPersistedPoEqual", param("headID", "string"), param("order", "[]byte")))
		if res.IsError {
			t.Fatalf("unexpected error: %s", errText(res))
		}
		assertFileContains(t, dir, "p/f.go", "if err != nil || cached == nil {")
		assertFileContains(t, dir, "p/f.go", "bytes.Equal(cached, order)")
		assertFileContains(t, dir, "p/use.go", "p.isPersistedPoEqual(\"h\", order)")
		goVets(t, dir, "")
	})
	t.Run("refuse bare nil check with pointer variable caller", func(t *testing.T) {
		files := map[string]string{
			"p/f.go":   "package p\n\ntype Item struct{}\ntype Card struct{}\n\nfunc m(items *[]Item) *Card {\n\tif items == nil {\n\t\treturn nil\n\t}\n\t_ = len(*items)\n\treturn &Card{}\n}\n",
			"p/use.go": "package p\n\nfunc Use(data *[]Item) *Card {\n\treturn m(data)\n}\n",
		}
		dir, res := runDeref(t, files, derefNamed("m", param("items", "[]Item")))
		wantDerefRefusal(t, dir, res, files, "use.go:4")
	})
	t.Run("refuse combined check with pointer variable caller", func(t *testing.T) {
		files := map[string]string{
			"p/f.go":   "package p\n\ntype Feature struct{}\n\nfunc h(ctx int, features *[]Feature) {\n\tif features == nil || len(*features) == 0 {\n\t\treturn\n\t}\n\t_ = (*features)[0]\n}\n",
			"p/use.go": "package p\n\nfunc Use(agsFeatures *[]Feature) {\n\th(1, agsFeatures)\n}\n",
		}
		dir, res := runDeref(t, files, derefNamed("h", param("ctx", "int"), param("features", "[]Feature")))
		wantDerefRefusal(t, dir, res, files, "may be nil")
	})
}

func TestChangeSignature_DerefParamBareNilCheckFolds(t *testing.T) {
	use := "package p\n\nfunc Use() int {\n\tvar x string\n\treturn g(&x, 1)\n}\n"
	cases := []struct {
		name, body, want, notWant string
	}{
		{"if deleted", "\tif s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n", "func g(s string, k int) int {\n\treturn len(s)\n}", "s == nil"},
		{"or operand dropped", "\tif k == 0 || s == nil || len(*s) == 0 {\n\t\treturn 0\n\t}\n\treturn k\n", "if k == 0 || len(s) == 0 {", "s == nil"},
		{"trailing or operands dropped", "\tif k == 0 || s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n", "if k == 0 {", "s == nil"},
		{"and operand dropped", "\tif s != nil && len(*s) > k {\n\t\treturn 0\n\t}\n\treturn 1\n", "if len(s) > k {", "s != nil"},
		{"all or operands fold, if deleted", "\tif s == nil || s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n", "func g(s string, k int) int {\n\treturn len(s)\n}", "s == nil"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := "package p\n\nfunc g(s *string, k int) int {\n" + c.body + "}\n"
			dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("g", param("s", "string"), param("k", "int")))
			if res.IsError {
				t.Fatalf("unexpected error: %s", errText(res))
			}
			assertFileContains(t, dir, "p/f.go", c.want)
			if got := fileText(t, dir, "p/f.go"); strings.Contains(got, c.notWant) {
				t.Errorf("f.go still contains %q:\n%s", c.notWant, got)
			}
			assertFileContains(t, dir, "p/use.go", "g(x, 1)")
			goVets(t, dir, "")
		})
	}
}

func TestChangeSignature_DerefParamRefusesUnfoldableNilChecks(t *testing.T) {
	bodies := map[string]string{
		"whole condition true": "\tif s != nil {\n\t\treturn len(*s)\n\t}\n\treturn 0\n",
		"if with else":         "\tif s == nil {\n\t\treturn 0\n\t} else {\n\t\treturn len(*s)\n\t}\n",
		"if with init":         "\tif _ = k; s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n",
		"assigned":             "\tmissing := s == nil\n\t_ = missing\n\treturn len(*s)\n",
		"returned":             "\t_ = len(*s)\n\treturn map[bool]int{true: 1}[s == nil]\n",
		"false operand in and": "\tif k == 1 && s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n",
		"true operand in or":   "\tif k == 1 || s != nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n",
		"negated":              "\tif !(s == nil) {\n\t\treturn len(*s)\n\t}\n\treturn 0\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"p/f.go":   "package p\n\nfunc g(s *string, k int) int {\n" + body + "}\n",
				"p/use.go": "package p\n\nfunc Use() int {\n\tvar x string\n\treturn g(&x, 1)\n}\n",
			}
			dir, res := runDeref(t, files, derefNamed("g", param("s", "string"), param("k", "int")))
			wantDerefRefusal(t, dir, res, files, "f.go:")
		})
	}
}

func TestChangeSignature_DerefParamNilCheckCallSiteGuard(t *testing.T) {
	bare := "package p\n\nfunc g(s *string, k int) int {\n\tif s == nil {\n\t\treturn 0\n\t}\n\treturn len(*s)\n}\n"
	combined := "package p\n\nfunc g(s *[]int, k int) int {\n\tif s == nil || len(*s) == 0 {\n\t\treturn 0\n\t}\n\treturn len(*s)\n}\n"
	cases := []struct {
		name, f, use, typ string
	}{
		{"bare with nil literal", bare, "package p\n\nfunc Use() int {\n\treturn g(nil, 1)\n}\n", "string"},
		{"bare with function value", bare, "package p\n\nvar fn = g\n", "string"},
		{"combined with expression", combined, "package p\n\nfunc Use(s *[]int) int {\n\treturn g(s, 1)\n}\n", "[]int"},
		{"combined with function value", combined, "package p\n\nvar fn = g\n", "[]int"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"p/f.go": c.f, "p/use.go": c.use}
			dir, res := runDeref(t, files, derefNamed("g", param("s", c.typ), param("k", "int")))
			wantDerefRefusal(t, dir, res, files, "use.go:")
		})
	}
}

func TestChangeSignature_DerefParamAcceptsElementWritesOfSliceAndMap(t *testing.T) {
	cases := []struct{ name, typ, body, want string }{
		{"map assign", "map[string]int", "(*p)[\"a\"] = 1", "p[\"a\"] = 1"},
		{"map op-assign", "map[string]int", "(*p)[\"a\"] += 1", "p[\"a\"] += 1"},
		{"map incdec", "map[string]int", "(*p)[\"a\"]++", "p[\"a\"]++"},
		{"slice incdec", "[]int", "(*p)[0]--", "p[0]--"},
		{"multi-assign", "[]int", "(*p)[0], (*p)[1] = 1, 2", "p[0], p[1] = 1, 2"},
		{"element field", "[]E", "(*p)[0].f = 1", "p[0].f = 1"},
		{"element address", "[]int", "sink(&(*p)[0])", "sink(&p[0])"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := "package p\n\ntype E struct{ f int }\n\nfunc sink(*int) {}\n\nfunc g(p *" + c.typ + ") {\n\t" + c.body + "\n}\n"
			use := "package p\n\nfunc Use() {\n\tx := " + c.typ + "{}\n\t_ = x\n\tg(&x)\n}\n"
			if strings.HasPrefix(c.typ, "[]") {
				use = strings.Replace(use, "{}\n", "{{}, {}}\n", 1)
				if c.typ == "[]E" {
					use = strings.Replace(use, "{{}, {}}", "{{}}", 1)
				}
				if c.typ == "[]int" {
					use = strings.Replace(use, "{{}, {}}", "{1, 2}", 1)
				}
			}
			dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("g", param("p", c.typ)))
			if res.IsError {
				t.Fatalf("unexpected error: %s", errText(res))
			}
			assertFileContains(t, dir, "p/f.go", c.want)
			assertFileContains(t, dir, "p/use.go", "g(x)")
			goVets(t, dir, "")
		})
	}
}

func TestChangeSignature_DerefParamRefusesWritesThatNeedTheCaller(t *testing.T) {
	cases := map[string]string{
		"reassign slice":     "func g(p *[]int) { *p = nil }\n",
		"reassign by slice":  "func g(p *[]int) { *p = (*p)[1:] }\n",
		"address of pointee": "func g(p *[]int) { _ = &*p }\n",
		"array element":      "func g(p *[2]int) { (*p)[0] = 1 }\n",
		"array incdec":       "func g(p *[2]int) { (*p)[0]++ }\n",
		"array element addr": "func g(p *[2]int) { _ = &(*p)[0] }\n",
		"struct field":       "func g(p *struct{ f int }) { (*p).f = 1 }\n",
		"struct slice field": "func g(p *struct{ f []int }) { (*p).f[0] = 1 }\n",
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			_, afterStar, _ := strings.Cut(decl, "*")
			typ, _, _ := strings.Cut(afterStar, ")")
			files := map[string]string{
				"p/f.go":   "package p\n\n" + decl,
				"p/use.go": "package p\n\nfunc Use() {\n\tvar x " + typ + "\n\tg(&x)\n}\n",
			}
			dir, res := runDeref(t, files, derefNamed("g", param("p", typ)))
			wantDerefRefusal(t, dir, res, files, "f.go:")
		})
	}
}

func TestChangeSignature_DerefParamRealElementWriteCases(t *testing.T) {
	f := "package p\n\ntype W struct {\n\tKey   string\n\tValue []string\n}\n\nfunc fill(attributes *map[string]string, w W) {\n\t(*attributes)[w.Key] = w.Value[0]\n}\n\nfunc Reportify(m *map[string]interface{}, opts ...string) {}\n\nfunc each(m *[]map[string]interface{}, opts ...string) {\n\tfor i := range *m {\n\t\tReportify(&(*m)[i], opts...)\n\t}\n}\n"
	use := "package p\n\nfunc Use() {\n\tattrs := map[string]string{}\n\tfill(&attrs, W{Key: \"k\", Value: []string{\"v\"}})\n\tms := []map[string]interface{}{{}}\n\teach(&ms)\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefNamed("fill", param("attributes", "map[string]string"), param("w", "W")))
	changeSigOK(t, sess, derefNamed("each", param("m", "[]map[string]interface{}"), param("opts", "...string")))

	assertFileContains(t, dir, "p/f.go", "attributes[w.Key] = w.Value[0]")
	assertFileContains(t, dir, "p/f.go", "range m {")
	assertFileContains(t, dir, "p/f.go", "Reportify(&m[i], opts...)")
	assertFileContains(t, dir, "p/use.go", "each(ms)")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamVariadicCallSites(t *testing.T) {
	f := "package p\n\nfunc put(key string, bs *[]byte, collection ...string) error { return nil }\n"
	use := "package p\n\nfunc Use(ptr *[]byte, rest []string) {\n\tx := []byte{1}\n\t_ = put(\"a\", &x)\n\t_ = put(\"a\", &x, \"c1\")\n\t_ = put(\"a\", &x, \"c1\", \"c2\")\n\t_ = put(\"a\", ptr, rest...)\n}\n"
	dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("put", param("key", "string"), param("bs", "[]byte"), param("collection", "...string")))
	if res.IsError {
		t.Fatalf("unexpected error: %s", errText(res))
	}
	assertFileContains(t, dir, "p/f.go", "func put(key string, bs []byte, collection ...string) error")
	for _, want := range []string{"put(\"a\", x)", "put(\"a\", x, \"c1\")", "put(\"a\", x, \"c1\", \"c2\")", "put(\"a\", *ptr, rest...)"} {
		assertFileContains(t, dir, "p/use.go", want)
	}
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamKeepsUntouchedParamGroups(t *testing.T) {
	f := "package p\n\nfunc g(includeA, includeB bool, newStuff, existingStuff *[]int) {\n\t_ = len(*newStuff)\n}\n"
	use := "package p\n\nfunc Use() {\n\ta, b := []int{}, []int{}\n\tg(true, false, &a, &b)\n}\n"
	dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use},
		derefNamed("g", param("includeA", "bool"), param("includeB", "bool"), param("newStuff", "[]int"), param("existingStuff", "*[]int")))
	if res.IsError {
		t.Fatalf("unexpected error: %s", errText(res))
	}
	assertFileContains(t, dir, "p/f.go", "func g(includeA, includeB bool, newStuff []int, existingStuff *[]int)")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamDropsParenthesesAroundDereferenceOnly(t *testing.T) {
	f := "package p\n\nfunc g(b *[]int, k int) int {\n\tx := ((*b))[k:]\n\treturn (len(*b)+1)*2 + len(x)\n}\n"
	use := "package p\n\nfunc Use() int {\n\tx := []int{1}\n\treturn g(&x, 0)\n}\n"
	dir, res := runDeref(t, map[string]string{"p/f.go": f, "p/use.go": use}, derefNamed("g", param("b", "[]int"), param("k", "int")))
	if res.IsError {
		t.Fatalf("unexpected error: %s", errText(res))
	}
	assertFileContains(t, dir, "p/f.go", "x := b[k:]")
	assertFileContains(t, dir, "p/f.go", "(len(b)+1)*2")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamAcceptsRecursivePassThrough(t *testing.T) {
	f := "package p\n\nfunc expr(r int, fields *map[string]bool, k string) string {\n\tif fields == nil || *fields == nil {\n\t\treturn k\n\t}\n\tif r > 0 {\n\t\treturn expr(r-1, fields, k)\n\t}\n\treturn k + expr(0, fields, k)\n}\n\ntype T struct{}\n\nfunc (T) m(fields *[]int) int {\n\tif len(*fields) == 0 {\n\t\treturn 0\n\t}\n\treturn T{}.m(fields)\n}\n"
	use := "package p\n\nfunc Use() {\n\tfs := map[string]bool{}\n\t_ = expr(1, &fs, \"k\")\n\tls := []int{1}\n\t_ = T{}.m(&ls)\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefNamed("expr", param("r", "int"), param("fields", "map[string]bool"), param("k", "string")))
	changeSigOK(t, sess, derefNamed("T.m", param("fields", "[]int")))

	assertFileContains(t, dir, "p/f.go", "return expr(r-1, fields, k)")
	assertFileContains(t, dir, "p/f.go", "k + expr(0, fields, k)")
	assertFileContains(t, dir, "p/f.go", "return T{}.m(fields)")
	assertFileContains(t, dir, "p/use.go", "expr(1, fs, \"k\")")
	assertFileContains(t, dir, "p/use.go", "T{}.m(ls)")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamRefusesPassThroughThatIsNotRecursive(t *testing.T) {
	cases := map[string]struct {
		decl string
		args map[string]any
	}{
		"other function": {
			"func other(x *[]int, y int) int { return len(*x) }\n\nfunc g(p *[]int, k int) int { return other(p, k) }\n",
			derefNamed("g", param("p", "[]int"), param("k", "int")),
		},
		"different position": {
			"func g(p *[]int, q *[]int) int { return g(q, p) }\n",
			derefNamed("g", param("p", "[]int"), param("q", "*[]int")),
		},
		"same-named other": {
			"type T struct{}\n\nfunc (T) g(p *[]int) int { return 0 }\n\nfunc g(p *[]int) int { return T{}.g(p) }\n",
			derefNamed("g", param("p", "[]int")),
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"p/f.go": "package p\n\n" + tt.decl, "p/use.go": "package p\n\nfunc Use() {\n\tvar x []int\n\t_ = x\n}\n"}
			dir, res := runDeref(t, files, tt.args)
			wantDerefRefusal(t, dir, res, files, "f.go:")
		})
	}
}
