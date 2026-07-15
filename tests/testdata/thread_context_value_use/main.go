package main

// Seed is referenced as a value below, which binds its current signature.
// Threading must abort rather than leave that use broken.
func Seed() {}

var h = Seed

func main() {
	h()
}
