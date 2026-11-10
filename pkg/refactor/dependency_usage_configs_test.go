package refactor

import "testing"

func TestDependencyUsageModuleOf(t *testing.T) {
	required := []string{"github.com/x/y", "github.com/x/y/v2", "github.com/a/b"}

	tests := []struct {
		name, pkg, want string
	}{
		{"exact", "github.com/a/b", "github.com/a/b"},
		{"subpackage", "github.com/a/b/c/d", "github.com/a/b"},
		{"longest wins for v2", "github.com/x/y/v2/z", "github.com/x/y/v2"},
		{"v1 subpackage", "github.com/x/y/z", "github.com/x/y"},
		{"shared prefix is not a boundary", "github.com/x/yy", ""},
		{"stdlib", "fmt", ""},
		{"workspace local", "example.com/app/internal/foo", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := moduleOf(tt.pkg, required)
			if got != tt.want {
				t.Errorf("moduleOf(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
	}
}
