package main

import "fmt"

type Calculator struct {
	result int
}

func (c *Calculator) Process(a, b int) {
	sum := a + b
	c.result = sum * 2
	fmt.Println(c.result)
}

func main() {
	calc := &Calculator{}
	calc.Process(3, 4)
}
