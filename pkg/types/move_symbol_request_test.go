package types

import (
	"slices"
	"testing"
)

func TestMoveSymbolRequestRefsDropsRepeatedPackageAndNameButKeepsSameNameFromAnotherPackage(t *testing.T) {
	req := MoveSymbolRequest{Symbols: []QualifiedSymbol{{"a", "X"}, {"b", "X"}, {"a", "X"}, {"a", "Y"}}}

	want := []QualifiedSymbol{{"a", "X"}, {"b", "X"}, {"a", "Y"}}
	if got := req.Refs(); !slices.Equal(got, want) {
		t.Errorf("Refs() = %v, want %v", got, want)
	}
}
