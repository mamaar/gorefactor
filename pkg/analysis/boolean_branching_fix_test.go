package analysis

import (
	"strings"
	"testing"
)

func TestBooleanBranchingFix_BasicFix(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].SwitchCases != 2 {
		t.Errorf("Expected 2 switch cases, got %d", results[0].SwitchCases)
	}
	if len(results[0].BooleansRemoved) != 2 {
		t.Errorf("Expected 2 booleans removed, got %d", len(results[0].BooleansRemoved))
	}

	// Should have 3 changes: 2 assignment removals + 1 if-chain replacement
	if len(plan.Changes) != 3 {
		t.Fatalf("Expected 3 changes, got %d", len(plan.Changes))
	}

	// Find the switch replacement change
	var switchChange *string
	for _, c := range plan.Changes {
		if strings.Contains(c.NewText, "switch") {
			s := c.NewText
			switchChange = &s
			break
		}
	}
	if switchChange == nil {
		t.Fatal("No switch statement change found")
	}

	if !strings.Contains(*switchChange, `switch accept`) {
		t.Errorf("Switch should use 'accept', got: %s", *switchChange)
	}
	if !strings.Contains(*switchChange, `case "x-shapefile"`) {
		t.Errorf("Missing x-shapefile case in: %s", *switchChange)
	}
	if !strings.Contains(*switchChange, `case "geojson"`) {
		t.Errorf("Missing geojson case in: %s", *switchChange)
	}
	if !strings.Contains(*switchChange, "doShapefile()") {
		t.Errorf("Missing doShapefile() body in: %s", *switchChange)
	}
	if !strings.Contains(*switchChange, "doGeoJSON()") {
		t.Errorf("Missing doGeoJSON() body in: %s", *switchChange)
	}
}

func TestBooleanBranchingFix_WithDefault(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	} else {
		doDefault()
	}
}

func doShapefile() {}
func doGeoJSON() {}
func doDefault() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	// 2 cases + 1 default
	if results[0].SwitchCases != 3 {
		t.Errorf("Expected 3 switch cases (including default), got %d", results[0].SwitchCases)
	}

	// Find the switch change
	var switchChange string
	for _, c := range plan.Changes {
		if strings.Contains(c.NewText, "switch") {
			switchChange = c.NewText
			break
		}
	}
	if switchChange == "" {
		t.Fatal("No switch statement change found")
	}
	if !strings.Contains(switchChange, "default:") {
		t.Errorf("Missing default case in: %s", switchChange)
	}
	if !strings.Contains(switchChange, "doDefault()") {
		t.Errorf("Missing default body in: %s", switchChange)
	}
}

func TestBooleanBranchingFix_NoViolations(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	switch accept {
	case "x-shapefile":
		doShapefile()
	case "geojson":
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, results, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(plan.Changes))
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestBooleanBranchingFix_SingleBoolean_NoFix(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"

	if wantShapefile {
		doShapefile()
	}
}

func doShapefile() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.Changes) != 0 {
		t.Errorf("Expected 0 changes for single boolean, got %d", len(plan.Changes))
	}
}

func TestBooleanBranchingFix_RemovesAssignments(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantShapefile := accept == "x-shapefile"
	wantGeoJSON := accept == "geojson"

	if wantShapefile {
		doShapefile()
	} else if wantGeoJSON {
		doGeoJSON()
	}
}

func doShapefile() {}
func doGeoJSON() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	// Count removal changes (empty NewText)
	removals := 0
	for _, c := range plan.Changes {
		if c.NewText == "" {
			removals++
			// Verify the removed text contains the assignment
			if !strings.Contains(c.OldText, "wantShapefile") && !strings.Contains(c.OldText, "wantGeoJSON") {
				t.Errorf("Removal change doesn't contain expected assignment: %q", c.OldText)
			}
		}
	}
	if removals != 2 {
		t.Errorf("Expected 2 removal changes, got %d", removals)
	}
}

func TestBooleanBranchingFix_MultipleBodies(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantA := accept == "a"
	wantB := accept == "b"

	if wantA {
		line1()
		line2()
	} else if wantB {
		line3()
		line4()
	}
}

func line1() {}
func line2() {}
func line3() {}
func line4() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	var switchChange string
	for _, c := range plan.Changes {
		if strings.Contains(c.NewText, "switch") {
			switchChange = c.NewText
			break
		}
	}
	if switchChange == "" {
		t.Fatal("No switch change found")
	}

	// Multi-line bodies should be preserved
	if !strings.Contains(switchChange, "line1()") || !strings.Contains(switchChange, "line2()") {
		t.Errorf("Case a body not preserved: %s", switchChange)
	}
	if !strings.Contains(switchChange, "line3()") || !strings.Contains(switchChange, "line4()") {
		t.Errorf("Case b body not preserved: %s", switchChange)
	}
}

func TestBooleanBranchingFix_AffectedFiles(t *testing.T) {
	src := `package testpkg

func handle(accept string) {
	wantA := accept == "a"
	wantB := accept == "b"

	if wantA {
		doA()
	} else if wantB {
		doB()
	}
}

func doA() {}
func doB() {}
`
	ws := createBoolBranchTestWorkspace(t, src)
	fixer := NewBooleanBranchingFixer(ws, 2)
	plan, _, err := fixer.Fix("")
	if err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	if len(plan.AffectedFiles) != 1 {
		t.Errorf("Expected 1 affected file, got %d", len(plan.AffectedFiles))
	}
	if len(plan.AffectedFiles) > 0 && plan.AffectedFiles[0] != "testpkg.go" {
		t.Errorf("Expected affected file 'testpkg.go', got %q", plan.AffectedFiles[0])
	}
}
