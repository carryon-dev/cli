package logging

import (
	"sync/atomic"
	"time"
)

var levelOrder = map[string]int{
	"debug": 0,
	"info":  1,
	"warn":  2,
	"error": 3,
}

// Logger filters log entries by level and writes them to a Store.
type Logger struct {
	store    *Store
	minLevel atomic.Int32
}

// NewLogger creates a Logger that only records entries at or above the given
// minimum level ("debug", "info", "warn", "error").
func NewLogger(store *Store, level string) *Logger {
	l := &Logger{store: store}
	l.minLevel.Store(int32(levelOrder[level]))
	return l
}

// SetLevel changes the minimum log level at runtime. Thread-safe.
func (l *Logger) SetLevel(level string) {
	l.minLevel.Store(int32(levelOrder[level]))
}

// Debug logs a message at debug level.
func (l *Logger) Debug(component, message string) {
	l.log("debug", component, message)
}

// Info logs a message at info level.
func (l *Logger) Info(component, message string) {
	l.log("info", component, message)
}

// Warn logs a message at warn level.
func (l *Logger) Warn(component, message string) {
	l.log("warn", component, message)
}

// Error logs a message at error level.
func (l *Logger) Error(component, message string) {
	l.log("error", component, message)
}

func (l *Logger) log(level, component, message string) {
	if int32(levelOrder[level]) < l.minLevel.Load() {
		return
	}
	entry := LogEntry{
		Timestamp: time.Now().UnixMilli(),
		Level:     level,
		Component: component,
		Message:   message,
	}
	l.store.Append(entry)
}
