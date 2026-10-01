package refactor

import "testing"

// common's test imports x and x's test imports common, which Go allows. The
// check of common then hands x a production-only copy of common, and every
// later importer of common keeps getting that copy.
func rescuedCommonFiles() map[string]string {
	return map[string]string{
		"common/common.go":      "package common\n\ntype TM struct{}\n\nfunc (*TM) Get() {}\n",
		"common/common_test.go": "package common\n\nimport \"example.com/threadtest/x\"\n\nvar _ = x.X\n",
		"x/x.go":                "package x\n\nvar X = 1\n",
		"x/x_test.go":           "package x\n\nimport \"example.com/threadtest/common\"\n\nvar _ = common.TM{}\n",
		"tile/tile.go":          "package tile\n\nimport \"example.com/threadtest/common\"\n\nfunc Use(tm *common.TM) { tm.Get() }\n",
	}
}

func TestThreadContext_SeedCalledThroughProductionOnlyCopyOfItsPackage(t *testing.T) {
	eng, dir := loadConfigsEngine(t, rescuedCommonFiles())
	v := eng.WorkspaceSet().Primary()
	v.Parser.EnsureTypeChecked(v.Workspace, v.Workspace.Packages[dir+"/common"])

	threadAcrossAndApply(t, eng, "common/common.go", "TM.Get")

	assertOnce(t, dir, "common/common.go", "func (*TM) Get(ctx context.Context)")
	assertOnce(t, dir, "tile/tile.go", "func Use(ctx context.Context, tm *common.TM) { tm.Get(ctx) }")
}
