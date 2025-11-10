package log

import (
	"fmt"
	"github.com/rs/zerolog"
	"os"
	"time"
)

// ZeroLogger wraps a zerolog.Logger to satisfy the Logger interface.
type ZeroLogger struct {
	logger zerolog.Logger
}

// NewLogger creates a new adapter with an underlying zerolog.Logger
// configured to the specified level.
func NewLogger(level Level) Logger {
	loggerLevel, err := zerolog.ParseLevel(LevelNames[level])
	if err != nil {
		fmt.Printf("Error creating logger with level %s\n", LevelNames[level])
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).Level(loggerLevel).With().Timestamp().Logger()

	// Return a new adapter wrapping the new logger
	return &ZeroLogger{logger: logger}
}

// Debug logs a formatted message at the Debug level.
func (z *ZeroLogger) Debug(format string, args ...any) {
	// Use Msgf to match the Sprintf-style interface
	z.logger.Debug().Msgf(format, args...)
}

// Info logs a formatted message at the Info level.
func (z *ZeroLogger) Info(format string, args ...any) {
	z.logger.Info().Msgf(format, args...)
}

// Warn logs a formatted message at the Warn level.
func (z *ZeroLogger) Warn(format string, args ...any) {
	z.logger.Warn().Msgf(format, args...)
}

// Error logs a formatted message at the Error level.
func (z *ZeroLogger) Error(format string, args ...any) {
	z.logger.Error().Msgf(format, args...)
}
