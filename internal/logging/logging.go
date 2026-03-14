package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
)

// LineClearer is implemented by the progress bar to clear its terminal line
// before a log message is printed.
type LineClearer interface {
	ClearLine()
	IsTTY() bool
}

// Logger provides dual-output logging: stdout for the user, file for persistent records.
// Errors and warnings are counted and summarized at the end.
type Logger struct {
	stdout  *log.Logger
	file    *log.Logger
	logFile *os.File
	verbose bool
	errors  int64
	warns   int64
	logPath string

	mu      sync.Mutex
	clearer LineClearer // optional progress bar line clearer
}

// New creates a Logger that writes to both stdout and the given log file.
// If logPath is empty, file logging is disabled.
func New(logPath string, verbose bool) (*Logger, error) {
	l := &Logger{
		stdout:  log.New(os.Stdout, "", 0),
		verbose: verbose,
		logPath: logPath,
	}

	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file %s: %w", logPath, err)
		}
		l.logFile = f
		l.file = log.New(f, "", log.Ldate|log.Ltime|log.Lmicroseconds)
	}

	return l, nil
}

// SetProgress registers a progress bar so the logger can clear its line
// before printing messages to avoid garbled output.
func (l *Logger) SetProgress(c LineClearer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clearer = c
}

// ClearProgress unregisters the progress bar.
func (l *Logger) ClearProgress() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clearer = nil
}

// printStdout clears the progress line (if any), prints the message,
// and lets the next progress tick re-render.
func (l *Logger) printStdout(msg string) {
	l.mu.Lock()
	c := l.clearer
	l.mu.Unlock()

	if c != nil {
		c.ClearLine()
	}
	l.stdout.Println(msg)
}

// Info logs an informational message to stdout and the log file.
func (l *Logger) Info(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	l.printStdout(msg)
	if l.file != nil {
		l.file.Println("[INFO] " + msg)
	}
}

// Debug logs a debug message to the log file always, and to stdout only if verbose.
func (l *Logger) Debug(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if l.verbose {
		l.printStdout(msg)
	}
	if l.file != nil {
		l.file.Println("[DEBUG] " + msg)
	}
}

// Warn logs a warning. During progress (TTY mode), warnings only go to the log file
// to avoid flooding the terminal. Without progress, they go to both outputs.
func (l *Logger) Warn(format string, args ...interface{}) {
	atomic.AddInt64(&l.warns, 1)
	msg := fmt.Sprintf(format, args...)

	l.mu.Lock()
	c := l.clearer
	l.mu.Unlock()

	if c == nil || !c.IsTTY() {
		// No progress bar active, or non-TTY: print to stdout normally.
		l.printStdout("WARNING: " + msg)
	}
	// Always write to log file.
	if l.file != nil {
		l.file.Println("[WARN] " + msg)
	}
}

// Error logs an error to both outputs and increments the error counter.
// Errors always print to stdout (clearing the progress line first).
func (l *Logger) Error(format string, args ...interface{}) {
	atomic.AddInt64(&l.errors, 1)
	msg := fmt.Sprintf(format, args...)
	l.printStdout("ERROR: " + msg)
	if l.file != nil {
		l.file.Println("[ERROR] " + msg)
	}
}

// Summary prints a final summary line if any errors or warnings occurred.
func (l *Logger) Summary() {
	e := atomic.LoadInt64(&l.errors)
	w := atomic.LoadInt64(&l.warns)
	if e > 0 || w > 0 {
		msg := fmt.Sprintf("%d error(s), %d warning(s)", e, w)
		if l.logPath != "" {
			msg += fmt.Sprintf(" logged to %s", l.logPath)
		}
		l.stdout.Println(msg)
	}
}

// ErrorCount returns the number of errors logged.
func (l *Logger) ErrorCount() int64 {
	return atomic.LoadInt64(&l.errors)
}

// WarnCount returns the number of warnings logged.
func (l *Logger) WarnCount() int64 {
	return atomic.LoadInt64(&l.warns)
}

// Writer returns an io.Writer that writes to the log file (or io.Discard if none).
func (l *Logger) Writer() io.Writer {
	if l.logFile != nil {
		return l.logFile
	}
	return io.Discard
}

// Close flushes and closes the log file.
func (l *Logger) Close() {
	if l.logFile != nil {
		l.logFile.Close()
	}
}
