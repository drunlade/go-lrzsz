package zmodem

import (
	"context"
	"io"
	"os"
	"sync"
)

// TerminalIO wraps an SSH reader/writer to automatically handle terminal output
// and ZModem protocol detection. It acts as middleware - you provide the SSH
// reader/writer and it handles everything else.
type TerminalIO struct {
	// Underlying I/O
	reader io.Reader
	writer io.Writer

	// Configuration
	config    *Config
	callbacks *Callbacks
	ctx       context.Context
	logger    Logger

	// State
	mu            sync.Mutex
	inZModem      bool
	scanBuffer    []byte // Last few bytes for detection
	maxScanBuffer int
	
	// ZModem session (created when ZModem is detected)
	zmodemSession *Session
}

// NewTerminalIO creates a new TerminalIO middleware that wraps SSH reader/writer.
// It automatically detects ZModem sequences and handles transfers transparently.
//
// The returned TerminalIO provides:
//   - TerminalReader(): io.Reader for terminal output (passes through normally, handles ZModem automatically)
//   - TerminalWriter(): io.Writer for terminal input (passes through normally)
//
// Example:
//
//	termIO := zmodem.NewTerminalIO(sshStdout, sshStdin, zmodem.WithCallbacks(callbacks))
//	go io.Copy(os.Stdout, termIO.TerminalReader())
//	go io.Copy(termIO.TerminalWriter(), os.Stdin)
func NewTerminalIO(reader io.Reader, writer io.Writer, opts ...Option) *TerminalIO {
	// Create default config and callbacks
	config := DefaultConfig()
	callbacks := defaultCallbacks()
	ctx := context.Background()
	var logger Logger = NoopLogger{}

	// Apply options
	for _, opt := range opts {
		// We need to create a temporary session to apply options
		// This is a bit of a hack, but we'll create the real session when needed
		tempSession := &Session{
			config:    config,
			callbacks: callbacks,
			ctx:       ctx,
		}
		opt(tempSession)
		config = tempSession.config
		callbacks = tempSession.callbacks
		ctx = tempSession.ctx
	}

	termIO := &TerminalIO{
		reader:        reader,
		writer:        writer,
		config:        config,
		callbacks:     callbacks,
		ctx:           ctx,
		logger:        logger,
		scanBuffer:    make([]byte, 0, 16),
		maxScanBuffer: 16, // Keep last 16 bytes for detection
	}

	return termIO
}

// NewTerminalIOWithLogger creates a TerminalIO with a logger
func NewTerminalIOWithLogger(reader io.Reader, writer io.Writer, logger Logger, opts ...Option) *TerminalIO {
	termIO := NewTerminalIO(reader, writer, opts...)
	termIO.logger = logger
	termIO.logger.Info("TerminalIO initialized with logging")
	return termIO
}

// TerminalReader returns an io.Reader that provides terminal output.
// ZModem sequences are automatically detected and handled.
func (t *TerminalIO) TerminalReader() io.Reader {
	return t
}

// TerminalWriter returns an io.Writer that accepts terminal input.
// Data is passed through to the underlying writer.
func (t *TerminalIO) TerminalWriter() io.Writer {
	return t.writer
}

// Read implements io.Reader for TerminalIO.
// It passes data through and scans for ZModem sequences.
func (t *TerminalIO) Read(p []byte) (int, error) {
	t.mu.Lock()
	inZModem := t.inZModem
	t.mu.Unlock()
	
	// If in ZModem mode, block application reads
	// The ZModem session is reading directly from the underlying reader
	if inZModem {
		// Wait for ZModem to finish
		for {
			t.mu.Lock()
			if !t.inZModem {
				t.mu.Unlock()
				break
			}
			t.mu.Unlock()
			// Small sleep to avoid busy wait
			select {
			case <-t.ctx.Done():
				return 0, t.ctx.Err()
			default:
			}
		}
		// ZModem finished, continue with normal read
	}
	
	// Read directly from underlying reader (no buffering)
	n, err := t.reader.Read(p)
	
	if n > 0 {
		t.logger.Debug("TerminalIO: Read %d bytes: %q", n, p[:n])
		
		// Scan the data for ZModem sequences
		t.mu.Lock()
		if !t.inZModem {
			// First check the current read buffer
			zmodemStart := t.findZModemStartInBuffer(p[:n])
			if zmodemStart >= 0 {
				// Found ZModem in current buffer!
				t.logger.Info("ZModem sequence detected at position %d in current read: %q", zmodemStart, p[:n])
				t.inZModem = true
				
				// Save the ZModem data (from detection point onwards) for the ZModem session
				t.scanBuffer = make([]byte, n-zmodemStart)
				copy(t.scanBuffer, p[zmodemStart:n])
				t.logger.Debug("Buffered %d bytes of ZModem data for session: %q", len(t.scanBuffer), t.scanBuffer)
				
				t.mu.Unlock()
				
				// Handle ZModem transfer synchronously
				// This blocks the application read until transfer completes
				t.handleZModemTransfer()
				
				// After transfer, continue reading
				return t.reader.Read(p)
			}
			
			// Not found in current buffer - check across boundary with previous data
			t.scanBuffer = append(t.scanBuffer, p[:n]...)
			// Keep only last maxScanBuffer bytes to check boundaries
			if len(t.scanBuffer) > t.maxScanBuffer {
				t.scanBuffer = t.scanBuffer[len(t.scanBuffer)-t.maxScanBuffer:]
			}
			
			// Check for ZModem sequence that spans the boundary
			zmodemStart = t.findZModemStartInBuffer(t.scanBuffer)
			if zmodemStart >= 0 {
				// Found ZModem spanning boundary!
				t.logger.Info("ZModem sequence detected at position %d spanning buffers: %q", zmodemStart, t.scanBuffer)
				t.inZModem = true
				
				// Keep the ZModem data (from detection point onwards) for the ZModem session
				t.scanBuffer = t.scanBuffer[zmodemStart:]
				t.logger.Debug("Buffered %d bytes of ZModem data for session (spanning): %q", len(t.scanBuffer), t.scanBuffer)
				
				t.mu.Unlock()
				
				// Handle ZModem transfer synchronously
				t.handleZModemTransfer()
				
				// After transfer, continue reading
				return t.reader.Read(p)
			}
		}
		t.mu.Unlock()
	}
	
	if err != nil && err != io.EOF {
		t.logger.Error("TerminalIO: Read error: %v", err)
	}
	
	return n, err
}

// findZModemStartInBuffer looks for ZModem ZRINIT initiation sequences in a buffer.
// Only ZRINIT (receiver init, frame type 01) should trigger automatic ZModem handling.
// Other frame types like ZFIN (frame type 08) should NOT trigger a new session.
func (t *TerminalIO) findZModemStartInBuffer(buf []byte) int {
	// Look for ZRINIT sequences only
	for i := 0; i < len(buf)-2; i++ {
		if buf[i] == ZPAD {
			// Check for ZPAD ZPAD ZDLE ZHEX (hex frame with ZDLE)
			if i+5 < len(buf) && buf[i+1] == ZPAD && buf[i+2] == ZDLE && buf[i+3] == ZHEX {
				// Check if frame type is ZRINIT (01)
				if buf[i+4] == '0' && buf[i+5] == '1' {
					t.logger.Debug("Found ZRINIT hex frame (with ZDLE) at position %d", i)
					return i
				}
				// Not ZRINIT - skip
				continue
			}
			// Check for ZPAD ZPAD ZHEX (hex frame - ZDLE might be omitted)
			// This handles the case where we see "**B01..." directly
			if i+4 < len(buf) && buf[i+1] == ZPAD && buf[i+2] == ZHEX {
				// Check if frame type is ZRINIT (01)
				if buf[i+3] == '0' && buf[i+4] == '1' {
					t.logger.Debug("Found ZRINIT hex frame (no ZDLE) at position %d", i)
					return i
				}
				// Not ZRINIT - skip
				continue
			}
		}
	}
	return -1
}

// bufferedReader wraps a reader with a prepended buffer
type bufferedReader struct {
	buffer []byte
	offset int
	reader io.Reader
}

func (br *bufferedReader) Read(p []byte) (int, error) {
	// First, drain the buffer
	if br.offset < len(br.buffer) {
		n := copy(p, br.buffer[br.offset:])
		br.offset += n
		return n, nil
	}
	// Buffer drained, read from underlying reader
	return br.reader.Read(p)
}

// handleZModemTransfer handles a detected ZModem transfer.
func (t *TerminalIO) handleZModemTransfer() {
	t.logger.Info("Starting ZModem transfer handling")
	
	defer func() {
		t.mu.Lock()
		t.inZModem = false
		t.scanBuffer = t.scanBuffer[:0] // Clear scan buffer after transfer
		t.mu.Unlock()
		t.logger.Info("ZModem transfer completed")
	}()

	// Create a buffered reader that starts with the already-read ZModem data
	t.mu.Lock()
	buffered := &bufferedReader{
		buffer: t.scanBuffer,
		offset: 0,
		reader: t.reader,
	}
	t.mu.Unlock()

	// Wrap reader/writer with logging if logger is enabled
	var reader io.Reader = buffered
	var writer io.Writer = t.writer
	
	if _, ok := t.logger.(NoopLogger); !ok {
		reader = NewLoggingReader(buffered, t.logger, "ZModem-Reader")
		writer = NewLoggingWriter(t.writer, t.logger, "ZModem-Writer")
	}

	// Create reader with timeout wrapper
	sshReader := &sshReader{
		reader:  reader,
		timeout: t.config.Timeout,
	}

	// Create ZModem session
	t.logger.Info("Creating ZModem session")
	t.zmodemSession = NewSession(sshReader, writer,
		WithConfig(t.config),
		WithCallbacks(t.callbacks),
		WithContext(t.ctx),
		WithSessionLogger(t.logger),
	)

	// Determine our role based on what the remote sent
	// If the buffered data contains ZRINIT, the remote is the receiver (rz), so we send
	// Otherwise, we're the receiver
	isRemoteReceiver := t.detectZRINIT(t.scanBuffer)
	
	if isRemoteReceiver {
		t.logger.Info("Remote is receiver (detected ZRINIT) - we will send files")
		// Remote ran 'rz' - they want us to send files
		// For now, we don't have files to send automatically, so just send ZFIN
		// Applications should use SendFileWhenRemoteReceives for explicit sends
		
		// Remote ran 'rz' - they want files from us
		// We need to initialize as sender and send session end (no files)
		
		sender := t.zmodemSession.sender
		if sender == nil {
			t.logger.Error("No sender available")
			return
		}
		
		// Parse the buffered ZRINIT manually (remote already sent it)
		t.logger.Info("Parsing receiver ZRINIT from buffer")
		frameType, hdr, err := sender.GetHeaderDirect()
		if err != nil {
			t.logger.Error("Failed to read ZRINIT: %v", err)
			return
		}
		
		if frameType != ZRINIT {
			t.logger.Error("Expected ZRINIT, got %s(%d)", FrameTypeName(frameType), frameType)
			return
		}
		
		t.logger.Info("Received ZRINIT, flags=[%02x %02x %02x %02x]", hdr[0], hdr[1], hdr[2], hdr[3])
		
		// Parse receiver capabilities
		sender.ParseZRINIT(hdr)

		// Send ZSINIT (required after ZRINIT)
		t.logger.Info("Calling SendZSINIT")
		if err := sender.SendZSINIT(); err != nil {
			t.logger.Error("Failed to send ZSINIT: %v", err)
			return
		}
		t.logger.Info("SendZSINIT completed")

		// Check if we have files to send
		var filesToSend []string
		if t.callbacks.OnFileList != nil {
			t.logger.Info("Asking application for files to send")
			files, err := t.callbacks.OnFileList()
			if err != nil {
				t.logger.Error("OnFileList error: %v", err)
			} else {
				filesToSend = files
			}
		}
		
		if len(filesToSend) > 0 {
			// Send files
			t.logger.Info("Sending %d file(s)", len(filesToSend))
			for _, filename := range filesToSend {
				// Open file
				var file io.Reader
				var info os.FileInfo
				var err error
				
				if t.callbacks.OnFileOpen != nil {
					file, info, err = t.callbacks.OnFileOpen(filename)
				} else {
					// Default: open file
					f, err := os.Open(filename)
					if err != nil {
						t.logger.Error("Failed to open %s: %v", filename, err)
						continue
					}
					defer f.Close()
					info, err = f.Stat()
					if err != nil {
						t.logger.Error("Failed to stat %s: %v", filename, err)
						continue
					}
					file = f
				}
				
				if err != nil {
					t.logger.Error("Failed to open %s: %v", filename, err)
					continue
				}
				
				// Send file
				t.logger.Info("Sending file: %s (%d bytes)", filename, info.Size())
				if err := t.zmodemSession.SendFile(t.ctx, filename, file, info); err != nil {
					// Check if file was skipped
					if zmErr, ok := err.(*Error); ok && zmErr.Type == ErrFileSkipped {
						t.logger.Info("File skipped by receiver: %s", filename)
						continue
					}
					// Other errors - log and break
					t.logger.Error("Failed to send %s: %v", filename, err)
					break
				}
				t.logger.Info("File sent successfully: %s", filename)
			}
			
			// Send ZFIN to end the session
			t.logger.Info("All files sent, sending ZFIN")
			hdr = stohdr(0)
			if err := zshhdr(writer, ZFIN, hdr); err != nil {
				t.logger.Error("Failed to send ZFIN: %v", err)
				return
			}
		} else {
			// No files to send - send ZFIN to end the session
			t.logger.Info("No files to send, sending ZFIN")
			hdr = stohdr(0)
			if err := zshhdr(writer, ZFIN, hdr); err != nil {
				t.logger.Error("Failed to send ZFIN: %v", err)
				return
			}
		}
		
		// Wait for response from receiver and handle accordingly (match saybibi() in lsz.c)
		t.logger.Debug("Waiting for acknowledgment from receiver")
		frameType, _, err = sender.GetHeaderDirect()
		if err != nil {
			t.logger.Error("Error getting ack: %v", err)
		} else {
			switch frameType {
			case ZFIN:
				t.logger.Debug("Received ZFIN, sending 'OO'")
				// Send "OO" to confirm end of session
				writer.Write([]byte("OO"))
				if flusher, ok := writer.(interface{ Flush() error }); ok {
					flusher.Flush()
				}
			case ZCAN:
				t.logger.Info("Received ZCAN from receiver (transfer cancelled)")
				// Just return, no "OO" needed
			default:
				t.logger.Debug("Received frame type %d, ending session", frameType)
			}
		}
		t.logger.Info("Session cleanup complete")
	} else {
		t.logger.Info("We are receiver - waiting for files")
		// We're the receiver - receive files
		if err := t.zmodemSession.ReceiveFiles(t.ctx, 0); err != nil {
			t.logger.Error("ReceiveFiles error: %v", err)
			return
		}
		t.logger.Info("ReceiveFiles completed successfully")
	}
}

// detectZRINIT checks if the buffer contains a ZRINIT frame
// ZRINIT means the remote is a receiver (running 'rz') and we should be the sender
func (t *TerminalIO) detectZRINIT(buf []byte) bool {
	// ZRINIT is frame type 1
	// Hex frame format: ** ZDLE ZHEX <type> <pos0> <pos1> <pos2> <pos3> <crc0> <crc1>
	// Where type, pos, and crc are hex encoded
	
	if len(buf) < 6 {
		return false
	}
	
	// Look for hex frame start: ** ZDLE ZHEX (0x2a 0x2a 0x18 0x42)
	for i := 0; i < len(buf)-5; i++ {
		if buf[i] == 0x2a && buf[i+1] == 0x2a && buf[i+2] == 0x18 && buf[i+3] == 0x42 {
			// Found hex frame, check if it's ZRINIT (type 1)
			// After ZHEX, next 2 chars are hex digits for frame type
			// ZRINIT = 1, so we're looking for "01"
			if buf[i+4] == '0' && buf[i+5] == '1' {
				t.logger.Debug("Detected ZRINIT frame in buffer - remote is receiver")
				return true
			}
		}
	}
	
	return false
}

