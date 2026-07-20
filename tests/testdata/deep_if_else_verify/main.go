package main

import "fmt"

// goodChain is a straightforward depth-3 chain: every else is a block ending
// in return, so it flattens all the way with nothing left nested. This is the
// control that proves an ordinary chain still gets applied once verification
// is in the loop.
func goodChain(x int) string {
	if x > 0 {
		if x < 100 {
			if x%2 == 0 {
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

// typeErrorChain looks exactly like a chain deepifelse's own safety checks
// wave through: no branch declares anything a later branch reads, and the
// final else's `x, y := ...` looks brand new from where the chain sits (it is
// only "new" because it lives inside its own nested block, shadowing the
// `x string` declared above). The rewriter elides every else (each branch
// hands control away) and then dedents the final else's statements into the
// function's top-level scope. That turns what was a legal shadowing
// declaration into a reassignment of a string variable with an int value,
// which does not type-check even though the original nested form did.
func typeErrorChain(cond1, cond2, cond3 bool) string {
	var x string
	if cond1 {
		fmt.Println(x)
		return "a"
	} else if cond2 {
		return "b"
	} else if cond3 {
		return "c"
	} else {
		x, y := 5, "z"
		fmt.Println(x, y)
	}
	return ""
}

func main() {
	fmt.Println(goodChain(42))
	fmt.Println(typeErrorChain(false, false, false))
}
