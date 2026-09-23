package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogLevel represents the log level
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

// Logger represents the application logger
type Logger struct {
	debugMode bool
	logFile   *os.File
	logger    *log.Logger
	level     LogLevel
	// buffering
	bufEnabled    atomic.Bool
	buf           []string
	bufMu         sync.Mutex
	maxBatch      int
	flushInterval time.Duration
	flushTicker   *time.Ticker
	quitCh        chan struct{}
}

var (
	defaultLogger *Logger
)

// Init initializes the logger
func Init(debugMode bool, logDir string) error {
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create log file with timestamp
	timestamp := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("mynet-%s.log", timestamp)
	logFilePath := filepath.Join(logDir, logFileName)

	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	// Always write only to file; avoid console output for interactive app
	var writer io.Writer = logFile

	defaultLogger = &Logger{
		debugMode:     debugMode,
		logFile:       logFile,
		logger:        log.New(writer, "", 0), // We'll handle formatting ourselves
		level:         INFO,
		maxBatch:      50,
		flushInterval: 2 * time.Second,
		quitCh:        make(chan struct{}),
	}

	if debugMode {
		defaultLogger.level = DEBUG
	}

	return nil
}

// Close closes the logger
func Close() {
	if defaultLogger != nil {
		DisableBuffering() // ensures flush
		if defaultLogger.logFile != nil {
			_ = defaultLogger.logFile.Sync()
			defaultLogger.logFile.Close()
		}
	}
}

// SetLevel sets the log level
func SetLevel(level LogLevel) {
	if defaultLogger != nil {
		defaultLogger.level = level
	}
}

// formatMessage formats a log message with timestamp, level, and caller info
func (l *Logger) formatMessage(level LogLevel, msg string) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	// Get caller info
	_, file, line, ok := runtime.Caller(3)
	var caller string
	if ok {
		filename := filepath.Base(file)
		caller = fmt.Sprintf("%s:%d", filename, line)
	} else {
		caller = "unknown"
	}

	levelStr := ""
	switch level {
	case DEBUG:
		levelStr = "DEBUG"
	case INFO:
		levelStr = "INFO "
	case WARN:
		levelStr = "WARN "
	case ERROR:
		levelStr = "ERROR"
	}

	return fmt.Sprintf("[%s] %s [%s] %s", timestamp, levelStr, caller, msg)
}

// log logs a message at the specified level
func (l *Logger) log(level LogLevel, format string, args ...interface{}) {
	if l == nil || level < l.level {
		return
	}

	message := fmt.Sprintf(format, args...)
	formatted := l.formatMessage(level, message)
	if l.bufEnabled.Load() {
		l.enqueue(formatted)
	} else {
		l.logger.Println(formatted)
	}
}

// enqueue adds a formatted line into buffer and triggers flush when needed
func (l *Logger) enqueue(line string) {
	l.bufMu.Lock()
	l.buf = append(l.buf, line)
	shouldFlush := len(l.buf) >= l.maxBatch
	l.bufMu.Unlock()
	if shouldFlush {
		go l.Flush()
	}
}

// Flush forces flushing buffered log lines to disk.
func (l *Logger) Flush() {
	if l == nil || !l.bufEnabled.Load() {
		return
	}
	l.bufMu.Lock()
	if len(l.buf) == 0 {
		l.bufMu.Unlock()
		return
	}
	lines := make([]string, len(l.buf))
	copy(lines, l.buf)
	l.buf = l.buf[:0]
	l.bufMu.Unlock()
	// single write
	if l.logFile != nil {
		_, _ = l.logFile.WriteString(strings.Join(lines, "\n") + "\n")
	} else if l.logger != nil { // fallback
		for _, ln := range lines {
			l.logger.Println(ln)
		}
	}
}

// EnableBuffering enables asynchronous batched flushing.
// interval: flush interval; maxBatch: flush when this many lines accumulated.
func EnableBuffering(interval time.Duration, maxBatch int) {
	if defaultLogger == nil {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if maxBatch <= 0 {
		maxBatch = 50
	}
	l := defaultLogger
	l.bufMu.Lock()
	l.flushInterval = interval
	l.maxBatch = maxBatch
	if l.flushTicker != nil {
		l.flushTicker.Stop()
	}
	l.flushTicker = time.NewTicker(l.flushInterval)
	l.bufMu.Unlock()
	if !l.bufEnabled.Load() {
		l.bufEnabled.Store(true)
		go l.flushLoop()
	}
}

func (l *Logger) flushLoop() {
	for {
		select {
		case <-l.flushTicker.C:
			l.Flush()
		case <-l.quitCh:
			l.Flush()
			return
		}
	}
}

// DisableBuffering disables buffering and flushes remaining lines.
func DisableBuffering() {
	if defaultLogger == nil {
		return
	}
	l := defaultLogger
	if !l.bufEnabled.Load() {
		return
	}
	l.bufEnabled.Store(false)
	if l.flushTicker != nil {
		l.flushTicker.Stop()
	}
	close(l.quitCh)
	l.Flush()
	// new quit channel for possible re-enable
	l.quitCh = make(chan struct{})
}

// Flush forces flush on default logger.
func Flush() {
	if defaultLogger != nil {
		defaultLogger.Flush()
	}
}

// Debug logs a debug message
func Debug(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(DEBUG, format, args...)
	}
}

// Info logs an info message
func Info(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(INFO, format, args...)
	}
}

// Warn logs a warning message
func Warn(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(WARN, format, args...)
	}
}

// Error logs an error message
func Error(format string, args ...interface{}) {
	if defaultLogger != nil {
		defaultLogger.log(ERROR, format, args...)
	}
}

// LogAPIRequest logs API request details
func LogAPIRequest(method, url string, headers map[string]string, body string) {
	if defaultLogger == nil || !defaultLogger.debugMode {
		return
	}

	Debug("=== API REQUEST ===")
	Debug("Method: %s", method)
	Debug("URL: %s", url)

	if len(headers) > 0 {
		Debug("Headers:")
		for key, value := range headers {
			// Mask sensitive headers
			if strings.ToLower(key) == "authorization" {
				if len(value) > 20 {
					value = value[:20] + "..."
				}
			}
			Debug("  %s: %s", key, value)
		}
	}

	if body != "" {
		Debug("Body: %s", body)
	}
}

// LogAPIResponse logs API response details
func LogAPIResponse(statusCode int, headers map[string]string, body string) {
	if defaultLogger == nil || !defaultLogger.debugMode {
		return
	}

	Debug("=== API RESPONSE ===")
	Debug("Status Code: %d", statusCode)

	if len(headers) > 0 {
		Debug("Headers:")
		for key, value := range headers {
			Debug("  %s: %s", key, value)
		}
	}

	if body != "" {
		Debug("Body: %s", body)
	}
	Debug("===================")
}

// LogUserAction logs user actions
func LogUserAction(action, details string) {
	Info("USER_ACTION: %s - %s", action, details)
}

// LogAuthEvent logs authentication events
func LogAuthEvent(event, user, result string) {
	Info("AUTH_EVENT: %s - User: %s - Result: %s", event, user, result)
}

// IsDebugMode returns true if debug mode is enabled
func IsDebugMode() bool {
	return defaultLogger != nil && defaultLogger.debugMode
}
