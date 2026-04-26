package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// LogFileWriter implements io.Writer that rotates log files daily.
// Log files are written to logs/{date}.log (e.g. logs/2026-04-26.log).
// It only handles file writing; console output is managed separately.
type LogFileWriter struct {
	mu         sync.Mutex
	dir        string
	current    string   // current date string, e.g. "2026-04-26"
	file       *os.File // current log file handle
	enabled    bool
	fileMaxLen int // max line length for file output (0 = no truncation, used in debug)
}

// newLogFileWriter creates a new daily rotating file writer.
// fileMaxLen: max line length for file (0 = no truncation, for debug mode).
func newLogFileWriter(dir string, enabled bool, fileMaxLen int) *LogFileWriter {
	w := &LogFileWriter{
		dir:        dir,
		enabled:    enabled,
		fileMaxLen: fileMaxLen,
	}
	if enabled {
		if err := os.MkdirAll(dir, 0755); err != nil {
			os.Stderr.WriteString("[logger] failed to create log directory " + dir + ": " + err.Error() + "\n")
			w.enabled = false
		}
	}
	return w
}

func (w *LogFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.enabled {
		return len(p), nil
	}

	today := time.Now().Format("2006-01-02")

	// Rotate file if date changed
	if today != w.current {
		if w.file != nil {
			w.file.Close()
			w.file = nil
		}
		w.current = today
	}

	// Open file if not yet open
	if w.file == nil {
		path := filepath.Join(w.dir, today+".log")
		f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if openErr != nil {
			os.Stderr.WriteString("[logger] failed to open log file " + path + ": " + openErr.Error() + "\n")
			return len(p), nil
		}
		w.file = f
	}

	// File: truncate only if fileMaxLen > 0 (non-debug mode); in debug mode write full content
	var fileOut []byte
	if w.fileMaxLen > 0 {
		fileOut = truncateLines(p, w.fileMaxLen)
	} else {
		fileOut = p
	}
	_, fileErr := w.file.Write(fileOut)
	if fileErr != nil {
		os.Stderr.WriteString("[logger] failed to write log file: " + fileErr.Error() + "\n")
	}

	return len(p), nil
}

// Close closes the current log file if open.
func (w *LogFileWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// consoleWriter wraps an io.Writer and truncates long lines for console output.
type consoleWriter struct {
	out        io.Writer
	maxLineLen int
}

func (c *consoleWriter) Write(p []byte) (n int, err error) {
	if c.maxLineLen > 0 {
		truncated := truncateLines(p, c.maxLineLen)
		_, err = c.out.Write(truncated)
	} else {
		_, err = c.out.Write(p)
	}
	return len(p), err
}

// truncateLines truncates lines in data that exceed maxLen characters.
// Lines shorter than maxLen are kept as-is. Preserves original line endings.
func truncateLines(data []byte, maxLen int) []byte {
	var result []byte
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if len(line) > maxLen {
			result = append(result, line[:maxLen]...)
			result = append(result, "..."...)
		} else {
			result = append(result, line...)
		}
		// Add newline after all lines except the last (which may be empty
		// if data ended with \n, or the actual last line content)
		if i < len(lines)-1 {
			result = append(result, '\n')
		}
	}
	return result
}

// parseLogLevel parses a log level string (debug/info/warn/error/trace/fatal/panic)
// and returns the corresponding zerolog.Level. Defaults to info.
func parseLogLevel(s string) zerolog.Level {
	switch s {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	case "panic":
		return zerolog.PanicLevel
	case "disabled":
		return zerolog.Disabled
	default:
		return zerolog.InfoLevel
	}
}

// multiWriter combines console and file writers for zerolog.
// It writes formatted (ConsoleWriter) output to the console,
// and formatted plain text (no colors) output to the log file.
type multiWriter struct {
	console zerolog.ConsoleWriter // console: with colors, truncated
	fileFmt zerolog.ConsoleWriter // file: no colors, formatted plain text
	file    *LogFileWriter
}

func (m *multiWriter) Write(p []byte) (n int, err error) {
	// Write to console (with colors, truncated)
	m.console.Write(p)
	// Write to file (plain text formatted, no ANSI colors, with file truncation rules)
	if m.file.enabled {
		m.fileFmt.Write(p)
	}
	return len(p), nil
}

// initLogger sets up zerolog with configurable log level and optional file logging.
// LOG_LEVEL: debug/info/warn/error (default: info)
// LOG_TO_FILE: 1/true to enable daily rotating file logs
func initLogger() *LogFileWriter {
	// Parse log level from config
	level := parseLogLevel(cfg.LogLevel)
	zerolog.SetGlobalLevel(level)

	// Determine if file logging is enabled
	fileEnabled := cfg.LogToFile == "1" || cfg.LogToFile == "true"

	// File: truncate in non-debug mode, write full content in debug mode
	fileMaxLineLen := 2000
	if level <= zerolog.DebugLevel {
		fileMaxLineLen = 0 // no truncation in debug mode
	}
	fileWriter := newLogFileWriter("logs", fileEnabled, fileMaxLineLen)

	// Console: always truncate long lines for readability
	consoleMaxLineLen := 500
	consoleOut := &consoleWriter{out: os.Stderr, maxLineLen: consoleMaxLineLen}

	// ConsoleWriter for terminal (with colors, truncated)
	consoleFmt := zerolog.ConsoleWriter{Out: consoleOut, TimeFormat: "2006-01-02 15:04:05"}

	// ConsoleWriter for file (no colors, formatted plain text, with file truncation rules)
	fileOut := &consoleWriter{out: fileWriter, maxLineLen: 0} // truncation handled by LogFileWriter
	fileFmt := zerolog.ConsoleWriter{Out: fileOut, TimeFormat: "2006-01-02 15:04:05", NoColor: true}

	mw := &multiWriter{
		console: consoleFmt,
		fileFmt: fileFmt,
		file:    fileWriter,
	}
	log.Logger = zerolog.New(mw).With().Timestamp().Caller().Logger()

	if fileEnabled {
		log.Info().Msg("[logger] file logging enabled, writing to logs/")
	} else {
		log.Info().Msg("[logger] file logging disabled (set LOG_TO_FILE=1 to enable)")
	}
	log.Info().Str("level", level.String()).Msg("[logger] log level configured")

	return fileWriter
}
