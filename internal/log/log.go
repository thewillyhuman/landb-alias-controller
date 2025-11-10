// Package log defines a flexible, level-based logging interface.
// It provides the core types and contracts for creating and using
// structured logging implementations.
package log

import "strings"

// Level defines the severity of a log message.
type Level int

// Defines the available log levels for the logger.
const (
	// LevelDebug is for detailed, diagnostic information, typically only
	// useful during development and debugging.
	LevelDebug Level = iota

	// LevelInfo is for general, informational messages that highlight
	// the progress or state of the application.
	LevelInfo

	// LevelWarn indicates a potential problem or a non-critical event
	// that may require attention.
	LevelWarn

	// LevelError designates a significant error or failure that has occurred
	// and likely requires investigation.
	LevelError

	// DefaultLogLevel represents the fallback log level for all the application.
	DefaultLogLevel = LevelInfo
)

var (
	LevelNames = map[Level]string{
		LevelDebug: "debug",
		LevelInfo:  "info",
		LevelWarn:  "warn",
		LevelError: "error",
	}

	// GlobalLogger is meant to be used from any other place in the application.
	GlobalLogger Logger
)

// --- Solution ---

// 1. Create the reverse map
var levelValues = make(map[string]Level)

// 2. Use an init() function to populate the reverse map
// This runs once at package startup.
func init() {
	for level, name := range LevelNames {
		levelValues[name] = level
	}
}

// Logger defines the standard contract for a level-based logger.
//
// Implementations are responsible for handling log message filtering based
// on the logger's configured level and formatting the output.
type Logger interface {

	// Debug logs a formatted message at the Debug level.
	// Arguments are handled in the manner of fmt.Sprintf.
	Debug(format string, args ...any)

	// Info logs a formatted message at the Info level.
	// Arguments are handled in the manner of fmt.Sprintf.
	Info(format string, args ...any)

	// Warn logs a formatted message at the Warn level.
	// Arguments are handled in the manner of fmt.Sprintf.
	Warn(format string, args ...any)

	// Error logs a formatted message at the Error level.
	// Arguments are handled in the manner of fmt.Sprintf.
	Error(format string, args ...any)
}

func LevelFromString(name string) (Level, bool) {
	// Use ToLower to make the lookup case-insensitive
	level, ok := levelValues[strings.ToLower(name)]
	return level, ok
}
