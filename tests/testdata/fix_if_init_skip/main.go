package main

import "fmt"

func makeIntString() string { return "42" }

func compute(n int) int { return n * 3 }

// forLoopInit: a for-loop's init must never be hoisted.
func forLoopInit(n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		sum += i
	}
	return sum
}

// labeledIf: an if that is the direct Stmt of a LabeledStmt must not have
// its own init hoisted, since that would retarget the label onto the
// hoisted assignment.
func labeledIf(n int) int {
	result := 0
Retry:
	if v := compute(n); v > 0 {
		result = v
	}
	if result == 0 && n > 0 {
		n--
		goto Retry
	}
	return result
}

// typeIncompatible: the if's `x` collides with the function parameter
// `x`, but the RHS type (string) is not assignable to the parameter's
// type (int), so the site must be skipped rather than hoisted.
func typeIncompatible(x int) {
	if x := makeIntString(); len(x) > 0 {
		fmt.Println(x)
	}
}

func main() {
	fmt.Println(forLoopInit(5), labeledIf(3))
	typeIncompatible(1)
}
