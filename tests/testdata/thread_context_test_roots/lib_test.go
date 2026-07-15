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

// helper accepts a *testing.T but is NOT a Test/Benchmark/Fuzz declaration,
// so it is not a root: it must be climbed like any other function and gain
// its own ctx parameter.
func helper(t *testing.T) {
	t.Helper()
	Seed()
}
