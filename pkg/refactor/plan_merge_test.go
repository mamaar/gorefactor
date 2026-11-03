package refactor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func ins(file string, at int, text, desc string) types.Change {
	return types.Change{File: file, Start: at, End: at, NewText: text, Description: desc}
}

func repl(file string, start, end int, old, text, desc string) types.Change {
	return types.Change{File: file, Start: start, End: end, OldText: old, NewText: text, Description: desc}
}

func planOf(changes ...types.Change) *types.RefactoringPlan {
	var files []string

	seen := map[string]bool{}
	for _, c := range changes {
		if !seen[c.File] {
			seen[c.File] = true
			files = append(files, c.File)
		}
	}

	return &types.RefactoringPlan{Changes: changes, AffectedFiles: files, Reversible: true}
}

func TestMergeConfigPlans_Changes(t *testing.T) {
	const f = "/ws/a.go"

	tests := []struct {
		name  string
		plans []ConfigPlan
		want  []types.Change
	}{
		{
			name: "identical insertions at same offset collapse",
			plans: []ConfigPlan{
				{"default", planOf(ins(f, 5, "x", "d"))},
				{"tags=fts5", planOf(ins(f, 5, "x", "d"))},
			},
			want: []types.Change{ins(f, 5, "x", "d")},
		},
		{
			name: "identical replacements collapse",
			plans: []ConfigPlan{
				{"default", planOf(repl(f, 1, 4, "old", "new", "d"))},
				{"GOOS=windows", planOf(repl(f, 1, 4, "old", "new", "d"))},
			},
			want: []types.Change{repl(f, 1, 4, "old", "new", "d")},
		},
		{
			name: "same insertion twice in one config keeps two",
			plans: []ConfigPlan{
				{"default", planOf(ins(f, 5, "x", "d"), ins(f, 5, "x", "d"))},
			},
			want: []types.Change{ins(f, 5, "x", "d"), ins(f, 5, "x", "d")},
		},
		{
			name: "multiset max when another config emits it once",
			plans: []ConfigPlan{
				{"a", planOf(ins(f, 5, "x", "d"))},
				{"b", planOf(ins(f, 5, "x", "d"), ins(f, 5, "x", "d"))},
				{"c", planOf(ins(f, 5, "x", "d"))},
			},
			want: []types.Change{ins(f, 5, "x", "d"), ins(f, 5, "x", "d")},
		},
		{
			name: "different insertions at same offset in one config keep both",
			plans: []ConfigPlan{
				{"default", planOf(ins(f, 5, "x", "d1"), ins(f, 5, "y", "d2"))},
				{"other", planOf(ins(f, 5, "x", "d1"), ins(f, 5, "y", "d2"))},
			},
			want: []types.Change{ins(f, 5, "x", "d1"), ins(f, 5, "y", "d2")},
		},
		{
			name: "overlapping changes inside one config keep both",
			plans: []ConfigPlan{
				{"default", planOf(repl(f, 1, 6, "", "a", "d1"), repl(f, 4, 8, "", "b", "d2"))},
			},
			want: []types.Change{repl(f, 1, 6, "", "a", "d1"), repl(f, 4, 8, "", "b", "d2")},
		},
		{
			name: "change present in one config only is kept",
			plans: []ConfigPlan{
				{"default", planOf(ins(f, 5, "x", "d"))},
				{"GOOS=windows", planOf(ins(f, 5, "x", "d"), ins("/ws/a_windows.go", 0, "y", "w"))},
			},
			want: []types.Change{ins(f, 5, "x", "d"), ins("/ws/a_windows.go", 0, "y", "w")},
		},
		{
			name: "same delete from two configs collapses",
			plans: []ConfigPlan{
				{"a", planOf(types.Change{File: f, Delete: true, Description: "rm"})},
				{"b", planOf(types.Change{File: f, Delete: true, Description: "rm"})},
			},
			want: []types.Change{{File: f, Delete: true, Description: "rm"}},
		},
		{
			name: "first-seen description and old text survive",
			plans: []ConfigPlan{
				{"a", planOf(repl(f, 1, 4, "abc", "new", "first"))},
				{"b", planOf(repl(f, 1, 4, "different", "new", "second"))},
			},
			want: []types.Change{repl(f, 1, 4, "abc", "new", "first")},
		},
		{
			name: "adjacent insertion and replacement do not conflict",
			plans: []ConfigPlan{
				{"a", planOf(ins(f, 3, "x", "i"))},
				{"b", planOf(repl(f, 3, 8, "", "y", "r"))},
			},
			want: []types.Change{ins(f, 3, "x", "i"), repl(f, 3, 8, "", "y", "r")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MergeConfigPlans(tt.plans)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(got.Changes, tt.want) {
				t.Errorf("changes = %+v, want %+v", got.Changes, tt.want)
			}
		})
	}
}

func TestMergeConfigPlans_Conflicts(t *testing.T) {
	const f = "/ws/a.go"

	tests := []struct {
		name     string
		plans    []ConfigPlan
		contains []string
	}{
		{
			name: "different insertions at same offset",
			plans: []ConfigPlan{
				{"default", planOf(ins(f, 5, "x", "add-param"))},
				{"tags=fts5", planOf(ins(f, 5, "y", "add-arg"))},
			},
			contains: []string{"default", "tags=fts5", "add-param", "add-arg", f},
		},
		{
			name: "overlapping replacements",
			plans: []ConfigPlan{
				{"default", planOf(repl(f, 1, 6, "", "a", "rename-one"))},
				{"GOOS=windows", planOf(repl(f, 4, 8, "", "b", "rename-two"))},
			},
			contains: []string{"default", "GOOS=windows", "rename-one", "rename-two", "[1,6)", "[4,8)"},
		},
		{
			name: "delete versus edit",
			plans: []ConfigPlan{
				{"default", planOf(types.Change{File: f, Delete: true, Description: "rm-file"})},
				{"tags=x", planOf(ins(f, 5, "x", "edit"))},
			},
			contains: []string{"default", "tags=x", "rm-file", "edit", f},
		},
		{
			name: "edit versus delete",
			plans: []ConfigPlan{
				{"tags=x", planOf(ins(f, 5, "x", "edit"))},
				{"default", planOf(types.Change{File: f, Delete: true, Description: "rm-file"})},
			},
			contains: []string{"default", "tags=x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MergeConfigPlans(tt.plans)
			if err == nil {
				t.Fatal("expected error")
			}

			for _, s := range tt.contains {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q missing %q", err, s)
				}
			}

			_, err2 := MergeConfigPlans(tt.plans)
			if err2 == nil || err2.Error() != err.Error() {
				t.Errorf("error not deterministic: %v vs %v", err, err2)
			}
		})
	}
}

func TestMergeConfigPlans_OtherFields(t *testing.T) {
	s1 := types.SkippedSite{File: "/ws/a.go", Line: 1, Reason: "r1", Snippet: "s1"}
	s2 := types.SkippedSite{File: "/ws/b.go", Line: 2, Reason: "r2", Snippet: "s2"}
	p1 := &types.RefactoringPlan{
		AffectedFiles: []string{"/ws/a.go", "/ws/b.go"},
		SkippedSites:  []types.SkippedSite{s1},
		Reversible:    true,
		Impact:        &types.ImpactAnalysis{},
	}
	p2 := &types.RefactoringPlan{
		AffectedFiles: []string{"/ws/c.go", "/ws/a.go"},
		SkippedSites:  []types.SkippedSite{s2, s1},
		Reversible:    false,
	}

	got, err := MergeConfigPlans([]ConfigPlan{{"a", p1}, {"b", p2}})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"/ws/a.go", "/ws/b.go", "/ws/c.go"}
	if !reflect.DeepEqual(got.AffectedFiles, want) {
		t.Errorf("AffectedFiles = %v, want %v", got.AffectedFiles, want)
	}

	if want := []types.SkippedSite{s1, s2}; !reflect.DeepEqual(got.SkippedSites, want) {
		t.Errorf("SkippedSites = %v, want %v", got.SkippedSites, want)
	}

	if got.Reversible {
		t.Error("Reversible = true, want false when any plan is irreversible")
	}

	if got.Impact != p1.Impact {
		t.Error("Impact is not the first non-nil one")
	}
}

func TestMergeConfigPlans_Inputs(t *testing.T) {
	const f = "/ws/a.go"

	_, err := MergeConfigPlans(nil)
	if err == nil {
		t.Error("zero plans: expected error")
	}

	_, err = MergeConfigPlans([]ConfigPlan{{"a", nil}, {"b", nil}})
	if err == nil {
		t.Error("all-nil plans: expected error")
	}

	got, err := MergeConfigPlans([]ConfigPlan{{"a", nil}, {"b", planOf(ins(f, 1, "x", "d"))}, {"c", nil}})
	if err != nil {
		t.Fatal(err)
	}

	want := []types.Change{ins(f, 1, "x", "d")}
	if !reflect.DeepEqual(got.Changes, want) {
		t.Errorf("changes = %+v, want %+v", got.Changes, want)
	}

	p := planOf(ins(f, 5, "x", "d"), ins(f, 5, "x", "d"), repl(f, 1, 3, "ab", "c", "r"))

	self, err := MergeConfigPlans([]ConfigPlan{{"a", p}, {"b", p}})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(self, p) {
		t.Errorf("self-merge = %+v, want %+v", self, p)
	}

	one, err := MergeConfigPlans([]ConfigPlan{{"a", p}})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(one, p) {
		t.Errorf("single-plan merge = %+v, want %+v", one, p)
	}
}

func TestMergeConfigPlans_ContextThreadingAppliesEachEditOnce(t *testing.T) {
	const f = "/ws/a.go"
	src := "package p\n\nfunc F() {}\n\nfunc G() { F() }\n"
	sigAt := strings.Index(src, "F()") + len("F(")
	callAt := strings.LastIndex(src, "F()") + len("F(")
	importAt := len("package p\n")
	threading := func() *types.RefactoringPlan {
		return planOf(
			ins(f, importAt, "\nimport \"context\"\n", "add import"),
			ins(f, sigAt, "ctx context.Context", "add ctx parameter"),
			ins(f, callAt, "ctx", "pass ctx at call site"),
		)
	}

	got, err := MergeConfigPlans([]ConfigPlan{{"default", threading()}, {"tags=fts5", threading()}})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(got.Changes))
	}

	err = assertNoOverlappingChanges(got.Changes)
	if err != nil {
		t.Fatalf("overlap assertion: %v", err)
	}

	out, err := NewSerializer().ApplyChangesToContent([]byte(src), got.Changes)
	if err != nil {
		t.Fatal(err)
	}

	want := "package p\n\nimport \"context\"\n\nfunc F(ctx context.Context) {}\n\nfunc G() { F(ctx) }\n"
	if string(out) != want {
		t.Errorf("applied = %q, want %q", out, want)
	}
}
