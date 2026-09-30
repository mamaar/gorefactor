package refactor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestMoveSymbolOperation_RefusesSourcePackageThatIsNotTypeChecked(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"src/src.go": "package src\n\nfunc One() int { return 1 }\n",
	})
	ws := eng.WorkspaceSet().Primary().Workspace
	op := &MoveSymbolOperation{Request: types.MoveSymbolRequest{
		SymbolName:   "One",
		FromPackage:  filepath.Join(dir, "src"),
		ToPackage:    filepath.Join(dir, "dst"),
		CreateTarget: true,
	}}

	err := op.Validate(ws)

	if err == nil || !strings.Contains(err.Error(), "not type-checked") {
		t.Errorf("Validate = %v, want a refusal naming the missing type-check", err)
	}
}
