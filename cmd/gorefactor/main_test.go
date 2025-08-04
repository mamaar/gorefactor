package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestGetSymbolKindName(t *testing.T) {
	testCases := []struct {
		kind     types.SymbolKind
		expected string
	}{
		{types.FunctionSymbol, "Function"},
		{types.MethodSymbol, "Method"},
		{types.TypeSymbol, "Type"},
		{types.VariableSymbol, "Variable"},
		{types.ConstantSymbol, "Constant"},
		{types.InterfaceSymbol, "Interface"},
		{types.StructFieldSymbol, "Struct Field"},
		{types.PackageSymbol, "Package"},
		{types.SymbolKind(99), "Unknown"},
	}

	for _, tc := range testCases {
		result := getSymbolKindName(tc.kind)
		if result != tc.expected {
			t.Errorf("Expected getSymbolKindName(%v) to return '%s', got '%s'", tc.kind, tc.expected, result)
		}
	}
}

// TestResolvePackagePath would require setting up a workspace, skipped for now

func TestOutputJSON(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Test data
	data := map[string]string{"test": "value"}
	outputJSON(data)

	// Restore stdout
	w.Close()
	os.Stdout = old

	// Read output
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, `"test": "value"`) {
		t.Errorf("Expected JSON output to contain test data, got: %s", output)
	}
}