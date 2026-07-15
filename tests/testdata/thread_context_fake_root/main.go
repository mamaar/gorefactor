package main

import "context"

// main is a root, so its local `ctx := context.Background()` is passed
// as-is at both call sites below and the statement is KEPT — contrast this
// with hoisted below, where the identical statement is deleted.
func main() {
	ctx := context.Background()
	_ = ctx
	target()
	outer()
}

// outer has no context in scope of its own, so it must be climbed and gain
// a ctx parameter, proving the climb continues past hoisted rather than
// stopping there.
func outer() {
	hoisted()
}

// hoisted is not a root, so its fabricated `ctx := context.Background()` is
// hoisted past: hoisted gains a real ctx parameter and the statement is
// deleted.
func hoisted() {
	ctx := context.Background()
	_ = ctx
	target()
}

// target is the seed.
func target() {}
