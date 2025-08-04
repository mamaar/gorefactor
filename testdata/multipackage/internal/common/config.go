package common

// Config holds application configuration
type Config struct {
	Host string
	Port int
}

// Logger provides logging functionality
type Logger struct {
	level LogLevel
}

type LogLevel int

const (
	Debug LogLevel = iota
	Info
	Warning
	Error
)

// NewLogger creates a new logger instance
func NewLogger(level LogLevel) *Logger {
	return &Logger{level: level}
}

// Log logs a message at the specified level
func (l *Logger) Log(level LogLevel, message string) {
	if level >= l.level {
		// Implementation would go here
	}
}