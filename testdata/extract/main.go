package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Calculator provides mathematical operations
type Calculator struct {
	name   string
	memory float64
}

// NewCalculator creates a new calculator instance
func NewCalculator(name string) *Calculator {
	return &Calculator{
		name:   name,
		memory: 0.0,
	}
}

// Process handles a calculation request with complex logic
func (c *Calculator) Process(input string) (float64, error) {
	// Complex processing logic that could be extracted
	parts := strings.Split(input, " ")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid input format")
	}
	
	// Parse first operand
	a, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid first operand: %v", err)
	}
	
	// Parse second operand
	b, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid second operand: %v", err)
	}
	
	// Perform operation
	operator := parts[1]
	var result float64
	switch operator {
	case "+":
		result = a + b
	case "-":
		result = a - b
	case "*":
		result = a * b
	case "/":
		if b == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		result = a / b
	default:
		return 0, fmt.Errorf("unknown operator: %s", operator)
	}
	
	// Store in memory
	c.memory = result
	return result, nil
}

// Add performs addition
func (c *Calculator) Add(a, b float64) float64 {
	result := a + b
	c.memory = result
	return result
}

// Subtract performs subtraction
func (c *Calculator) Subtract(a, b float64) float64 {
	result := a - b
	c.memory = result
	return result
}

// Multiply performs multiplication
func (c *Calculator) Multiply(a, b float64) float64 {
	result := a * b
	c.memory = result
	return result
}

// Divide performs division
func (c *Calculator) Divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, fmt.Errorf("division by zero")
	}
	result := a / b
	c.memory = result
	return result, nil
}

// GetMemory returns the stored memory value
func (c *Calculator) GetMemory() float64 {
	return c.memory
}

// ClearMemory resets the memory to zero
func (c *Calculator) ClearMemory() {
	c.memory = 0.0
}

func main() {
	calc := NewCalculator("TestCalc")
	
	// Test basic operations
	fmt.Printf("5 + 3 = %.2f\n", calc.Add(5, 3))
	fmt.Printf("Memory: %.2f\n", calc.GetMemory())
	
	// Test complex processing
	result, err := calc.Process("10 * 2")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	} else {
		fmt.Printf("Process result: %.2f\n", result)
	}
	
	// Complex expression that could be extracted as a variable
	complexCalculation := calc.Add(calc.Multiply(2, 3), calc.Divide(10, 2))
	fmt.Printf("Complex calculation: %.2f\n", complexCalculation)
}