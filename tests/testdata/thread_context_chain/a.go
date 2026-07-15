package main

// a does not yet import "context", exercising import insertion once it
// gains a ctx parameter of its own.
func a() {
	b()
}
