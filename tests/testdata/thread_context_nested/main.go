package main

import "context"

// run already has a context.Context in scope, so the climb stops here: run
// itself is untouched, but the nested call below has both Outer and Inner
// threaded independently as two zero-width insertions.
func run(ctx context.Context) {
	Outer(Inner(1))
}

func Outer(x int) {
	Helper(x)
}

func Inner(x int) int {
	Helper(x)
	return x
}

// Helper is the seed.
func Helper(x int) {}

func main() {
	run(context.Background())
}
