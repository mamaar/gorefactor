package main

import "fmt"

// Depth 3 in a shape extractChain can flatten: every else is a block ending in
// return, and every body is a single nested if-else. This one must be rewritten.
func process(x int) string {
	if x > 0 {
		if x < 100 {
			if x%2 == 0 {
				// even numbers get a dedicated label
				return fmt.Sprintf("even: %d", x)
			} else {
				return fmt.Sprintf("odd: %d", x)
			}
		} else {
			return "too large"
		}
	} else {
		return "negative"
	}
}

// Depth 3, using `else if` rather than a block at the outer level - the shape
// the old rewriter refused outright. Every branch already returns, so every
// link elides and no else survives. The nesting inside the first branch is not
// a chain of its own to lift: it is that branch's own body, flattened in
// place.
func categorize(x int) string {
	if x < 0 {
		if x < -100 {
			if x < -1000 {
				return "extremely negative"
			} else {
				return "very negative"
			}
		} else {
			return "negative"
		}
	} else if x == 0 {
		return "zero"
	} else {
		return "positive"
	}
}

// Depth 2, below the default threshold. Must be left exactly as written -
// this is the control that keeps the threshold from being quietly lowered.
func describe(x int) string {
	if x > 0 {
		if x < 100 {
			return "small positive"
		} else {
			return "large positive"
		}
	} else {
		return "non-positive"
	}
}

func main() {
	fmt.Println(process(42))
	fmt.Println(categorize(-5))
	fmt.Println(describe(7))
}
