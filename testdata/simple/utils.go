package main

import "strings"

// StringUtils provides string utility functions
type StringUtils struct{}

// ToUpper converts a string to uppercase
func (su StringUtils) ToUpper(s string) string {
	return strings.ToUpper(s)
}

// Reverse reverses a string
func (su StringUtils) Reverse(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Constants for the application
const (
	DefaultTimeout = 30
	MaxRetries     = 3
)

// Global variables
var (
	AppName    = "SimpleApp"
	AppVersion = "1.0.0"
)