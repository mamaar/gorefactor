package refactor

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestSymbolNames_NamesALoneMethodByTypeAndSkipsMethodsRidingWithTheirType(t *testing.T) {
	typ := &movingSymbol{sym: &types.Symbol{Name: "T"}}
	riding := &movingSymbol{sym: &types.Symbol{Name: "Ride"}, typ: typ}
	lone := &movingSymbol{sym: &types.Symbol{Name: "M"}, typ: &movingSymbol{sym: &types.Symbol{Name: "U"}}, lone: true}

	if got := symbolNames([]*movingSymbol{typ, riding, lone}); got != "T, U.M" {
		t.Errorf("symbolNames = %q, want %q", got, "T, U.M")
	}
}
