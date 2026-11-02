package refactor

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// loadRescuedInterface loads rescuedInterfaceFiles with common checked first,
// so that its importers hold the production-only copy.
func loadRescuedInterface(t *testing.T) (*DefaultEngine, string) {
	t.Helper()
	return loadRescuedInterfaceWith(t, strings.NewReplacer())
}

// loadRescuedInterfaceWith is loadRescuedInterface with variant applied to every file.
func loadRescuedInterfaceWith(t *testing.T, variant *strings.Replacer) (*DefaultEngine, string) {
	t.Helper()
	files := rescuedInterfaceFiles()
	files["app/fresh.go"] = "package app\n\nimport \"example.com/threadtest/common\"\n\nfunc Fresh() common.Opts { return common.Opts{Refresh: true} }\n"
	for rel, src := range files {
		files[rel] = variant.Replace(src)
	}
	eng, dir := loadConfigsEngine(t, files)
	v := eng.WorkspaceSet().Primary()
	v.Parser.EnsureTypeChecked(v.Workspace, v.Workspace.Packages[dir+"/common"])
	return eng, dir
}

func applyPlan(t *testing.T, eng *DefaultEngine, plan *types.RefactoringPlan, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}
}

func TestRenameMethod_InterfaceOfPackageSeenThroughProductionOnlyCopy(t *testing.T) {
	eng, dir := loadRescuedInterface(t)

	plan, err := eng.RenameMethodAcrossConfigs(types.RenameMethodRequest{TypeName: "Manager", MethodName: "Token", NewMethodName: "Fetch"})
	applyPlan(t, eng, plan, err)

	assertOnce(t, dir, "common/common.go", "\tFetch(service string, opts Opts) (string, error)\n")
	assertOnce(t, dir, "common/common.go", "func (m *Manager) Fetch(service string, opts Opts) (string, error)")
	assertOnce(t, dir, "common/common.go", "return m.Fetch(\"d\", Opts{})")
	assertOnce(t, dir, "common/common_test.go", "src.Fetch(helper.Name, Opts{})")
	assertOnce(t, dir, "helper/helper_test.go", "return src.Fetch(Name, common.Opts{})")
	assertOnce(t, dir, "app/app.go", "return a.src.Fetch(\"x\", common.Opts{})")
	assertOnce(t, dir, "app/app.go", "return m.Fetch(\"c\", common.Opts{})")
	assertOnce(t, dir, "app/app.go", "func (inProc) Fetch(service string, opts common.Opts) (string, error)")
	vetBuilds(t, dir, linuxBuild)
}

func TestRenameField_UseInImporterOfProductionOnlyCopy(t *testing.T) {
	eng, dir := loadRescuedInterface(t)

	plan, err := eng.RenameFieldAcrossConfigs(types.RenameFieldRequest{TypeName: "Opts", FieldName: "Refresh", NewFieldName: "Force"})
	applyPlan(t, eng, plan, err)

	assertOnce(t, dir, "common/common.go", "type Opts struct{ Force bool }")
	assertOnce(t, dir, "app/fresh.go", "common.Opts{Force: true}")
	vetBuilds(t, dir, linuxBuild)
}

func TestSafeDelete_ReferencesInImportersOfProductionOnlyCopyBlock(t *testing.T) {
	eng, dir := loadRescuedInterface(t)

	blockers, err := eng.SafeDeleteBlockingReferences(types.SafeDeleteRequest{Symbol: "Direct", SourceFile: filepath.Join(dir, "common/common.go")})
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 0 {
		t.Fatalf("Direct has no references, got %+v", blockers)
	}

	blockers, err = eng.SafeDeleteBlockingReferences(types.SafeDeleteRequest{Symbol: "Manager", SourceFile: filepath.Join(dir, "common/common.go")})
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, b := range blockers {
		rel, _ := filepath.Rel(dir, b.File)
		files = append(files, filepath.ToSlash(rel))
	}
	slices.Sort(files)
	want := []string{"app/app.go", "common/common.go", "common/common_test.go", "helper/helper_test.go"}
	if got := slices.Compact(files); !slices.Equal(got, want) {
		t.Errorf("blocking files = %v, want %v", got, want)
	}
}

// TestChangeSignature_CallInImporterOfProductionOnlyCopy uses a signature of
// basic types; with the package's own type the next test applies.
func TestChangeSignature_CallInImporterOfProductionOnlyCopy(t *testing.T) {
	eng, dir := loadRescuedInterfaceWith(t, withoutOpts)

	plan, err := eng.ChangeSignatureAcrossConfigs(ChangeSignatureRequest{
		FunctionName:         "Manager.Token",
		SourceFile:           filepath.Join(dir, "common/common.go"),
		Subcommand:           "add_param",
		NewParams:            []Parameter{{Name: "service", Type: "string"}, {Name: "n", Type: "int"}},
		DefaultValue:         "0",
		NewParamPosition:     1,
		Scope:                types.WorkspaceScope,
		PropagateToInterface: true,
	})
	applyPlan(t, eng, plan, err)

	assertOnce(t, dir, "common/common.go", "\tToken(service string, n int) (string, error)\n")
	assertOnce(t, dir, "common/common.go", "func (m *Manager) Token(service string, n int) (string, error)")
	assertOnce(t, dir, "common/common.go", "return m.Token(\"d\", 0)")
	assertOnce(t, dir, "app/app.go", "return m.Token(\"c\", 0)")
	vetBuilds(t, dir, linuxBuild)
}

func TestChangeSignature_RefusesPackageLocalTypeWrittenUnqualifiedIntoImplementation(t *testing.T) {
	eng, dir := loadRescuedInterface(t)

	plan, err := eng.ChangeSignatureAcrossConfigs(ChangeSignatureRequest{
		FunctionName:         "Manager.Token",
		SourceFile:           filepath.Join(dir, "common/common.go"),
		Subcommand:           "add_param",
		NewParams:            []Parameter{{Name: "service", Type: "string"}, {Name: "opts", Type: "Opts"}, {Name: "n", Type: "int"}},
		DefaultValue:         "0",
		NewParamPosition:     2,
		Scope:                types.WorkspaceScope,
		PropagateToInterface: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err == nil || !strings.Contains(err.Error(), "undefined: Opts") {
		t.Errorf("ExecutePlan error = %v, want the type check's undefined: Opts", err)
	}
}
