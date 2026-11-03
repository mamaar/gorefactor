package refactor

import (
	"testing"
)

func TestFacadeFileName(t *testing.T) {
	tests := []struct {
		key  facadeKey
		want string
	}{
		{facadeKey{}, "facade.go"},
		{facadeKey{suffix: "windows"}, "facade_windows.go"},
		{facadeKey{expr: "tagx"}, "facade_tagx_tags.go"},
		{facadeKey{expr: "cgo"}, "facade_cgo_tags.go"},
		// A trailing "windows" must not turn the name into a GOOS suffix.
		{facadeKey{expr: "!windows"}, "facade_not_windows_tags.go"},
		{facadeKey{expr: "(a || b) && cgo", suffix: "linux_amd64"}, "facade_a_or_b_and_cgo_linux_amd64.go"},
	}
	for _, tt := range tests {
		got := facadeFileName(tt.key, nil)
		if got != tt.want {
			t.Errorf("facadeFileName(%+v) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

// Different expressions can flatten to the same words.
func TestFacadeFileNameAvoidsTakenNames(t *testing.T) {
	taken := map[string]bool{"facade_a_tags.go": true}

	got := facadeFileName(facadeKey{expr: "a"}, taken)
	if got != "facade_a_2_tags.go" {
		t.Errorf("got %q", got)
	}
}
