package logger

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Logger interface {
	Debug(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
}

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

const (
	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	gray   = "\033[90m"
)

type logger struct {
	mu        sync.Mutex
	level     Level
	useColors bool
	out       io.Writer
	err       io.Writer
}

type Config struct {
	Level     Level
	UseColors *bool // If nil, auto-detect based on TTY
	Out       io.Writer
	Err       io.Writer
}

func New(cfg Config) Logger {
	l := &logger{
		level: cfg.Level,
		out:   cfg.Out,
		err:   cfg.Err,
	}

	if l.out == nil {
		l.out = os.Stdout
	}
	if l.err == nil {
		l.err = os.Stderr
	}

	if cfg.UseColors != nil {
		l.useColors = *cfg.UseColors
	} else {
		// Auto-detect: Check if stdout is a terminal
		// If stdout is piped (e.g., to journald), disable colors.
		l.useColors = isTerminal(os.Stdout)
	}

	return l
}

// Default returns a standard logger (Info level, auto colors).
func Default() Logger {
	return New(Config{
		Level: LevelInfo,
	})
}

func (l *logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

func (l *logger) log(level Level, colorCode, prefix, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	writer := l.out
	if level == LevelError {
		writer = l.err
	}

	msg := fmt.Sprintf(format, args...)

	var line string
	now := time.Now().Format("15:04:05")

	if l.useColors {
		line = fmt.Sprintf("%s[%s]%s %s%s%s %s\n", gray, now, reset, colorCode, prefix, reset, msg)
	} else {
		line = fmt.Sprintf("[%s] %s %s\n", now, prefix, msg)
	}

	_, _ = fmt.Fprint(writer, line)
}

func (l *logger) Debug(format string, args ...any) {
	l.log(LevelDebug, blue, "Debug", format, args...)
}

func (l *logger) Info(format string, args ...any) {
	l.log(LevelInfo, green, "Info", format, args...)
}

func (l *logger) Warn(format string, args ...any) {
	l.log(LevelWarn, yellow, "Warn", format, args...)
}

func (l *logger) Error(format string, args ...any) {
	l.log(LevelError, red, "Error", format, args...)
}

// isTerminal checks if the file descriptor is a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
