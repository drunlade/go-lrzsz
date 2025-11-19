package zmodem

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger interface for ZModem protocol logging
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// FileLogger writes logs to a file
type FileLogger struct {
	file *os.File
	mu   sync.Mutex
}

// NewFileLogger creates a logger that writes to a file
func NewFileLogger(path string) (*FileLogger, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	return &FileLogger{file: file}, nil
}

func (l *FileLogger) log(level, format string, args ...interface{}) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.file, "[%s] %s: %s\n", timestamp, level, msg)
}

func (l *FileLogger) Debug(format string, args ...interface{}) {
	l.log("DEBUG", format, args...)
}

func (l *FileLogger) Info(format string, args ...interface{}) {
	l.log("INFO", format, args...)
}

func (l *FileLogger) Error(format string, args ...interface{}) {
	l.log("ERROR", format, args...)
}

func (l *FileLogger) Close() error {
	if l != nil && l.file != nil {
		return l.file.Close()
	}
	return nil
}

// NoopLogger does nothing
type NoopLogger struct{}

func (NoopLogger) Debug(format string, args ...interface{}) {}
func (NoopLogger) Info(format string, args ...interface{})  {}
func (NoopLogger) Error(format string, args ...interface{}) {}

// FormatFrameLog formats a frame for logging with optional data truncation
func FormatFrameLog(direction string, frameType int, hdr Header, data []byte, dataSize int) string {
	frameName := FrameTypeName(frameType)
	pos := rclhdr(hdr)
	
	msg := fmt.Sprintf("%s %s (pos=%d, hdr=[%02x %02x %02x %02x])", 
		direction, frameName, pos, hdr[0], hdr[1], hdr[2], hdr[3])
	
	if dataSize > 0 {
		msg += fmt.Sprintf(", data_size=%d", dataSize)
		if data != nil && len(data) > 0 {
			displayLen := len(data)
			truncated := false
			if displayLen > 128 {
				displayLen = 128
				truncated = true
			}
			if truncated {
				msg += fmt.Sprintf(", data=%q...[truncated]", data[:displayLen])
			} else {
				msg += fmt.Sprintf(", data=%q", data[:displayLen])
			}
		}
	}
	
	return msg
}

// LoggingReader wraps a reader and logs all reads
type LoggingReader struct {
	reader io.Reader
	logger Logger
	name   string
}

func NewLoggingReader(reader io.Reader, logger Logger, name string) *LoggingReader {
	return &LoggingReader{
		reader: reader,
		logger: logger,
		name:   name,
	}
}

func (lr *LoggingReader) Read(p []byte) (int, error) {
	n, err := lr.reader.Read(p)
	if lr.logger != nil && n > 10 {
		// Only log significant reads to reduce noise
		data := p[:n]
		if n > 128 {
			lr.logger.Debug("%s: Read %d bytes: %q...[truncated]", lr.name, n, data[:128])
		} else {
			lr.logger.Debug("%s: Read %d bytes: %q", lr.name, n, data)
		}
	}
	if err != nil && err != io.EOF && lr.logger != nil {
		lr.logger.Error("%s: Read error: %v", lr.name, err)
	}
	return n, err
}

// LoggingWriter wraps a writer and logs all writes
type LoggingWriter struct {
	writer io.Writer
	logger Logger
	name   string
}

func NewLoggingWriter(writer io.Writer, logger Logger, name string) *LoggingWriter {
	return &LoggingWriter{
		writer: writer,
		logger: logger,
		name:   name,
	}
}

func (lw *LoggingWriter) Write(p []byte) (int, error) {
	n, err := lw.writer.Write(p)
	if lw.logger != nil && n > 10 {
		// Only log significant writes to reduce noise
		data := p[:n]
		if n > 128 {
			lw.logger.Debug("%s: Wrote %d bytes: %q...[truncated]", lw.name, n, data[:128])
		} else {
			lw.logger.Debug("%s: Wrote %d bytes: %q", lw.name, n, data)
		}
	}
	if err != nil && lw.logger != nil {
		lw.logger.Error("%s: Write error: %v", lw.name, err)
	}
	return n, err
}

