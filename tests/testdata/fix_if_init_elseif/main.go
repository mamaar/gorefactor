package main

import "fmt"

func compute(a int) int      { return a * 2 }
func computeOther(a int) int { return a + 5 }

// single has one else-if with an init, and no trailing else at all.
func single(a int) string {
	if a == 1 {
		return "one"
	} else if b := compute(a); b > 10 {
		return "big"
	}
	return "small"
}

// chain has two else-ifs, each with its own init, followed by a plain
// else with no init.
func chain(a int) string {
	if a == 0 {
		return "zero"
	} else if b := compute(a); b > 100 {
		return "big"
	} else if c := computeOther(a); c > 10 {
		return "medium"
	} else {
		return "small"
	}
}

func main() {
	fmt.Println(single(1), chain(50))
}
