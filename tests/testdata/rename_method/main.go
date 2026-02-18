package main

type Calculator struct {
	value int
}

func (c *Calculator) Add(n int) {
	c.value += n
}

func main() {
	calc := &Calculator{}
	calc.Add(5)
	calc.Add(10)
}
