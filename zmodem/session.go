package zmodem

import (
	"context"
	"io"
	"os"
	path "path/filepath"
	"time"
)

// Session represents a ZModem transfer session.
// It provides a high-level API for sending and receiving files.
type Session struct {
	// I/O
	reader ReaderWithTimeout
	writer io.Writer

	// Configuration
	config *Config

	// Callbacks
	callbacks *Callbacks

	// Internal state
	sender   *Sender
	receiver *Receiver

	// Context
	ctx context.Context

	// Logger
	logger Logger
}

// Config holds session configuration.
type Config struct {
	// Protocol options
	Use32BitCRC   bool
	EscapeControl bool
	TurboEscape   bool

	// Timeouts (in tenths of seconds)
	Timeout int

	// Window size
	WindowSize uint

	// Block size
	BlockSize    int
	MaxBlockSize int

	// ZNulls (number of nulls before ZDATA)
	ZNulls int

	// Attention string
	Attention []byte

	// Progress update interval
	ProgressInterval time.Duration
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		Use32BitCRC:      true,
		EscapeControl:    false,
		TurboEscape:      false,
		Timeout:          100, // 10 seconds
		WindowSize:       0,
		BlockSize:        1024 * 2,
		MaxBlockSize:     1024 * 8,
		ZNulls:           0,
		Attention:        []byte{0x03, 0x8E, 0}, // ^C + pause
		ProgressInterval: 100 * time.Millisecond,
	}
}

// Option configures a Session.
type Option func(*Session)

// WithConfig sets the session configuration.
func WithConfig(config *Config) Option {
	return func(s *Session) {
		s.config = config
	}
}

// WithCallbacks sets the session callbacks.
func WithCallbacks(callbacks *Callbacks) Option {
	return func(s *Session) {
		s.callbacks = mergeCallbacks(callbacks)
	}
}

// WithContext sets the session context.
func WithContext(ctx context.Context) Option {
	return func(s *Session) {
		s.ctx = ctx
	}
}

// WithLogger sets a logger for protocol debugging.
func WithSessionLogger(logger Logger) Option {
	return func(s *Session) {
		s.logger = logger
	}
}

// NewSession creates a new ZModem session.
func NewSession(reader ReaderWithTimeout, writer io.Writer, opts ...Option) *Session {
	s := &Session{
		reader:    reader,
		writer:    writer,
		config:    DefaultConfig(),
		callbacks: defaultCallbacks(),
		ctx:       context.Background(),
	}

	for _, opt := range opts {
		opt(s)
	}

	// Create sender and receiver (will be initialized when needed)
	senderConfig := &SenderConfig{
		Use32BitCRC:      s.config.Use32BitCRC,
		EscapeControl:    s.config.EscapeControl,
		TurboEscape:      s.config.TurboEscape,
		Timeout:          s.config.Timeout,
		WindowSize:       s.config.WindowSize,
		BlockSize:        s.config.BlockSize,
		MaxBlockSize:     s.config.MaxBlockSize,
		ZNulls:           s.config.ZNulls,
		Attention:        s.config.Attention,
		Context:          s.ctx,
		Logger:           s.logger,
		Callbacks:        s.callbacks,
		ProgressInterval: s.config.ProgressInterval,
	}

	receiverConfig := &ReceiverConfig{
		Use32BitCRC:   s.config.Use32BitCRC,
		EscapeControl: s.config.EscapeControl,
		TurboEscape:   s.config.TurboEscape,
		Timeout:       s.config.Timeout,
		BufferSize:    s.config.MaxBlockSize,
		Attention:     s.config.Attention,
		Context:       s.ctx,
		Logger:        s.logger,
	}

	s.sender = NewSender(reader, writer, senderConfig)
	s.receiver = NewReceiver(reader, writer, receiverConfig)

	return s
}

// SendFile sends a file over the session.
// This is a high-level wrapper around the sender implementation.
func (s *Session) SendFile(ctx context.Context, filename string, file io.Reader, fileInfo os.FileInfo) error {
	// Use context from session if not provided
	if ctx == nil {
		ctx = s.ctx
	}

	// Notify file start
	_, actualFileName := path.Split(filename)
	s.callbacks.OnFileStart(actualFileName, fileInfo.Size(), fileInfo.Mode())

	// Build file header
	fileHeader := BuildFileHeader(actualFileName, fileInfo, 0, 0)

	// Initialize receiver if needed (skip if already initialized)
	if !s.sender.initialized {
		if err := s.sender.GetReceiverInit(); err != nil {
			s.callbacks.OnError(err, "receiver initialization")
			return err
		}
	}

	// Send file
	err := s.sender.SendFile(actualFileName, file, fileInfo, fileHeader)

	if err != nil {
		s.callbacks.OnError(err, "send file")
		return err
	}

	// Notify file complete
	s.callbacks.OnFileComplete(actualFileName, fileInfo.Size(), 0)

	return nil
}

// ReceiveFile receives a file over the session.
// This is a high-level wrapper around the receiver implementation.
func (s *Session) ReceiveFile(ctx context.Context) error {
	// Use context from session if not provided
	if ctx == nil {
		ctx = s.ctx
	}

	s.logger.Info("ReceiveFile: waiting for ZFILE")

	// Wait for ZFILE
	fileHeader, err := s.receiver.WaitForZFILE()
	if err != nil {
		s.logger.Error("ReceiveFile: WaitForZFILE error: %v", err)
		s.callbacks.OnError(err, "wait for ZFILE")
		return err
	}

	s.logger.Debug("ReceiveFile: got ZFILE header: %q", fileHeader)

	// Parse file header
	filename, size, mtime, mode, _, _, err := ParseFileHeader(fileHeader)
	if err != nil {
		s.logger.Error("ReceiveFile: ParseFileHeader error: %v", err)
		s.callbacks.OnError(err, "parse file header")
		return err
	}

	s.logger.Info("ReceiveFile: file=%s, size=%d, mode=%o, mtime=%d", filename, size, mode, mtime)

	// Prompt user
	accept, err := s.callbacks.OnFilePrompt(filename, size, mode)
	if err != nil {
		return err
	}
	if !accept {
		// Send ZSKIP
		hdr := stohdr(0)
		if err := zshhdr(s.writer, ZSKIP, hdr); err != nil {
			return err
		}
		return NewError(ErrFileSkipped, filename)
	}

	// Create file
	var file io.Writer
	if s.callbacks.OnFileCreate != nil {
		file, err = s.callbacks.OnFileCreate(filename, size, mode)
	} else {
		// Default: create file
		file, err = os.Create(filename)
	}
	if err != nil {
		s.callbacks.OnError(err, "create file")
		return err
	}

	// Close file when done
	if closer, ok := file.(io.Closer); ok {
		defer closer.Close()
	}

	// Notify file start
	s.callbacks.OnFileStart(filename, size, mode)

	// Receive file
	s.logger.Info("ReceiveFile: receiving %d bytes", size)
	err = s.receiver.ReceiveFile(file, size)

	if err != nil {
		s.logger.Error("ReceiveFile: ReceiveFile error: %v", err)
		s.callbacks.OnError(err, "receive file")
		return err
	}

	s.logger.Info("ReceiveFile: completed")

	// Set file permissions and mtime if possible
	if fileInfo, ok := file.(interface {
		Chmod(os.FileMode) error
		SetModTime(time.Time) error
	}); ok {
		fileInfo.Chmod(mode)
		fileInfo.SetModTime(time.Unix(mtime, 0))
	} else if f, ok := file.(*os.File); ok {
		os.Chmod(f.Name(), mode)
		os.Chtimes(f.Name(), time.Unix(mtime, 0), time.Unix(mtime, 0))
	}

	// Notify file complete
	s.callbacks.OnFileComplete(filename, size, 0)

	return nil
}

// SendFiles sends multiple files over the session.
func (s *Session) SendFiles(ctx context.Context, files []FileInfo) error {
	// Initialize receiver
	if err := s.sender.GetReceiverInit(); err != nil {
		return err
	}

	// Send each file
	for _, fileInfo := range files {
		// Open file
		var file io.Reader
		var err error
		if s.callbacks.OnFileOpen != nil {
			file, _, err = s.callbacks.OnFileOpen(fileInfo.Filename)
		} else {
			// Default: open file
			f, err := os.Open(fileInfo.Filename)
			if err != nil {
				s.callbacks.OnError(err, "open file")
				continue
			}
			file = f
			defer f.Close()

			// Get file info
			info, err := f.Stat()
			if err != nil {
				s.callbacks.OnError(err, "stat file")
				continue
			}
			fileInfo.Info = info
		}

		if err != nil {
			s.callbacks.OnError(err, "open file")
			continue
		}

		// Send file
		if err := s.SendFile(ctx, fileInfo.Filename, file, fileInfo.Info); err != nil {
			// Check if file was skipped
			if zmErr, ok := err.(*Error); ok && zmErr.Type == ErrFileSkipped {
				// File skipped by receiver, log and continue
				s.logger.Info("File skipped by receiver: %s", fileInfo.Filename)
				s.callbacks.OnError(err, "send file")
				continue
			}

			// For other errors, check if we should retry
			if s.callbacks.OnError(err, "send file") {
				// Retry once
				if err := s.SendFile(ctx, fileInfo.Filename, file, fileInfo.Info); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	return nil
}

// ReceiveFiles receives multiple files over the session.
func (s *Session) ReceiveFiles(ctx context.Context, maxFiles int) error {
	filesReceived := 0

	for {
		// Check max files limit
		if maxFiles > 0 && filesReceived >= maxFiles {
			break
		}

		// Receive file
		err := s.ReceiveFile(ctx)
		if err != nil {
			if IsCancelled(err) || err == NewError(ErrFileSkipped, "") {
				// File skipped or cancelled - continue
				continue
			}
			return err
		}

		filesReceived++
	}

	return nil
}

// FileInfo holds information about a file to transfer.
type FileInfo struct {
	Filename string
	Info     os.FileInfo
}
