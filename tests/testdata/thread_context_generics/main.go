package main

// Seed is the seed: a generic function with a variadic parameter and a
// func-typed parameter. The new ctx parameter must be inserted without
// disturbing [T any], the variadic xs, or the fn signature — a rebuilt
// signature (rather than a zero-width insertion) would corrupt fn to
// func(...) and drop the type parameter entirely.
func Seed[T any](x T, fn func(int) error, xs ...string) {
	_ = x
	_ = fn
	_ = xs
}

func caller() {
	Seed(1, func(int) error { return nil }, "a", "b")
}

func main() {
	caller()
}
