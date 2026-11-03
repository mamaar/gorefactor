package mcp

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestBuildDiffsSummarisesCarriedFile(t *testing.T) {
	content := strings.Repeat("x", 3*maxHunkText)
	diffs := buildDiffs([]types.Change{{File: "/q/data.txt", NewText: content, Mode: 0o644, Relocation: true}})

	got := diffs[0].Hunks[0].New
	if got != "[file moved, 24000 bytes]" {
		t.Errorf("hunk = %q", got)
	}
}
