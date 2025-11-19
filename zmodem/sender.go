package zmodem

import (
	"context"
	"fmt"
	"io"
	"os"
)

// Sender handles sending files using the ZModem protocol.
// This implements the sender state machine from lsz.c.
type Sender struct {
	// I/O
	io          *zmodemIO
	writer      FrameWriter
	reader      FrameReader
	unescaper   *zdlreadUnescaper
	
	// Configuration
	use32bitCRC bool
	escapeCtrl  bool
	turboEscape bool
	timeout     int
	windowSize  uint
	blockSize   int
	maxBlockSize int
	
	// Receiver capabilities (from ZRINIT)
	rxflags     byte
	rxflags2    byte
	rxbuflen    uint16
	txwindow    uint
	txwspac     uint
	
	// State
	zrqinitsSent int
	znulls       int
	attn         []byte
	initialized  bool  // Set to true after successful ZRINIT exchange
	logger       Logger
	
	// Context
	ctx context.Context
}

// NewSender creates a new ZModem sender.
func NewSender(reader ReaderWithTimeout, writer io.Writer, config *SenderConfig) *Sender {
	// Create I/O layer
	zio := newZmodemIO(reader, writer, 128, 256, config.Timeout)
	
	// Create frame writer with escaping
	escaper := newZsendlineEscaper(writer, config.EscapeControl, config.TurboEscape)
	frameWriter := &frameWriterWrapper{
		writer:  writer,
		escaper: escaper,
	}
	
	// Create frame reader and unescaper
	frameReader := &frameReaderWrapper{io: zio}
	unescaper := newZdlreadUnescaper(frameReader)
	
	logger := config.Logger
	if logger == nil {
		logger = NoopLogger{}
	}
	
	return &Sender{
		io:           zio,
		writer:       frameWriter,
		reader:       frameReader,
		unescaper:    unescaper,
		use32bitCRC:  config.Use32BitCRC,
		escapeCtrl:   config.EscapeControl,
		turboEscape:  config.TurboEscape,
		timeout:      config.Timeout,
		windowSize:   config.WindowSize,
		blockSize:    config.BlockSize,
		maxBlockSize: config.MaxBlockSize,
		znulls:       config.ZNulls,
		attn:         config.Attention,
		ctx:          config.Context,
		logger:       logger,
	}
}

// SenderConfig holds configuration for a sender.
type SenderConfig struct {
	Use32BitCRC   bool
	EscapeControl bool
	TurboEscape   bool
	Timeout       int // in tenths of seconds
	WindowSize    uint
	BlockSize     int
	MaxBlockSize  int
	ZNulls        int
	Attention     []byte
	Context       context.Context
	Logger        Logger
}

// DefaultSenderConfig returns a default sender configuration.
func DefaultSenderConfig() *SenderConfig {
	return &SenderConfig{
		Use32BitCRC:   true,
		EscapeControl: false,
		TurboEscape:   false,
		Timeout:       100, // 10 seconds
		WindowSize:    0,
		BlockSize:     1024,
		MaxBlockSize:  8192,
		ZNulls:        0,
		Attention:     []byte{0x03, 0x8E, 0}, // ^C + pause
		Context:       context.Background(),
	}
}

// frameWriterWrapper wraps a writer to implement FrameWriter.
type frameWriterWrapper struct {
	writer  io.Writer
	escaper *zsendlineEscaper
}

func (f *frameWriterWrapper) Write(p []byte) (int, error) {
	return f.writer.Write(p)
}

func (f *frameWriterWrapper) WriteByte(b byte) error {
	return f.escaper.WriteByte(b)
}

func (f *frameWriterWrapper) Flush() error {
	if flusher, ok := f.writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

// frameReaderWrapper wraps zmodemIO to implement FrameReader.
type frameReaderWrapper struct {
	io *zmodemIO
}

func (f *frameReaderWrapper) Read(p []byte) (int, error) {
	return f.io.Read(p)
}

func (f *frameReaderWrapper) ReadByte() (byte, error) {
	return f.io.ReadByte()
}

// GetHeaderDirect reads a header without any state management.
// This is useful when we know a header is waiting.
func (s *Sender) GetHeaderDirect() (int, Header, error) {
	return s.getHeader(1)
}

// ParseZRINIT parses a ZRINIT header and updates sender capabilities.
func (s *Sender) ParseZRINIT(hdr Header) {
	// Parse receiver capabilities
	s.rxflags = hdr[ZF0]
	s.rxflags2 = hdr[ZF1]
	
	// Determine if we should use 32-bit CRC
	s.use32bitCRC = s.use32bitCRC && (s.rxflags&CANFC32 != 0)
	
	// Update escape control based on receiver
	oldEscape := s.escapeCtrl
	s.escapeCtrl = s.escapeCtrl || (s.rxflags&TESCCTL != 0)
	if s.escapeCtrl && !oldEscape {
		// Reinitialize escaper if needed
		if fw, ok := s.writer.(*frameWriterWrapper); ok {
			fw.escaper = newZsendlineEscaper(s.writer, s.escapeCtrl, s.turboEscape)
		}
	}
	
	// Get receiver buffer length (little-endian from ZP0, ZP1)
	s.rxbuflen = uint16(hdr[ZP0]) | (uint16(hdr[ZP1]) << 8)
	
	// Check if receiver supports full duplex
	if s.rxflags&CANFDX == 0 {
		s.txwindow = 0
	} else {
		s.txwindow = s.windowSize
	}
	
	// Validate and adjust buffer length
	if s.rxbuflen < 32 || s.rxbuflen > uint16(s.maxBlockSize) {
		s.rxbuflen = uint16(s.maxBlockSize)
	}
	if s.rxbuflen == 0 {
		s.rxbuflen = 1024
	}
	
	// Adjust block size based on receiver buffer
	if s.rxbuflen > 0 && s.blockSize > int(s.rxbuflen) {
		s.blockSize = int(s.rxbuflen)
	}
	
	// Calculate window spacing
	// Note: Original C code used /4 for unreliable serial links
	// For modern TCP/SSH, we use the full window to reduce ACK overhead
	if s.txwindow > 0 {
		s.txwspac = s.txwindow
	}
	
	// Mark as initialized
	s.initialized = true
}

// GetReceiverInit gets the receiver's initialization parameters.
// This matches getzrxinit() from lsz.c.
//
// Flow:
//   1. Send ZRQINIT (up to 4 times)
//   2. Wait for ZRINIT
//   3. Parse receiver capabilities
//   4. Send ZSINIT if needed
func (s *Sender) GetReceiverInit() error {
	oldTimeout := s.timeout
	s.timeout = 100 // 10 seconds for init
	
	dontSendZRQINIT := true
	
	for n := 10; n > 0; n-- {
		// Send ZRQINIT if needed (but not on first iteration)
		if s.zrqinitsSent < 4 && n != 10 && !dontSendZRQINIT {
			s.zrqinitsSent++
			hdr := stohdr(0)
			if err := zshhdr(s.writer, ZRQINIT, hdr); err != nil {
				return err
			}
		}
		dontSendZRQINIT = false
		
		// Wait for response
		frameType, hdr, err := s.getHeader(1)
		if err != nil {
			if IsTimeout(err) {
				// Force one more ZRQINIT on first timeout
				if n == 10 {
					continue
				}
				return err
			}
			return err
		}
		
		switch frameType {
		case ZCHALLENGE:
			// Echo receiver's challenge number
			hdr = stohdr(uint32(rclhdr(hdr)))
			if err := zshhdr(s.writer, ZACK, hdr); err != nil {
				return err
			}
			continue
			
		case ZCOMMAND:
			// Receiver didn't see our ZRQINIT
			continue
			
		case ZRINIT:
			// Parse receiver capabilities
			s.ParseZRINIT(hdr)
			s.timeout = oldTimeout
			s.initialized = true
			return s.SendZSINIT()
			
		case ZCAN:
			return NewError(ErrCancelled, "receiver cancelled")
			
		case TIMEOUT:
			// Force one more ZRQINIT on first timeout
			if n == 10 {
				continue
			}
			return NewError(ErrTimeout, "timeout waiting for ZRINIT")
			
		case ZRQINIT:
			// Remote site is also a sender
			if hdr[ZF0] == ZCOMMAND {
				continue
			}
			// Send NAK
			hdr = stohdr(0)
			if err := zshhdr(s.writer, ZNAK, hdr); err != nil {
				return err
			}
			continue
			
		default:
			// Send NAK for unknown frames
			hdr = stohdr(0)
			if err := zshhdr(s.writer, ZNAK, hdr); err != nil {
				return err
			}
			continue
		}
	}
	
	return NewError(ErrTimeout, "timeout waiting for ZRINIT")
}

// SendZSINIT sends the send-init information (attention string).
// This matches sendzsinit() from lsz.c.
func (s *Sender) SendZSINIT() error {
	// Skip if no attention string and no control escaping needed
	canSkip := len(s.attn) == 0 && (!s.escapeCtrl || (s.rxflags&TESCCTL != 0))
	if canSkip {
		// Can skip ZSINIT
		return nil
	}
	
	errors := 0
	for {
		hdr := stohdr(0)
		
		// Send attention string with null terminator
		// Match C code: sends 1+strlen(attn) bytes, minimum 1 byte
		attnData := s.attn
		if len(attnData) == 0 || attnData[len(attnData)-1] != 0 {
			// Append null terminator
			attnData = append(attnData, 0)
		}
		
		if s.escapeCtrl {
			hdr[ZF0] |= TESCCTL
			if err := zshhdr(s.writer, ZSINIT, hdr); err != nil {
				return err
			}
		} else {
			if err := zsbhdr(s.writer, ZSINIT, hdr, s.use32bitCRC, 0); err != nil {
				return err
			}
		}
		s.logger.Info(FormatFrameLog("TX", ZSINIT, hdr, attnData, len(attnData)))
		
		if err := zsdata(s.writer, attnData, ZCRCW, s.use32bitCRC); err != nil {
			return err
		}
		
		// Wait for ZACK
		frameType, rxHdr, err := s.getHeader(1)
		if err != nil {
			if errors++; errors > 19 {
				return err
			}
			continue
		}
		s.logger.Info(FormatFrameLog("RX", frameType, rxHdr, nil, 0))
		
		switch frameType {
		case ZCAN:
			return NewError(ErrCancelled, "receiver cancelled")
		case ZACK:
			return nil
		default:
			if errors++; errors > 19 {
				return NewError(ErrProtocol, "too many errors in ZSINIT")
			}
			continue
		}
	}
}

// getHeader receives a header frame.
// This matches zgethdr() from zm.c.
// Returns frame type, header, and error.
func (s *Sender) getHeader(eflag int) (int, Header, error) {
	maxGarbage := 1400 + 2400 // Zrwindow + Baudrate (defaults)
	
	for {
		cancount := 5
		
		// Read first byte
		c, err := s.io.ReadByte()
		if err != nil {
			if err == io.EOF {
				return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
			}
			return 0, Header{}, err
		}
		
		// Check for timeout/carrier lost
		if c == 0 {
			return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
		}
		
		// Check for CAN*5 sequence
		if c == CAN {
			for cancount > 0 {
				cancount--
				c2, err := s.io.ReadByte()
				if err != nil {
					return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
				}
				if c2 == CAN {
					if cancount <= 0 {
						return ZCAN, Header{}, nil
					}
					continue
				}
				if c2 == ZCRCW {
					return -1, Header{}, NewError(ErrProtocol, "ZCRCW sequence")
				}
				// Not CAN*5, break and continue
				break
			}
			if cancount <= 0 {
				return ZCAN, Header{}, nil
			}
		}
		
		// Check for ZPAD
		if c == ZPAD || c == (ZPAD|0x80) {
			// Found ZPAD, look for more ZPADs and ZDLE
			for {
				cInt, err := s.io.noxrd7()
				if err != nil {
					return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
				}
				if cInt == ZPAD {
					continue
				}
				if cInt == ZDLE {
					// Read frame format
					cInt, err = s.io.noxrd7()
					if err != nil {
						return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
					}
					
					switch cInt {
					case ZBIN:
						// Binary header (16-bit CRC)
						frameType, hdr, err := zrbhdr(s.reader, s.unescaper)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case ZBIN32:
						// Binary header (32-bit CRC)
						frameType, hdr, err := zrbhdr32(s.reader, s.unescaper)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case ZHEX:
						// Hex header
						frameType, hdr, err := zrhhdr(s.reader)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case CAN:
						// Handle CAN sequence
						cancount = 5
						for cancount > 0 {
							cancount--
							cInt2, err := s.io.ReadByte()
							if err != nil {
								return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
							}
							if cInt2 == CAN {
								if cancount <= 0 {
									return ZCAN, Header{}, nil
								}
								continue
							}
							break
						}
						if cancount <= 0 {
							return ZCAN, Header{}, nil
						}
						// Not CAN*5, continue looking
						break
					default:
						// Invalid frame format, continue looking
						maxGarbage--
						if maxGarbage == 0 {
							return -1, Header{}, NewError(ErrProtocol, "garbage count exceeded")
						}
						break
					}
					break
				}
				// Not ZDLE after ZPAD, continue looking
				maxGarbage--
				if maxGarbage == 0 {
					return -1, Header{}, NewError(ErrProtocol, "garbage count exceeded")
				}
				break
			}
		}
		
		// Not a ZModem frame - count as garbage
		maxGarbage--
		if maxGarbage == 0 {
			return -1, Header{}, NewError(ErrProtocol, "garbage count exceeded")
		}
	}
}

// SendFile sends a file using ZModem protocol.
// This matches zsendfile() from lsz.c.
//
// Parameters:
//   - filename: name of the file to send
//   - file: the file to send
//   - fileInfo: file metadata
//   - fileHeader: the ZFILE header data (filename + metadata string)
func (s *Sender) SendFile(filename string, file io.Reader, fileInfo os.FileInfo, fileHeader []byte) error {
	// Build file header flags
	var hdr Header
	hdr[ZF0] = ZCBIN // Binary transfer
	hdr[ZF1] = ZF1_ZMCLOB // Overwrite existing
	hdr[ZF2] = 0 // No transport options
	hdr[ZF3] = 0
	
	errors := 0
	for {
		// Send ZFILE header
		if err := zsbhdr(s.writer, ZFILE, hdr, s.use32bitCRC, 0); err != nil {
			return err
		}
		s.logger.Info(FormatFrameLog("TX", ZFILE, hdr, fileHeader, len(fileHeader)))
		
		// Send file header data
		if err := zsdata(s.writer, fileHeader, ZCRCW, s.use32bitCRC); err != nil {
			return err
		}
		
		// Wait for response
		frameType, rxHdr, err := s.getHeader(1)
		if err != nil {
			if errors++; errors > 10 {
				return err
			}
			continue
		}
		s.logger.Info(FormatFrameLog("RX", frameType, rxHdr, nil, 0))
		
		switch frameType {
		case ZRINIT:
			// Discard any remaining data
			for {
				c, err := s.io.ReadByte()
				if err != nil || c == ZPAD {
					break
				}
			}
			continue
			
		case ZRQINIT:
			// Remote site is also a sender
			return NewError(ErrProtocol, "remote site is sender")
			
		case ZCAN:
			return NewError(ErrCancelled, "receiver cancelled")
			
		case TIMEOUT:
			if errors++; errors > 10 {
				return NewError(ErrTimeout, "timeout waiting for file response")
			}
			continue
			
		case ZABORT, ZFIN:
			return NewError(ErrCancelled, "receiver aborted")
			
		case ZCRC:
			// Receiver wants file CRC
			// Calculate and send CRC
			crc := s.calculateFileCRC(file, fileInfo.Size())
			hdr = stohdr(crc)
			if err := zsbhdr(s.writer, ZCRC, hdr, s.use32bitCRC, 0); err != nil {
				return err
			}
			continue
			
		case ZSKIP:
			// Receiver skipped this file
			return NewError(ErrFileSkipped, "receiver skipped file")
			
		case ZRPOS:
			// Receiver wants to resume at position
			rxpos := rclhdr(rxHdr)
			if rxpos > 0 {
				// Seek to position if possible
				if seeker, ok := file.(io.Seeker); ok {
					if _, err := seeker.Seek(int64(rxpos), io.SeekStart); err != nil {
						return err
					}
				}
			}
			// Send file data
			return s.sendFileData(file, fileInfo.Size(), rxpos)
			
		default:
			if errors++; errors > 10 {
				return NewError(ErrProtocol, "unexpected frame type")
			}
			continue
		}
	}
}

// calculateFileCRC calculates the CRC32 of a file.
func (s *Sender) calculateFileCRC(file io.Reader, size int64) uint32 {
	crc := uint32(0xFFFFFFFF)
	
		// Read file and calculate CRC
		buf := make([]byte, 8192)
		remaining := size
		for remaining > 0 {
			readSize := int64(len(buf))
			if readSize > remaining {
				readSize = remaining
			}
			n, err := file.Read(buf[:readSize])
			if err != nil && err != io.EOF {
				break
			}
			if n == 0 {
				break
			}
			for i := 0; i < n; i++ {
				crc = updcrc32(buf[i], crc)
			}
			remaining -= int64(n)
		}
	
	return CRC32Finalize(crc)
}

// BuildFileHeader builds the ZFILE header data string.
// This matches the format from wctxpn() in lsz.c.
//
// Format: filename\0size mtime mode filesleft totalleft
func BuildFileHeader(filename string, fileInfo os.FileInfo, filesLeft, totalLeft int) []byte {
	// Build header: filename + null + file info
	// Format: "filename\0size mtime mode 0 filesleft totalleft"
	
	// Build file info string: "size mtime mode 0 filesleft totalleft"
	// Note: size is decimal, mtime and mode are octal
	infoStr := fmt.Sprintf("%d %o %o 0 %d %d",
		fileInfo.Size(),
		fileInfo.ModTime().Unix(), // Octal, matching lsz.c line 1239
		fileInfo.Mode()&0777,      // Only permission bits, octal
		filesLeft,
		totalLeft)
	
	// Allocate buffer large enough for filename + null + info
	totalSize := len(filename) + 1 + len(infoStr)
	buf := make([]byte, totalSize)
	pos := 0
	
	// Copy filename
	copy(buf[pos:], []byte(filename))
	pos += len(filename)
	
	// Null terminator
	buf[pos] = 0
	pos++
	
	// Copy file info
	copy(buf[pos:], []byte(infoStr))
	pos += len(infoStr)
	
	return buf
}

// sendFileData sends the file data frames.
// This matches zsendfdata() from lsz.c.
func (s *Sender) sendFileData(file io.Reader, fileSize int64, startPos uint32) error {
	bytesSent := int64(startPos)
	// Initialize to -1 so first packet doesn't trigger ZCRCW (match C code: Lastsync = rxpos - 1)
	lastRxPos := uint32(0xFFFFFFFF) // -1 in unsigned
	if startPos > 0 {
		lastRxPos = startPos - 1
	}
	junkCount := 0
	
	// Send ZDATA header
	hdr := stohdr(uint32(bytesSent))
	if err := zsbhdr(s.writer, ZDATA, hdr, s.use32bitCRC, s.znulls); err != nil {
		return err
	}
	s.logger.Info(FormatFrameLog("TX", ZDATA, hdr, nil, 0))
	
	// Seek to start position if needed
	if startPos > 0 {
		if seeker, ok := file.(io.Seeker); ok {
			if _, err := seeker.Seek(int64(startPos), io.SeekStart); err != nil {
				return err
			}
		}
	}
	
	buf := make([]byte, s.blockSize)
	txwcnt := uint(0)
	blocksSinceAck := 0
	// Force periodic ACK requests to detect dead receivers (every ~64KB in streaming mode)
	maxBlocksWithoutAck := 64
	
	for {
		// Check context
		if s.ctx != nil {
			select {
			case <-s.ctx.Done():
				return s.ctx.Err()
			default:
			}
		}
		
		// Read data block
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		
		eofSeen := err == io.EOF || n == 0
		
		// Determine frame end type
		var frameEnd int
		if eofSeen {
			frameEnd = ZCRCE
		} else if junkCount > 3 {
			frameEnd = ZCRCW
		} else if bytesSent == int64(lastRxPos) {
			frameEnd = ZCRCW
		} else if s.txwindow > 0 && (txwcnt+uint(n)) >= s.txwspac {
			txwcnt = 0
			frameEnd = ZCRCQ
		} else if blocksSinceAck >= maxBlocksWithoutAck {
			// Periodic health check: request ACK even in streaming mode
			blocksSinceAck = 0
			frameEnd = ZCRCQ
		} else {
			frameEnd = ZCRCG
		}
		
		blocksSinceAck++
		
		// Send data frame
		if err := zsdata(s.writer, buf[:n], frameEnd, s.use32bitCRC); err != nil {
			return err
		}
		
		bytesSent += int64(n)
		txwcnt += uint(n)
		
		// Handle frame end types
		if frameEnd == ZCRCW {
			// Wait for ACK with retry
			frameType, rxHdr, err := s.getHeader(0)
			if err != nil {
				// On timeout/error, increment junk count and retry
				junkCount++
				if junkCount > 10 {
					return NewError(ErrProtocol, "receiver not responding (too many timeouts)")
				}
				// Retry from current position
				lastRxPos = uint32(bytesSent) - uint32(n) // Back up one block
				continue
			}
			
			switch frameType {
			case ZACK:
				lastRxPos = rclhdr(rxHdr)
				junkCount = 0
				blocksSinceAck = 0
			case ZRPOS:
				// Receiver wants to resume at different position
				rxpos := rclhdr(rxHdr)
				if rxpos != uint32(bytesSent) {
					// Seek and resend from new position
					if seeker, ok := file.(io.Seeker); ok {
						if _, err := seeker.Seek(int64(rxpos), io.SeekStart); err != nil {
							return err
						}
					}
					bytesSent = int64(rxpos)
					lastRxPos = rxpos
					
					// Send new ZDATA header
					hdr := stohdr(uint32(bytesSent))
					if err := zsbhdr(s.writer, ZDATA, hdr, s.use32bitCRC, s.znulls); err != nil {
						return err
					}
					continue
				}
			case ZCAN, ZABORT:
				return NewError(ErrCancelled, "receiver cancelled")
			case ZSKIP:
				return NewError(ErrFileSkipped, "receiver skipped")
			default:
				junkCount++
				if junkCount > 10 {
					return NewError(ErrProtocol, "too many errors")
				}
			}
		} else if frameEnd == ZCRCQ {
			// Wait for ACK but continue sending (with timeout detection)
			frameType, rxHdr, err := s.getHeader(1)
			if err != nil {
				// Timeout or error - receiver may be dead
				junkCount++
				s.logger.Debug("ZCRCQ timeout/error (junkCount=%d): %v", junkCount, err)
				if junkCount > 5 {
					return NewError(ErrProtocol, "receiver not responding (no ACK for ZCRCQ)")
				}
			} else if frameType == ZACK {
				lastRxPos = rclhdr(rxHdr)
				junkCount = 0
				blocksSinceAck = 0
			} else if frameType == ZCAN {
				return NewError(ErrCancelled, "receiver cancelled")
			} else {
				junkCount++
				if junkCount > 5 {
					return NewError(ErrProtocol, "unexpected response to ZCRCQ")
				}
			}
		}
		
		// Check window size
		if s.txwindow > 0 {
			windowUsed := uint32(bytesSent) - lastRxPos
			if windowUsed >= uint32(s.txwindow) {
				// Window full - wait for ACK with retry
				for retries := 0; retries < 3; retries++ {
					frameType, rxHdr, err := s.getHeader(1)
					if err != nil {
						if retries >= 2 {
							return NewError(ErrProtocol, "receiver not responding (window ACK timeout)")
						}
						continue
					}
					if frameType == ZACK {
						lastRxPos = rclhdr(rxHdr)
						blocksSinceAck = 0
						break
					} else if frameType == ZCAN {
						return NewError(ErrCancelled, "receiver cancelled")
					} else {
						return NewError(ErrProtocol, "expected ZACK")
					}
				}
			}
		}
		
		if eofSeen {
			break
		}
	}
	
	// Send ZEOF
	for {
		hdr := stohdr(uint32(bytesSent))
		if err := zsbhdr(s.writer, ZEOF, hdr, s.use32bitCRC, 0); err != nil {
			return err
		}
		s.logger.Info(FormatFrameLog("TX", ZEOF, hdr, nil, 0))
		
		frameType, rxHdr, err := s.getHeader(0)
		if err != nil {
			return err
		}
		s.logger.Info(FormatFrameLog("RX", frameType, rxHdr, nil, 0))
		
		switch frameType {
		case ZACK:
			// File complete
			return nil
		case ZRPOS:
			// Receiver wants more data - resend from position
			rxpos := rclhdr(rxHdr)
			if seeker, ok := file.(io.Seeker); ok {
				if _, err := seeker.Seek(int64(rxpos), io.SeekStart); err != nil {
					return err
				}
			}
			bytesSent = int64(rxpos)
			return s.sendFileData(file, fileSize, uint32(rxpos))
		case ZRINIT:
			// New session
			return nil
		case ZSKIP:
			return NewError(ErrFileSkipped, "receiver skipped")
		default:
			return NewError(ErrProtocol, "unexpected response to ZEOF")
		}
	}
}

