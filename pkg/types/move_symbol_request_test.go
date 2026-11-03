package types

import (
	"slices"
	"testing"
)

func TestMoveSymbolRequestRefsDropsRepeatedPackageAndNameButKeepsSameNameFromAnotherPackage(t *testing.T) {
	ref := func(pkg, name string) QualifiedSymbol { return QualifiedSymbol{Package: pkg, Name: name} }
	req := MoveSymbolRequest{Symbols: []QualifiedSymbol{ref("a", "X"), ref("b", "X"), ref("a", "X"), ref("a", "Y")}}

	want := []QualifiedSymbol{ref("a", "X"), ref("b", "X"), ref("a", "Y")}
	got := req.Refs()
	if !slices.Equal(got, want) {
		t.Errorf("Refs() = %v, want %v", got, want)
	}
}
