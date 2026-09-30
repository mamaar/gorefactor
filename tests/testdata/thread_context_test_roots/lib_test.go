package foo

import "testing"

// TestFoo is a root: Test declarations get t.Context(), never a parameter.
// Its t.Run subtest closure is also a root, and gets its OWN t.Context().
func TestFoo(t *testing.T) {
	helper(t)
	t.Run("sub", func(t *testing.T) {
		Seed()
	})
}

// helper is a root too: a named *testing.T parameter makes it one, so it
// gains no ctx parameter and passes t.Context().
func helper(t *testing.T) {
	t.Helper()
	Seed()
}
