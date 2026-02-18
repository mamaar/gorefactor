package main

func Add(a, b int) int {
	return a + b
}

func main() {
	x := Add(1, 2)
	y := Add(3, 4)
	_ = x + y
}
