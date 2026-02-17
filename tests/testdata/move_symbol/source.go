package main

import "tests/move_symbol/pkg/target"

func Multiply(a, b int) int {
	return a * b
}

func main() {
	_ = Multiply(2, 3)
	_ = target.Greet()
}
