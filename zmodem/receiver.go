package zmodem

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// Receiver handles receiving files using the ZModem protocol.
// This implements the receiver state machine from lrz.c.
type Receiver struct {
	// I/O
	io          *zmodemIO
	writer      io.Writer
	reader      FrameReader
	unescaper   *zdlreadUnescaper
	
	// Configuration
	use32bitCRC bool
	escapeCtrl  bool
	turboEscape bool
	timeout     int
	bufferSize  int
	
	// Sender capabilities (from ZFILE)
	txflags     byte
	zconv       byte
	zmanag      byte
	ztrans      byte
	
	// State
	zrqinitsReceived int
	attn             []byte
	
	// Context
	ctx context.Context
	
	// Logger
	logger Logger
}

// ReceiverConfig holds configuration for a receiver.
type ReceiverConfig struct {
	Use32BitCRC   bool
	EscapeControl bool
	TurboEscape   bool
	Timeout       int // in tenths of seconds
	BufferSize    int
	Attention     []byte
	Context       context.Context
	Logger        Logger
}

// DefaultReceiverConfig returns a default receiver configuration.
func DefaultReceiverConfig() *ReceiverConfig {
	return &ReceiverConfig{
		Use32BitCRC:   true,
		EscapeControl: false,
		TurboEscape:   false,
		Timeout:       100, // 10 seconds
		BufferSize:    8192,
		Attention:     []byte{0x03, 0x8E, 0}, // ^C + pause
		Context:       context.Background(),
	}
}

// NewReceiver creates a new ZModem receiver.
func NewReceiver(reader ReaderWithTimeout, writer io.Writer, config *ReceiverConfig) *Receiver {
	// Create I/O layer
	zio := newZmodemIO(reader, writer, 128, 256, config.Timeout)
	
	// Create frame reader and unescaper
	frameReader := &frameReaderWrapper{io: zio}
	unescaper := newZdlreadUnescaper(frameReader)
	
	if config.Logger == nil {
		config.Logger = NoopLogger{}
	}
	
	r := &Receiver{
		io:           zio,
		writer:       writer,
		reader:       frameReader,
		unescaper:    unescaper,
		use32bitCRC:  config.Use32BitCRC,
		escapeCtrl:   config.EscapeControl,
		turboEscape:  config.TurboEscape,
		timeout:      config.Timeout,
		bufferSize:   config.BufferSize,
		attn:         config.Attention,
		ctx:          config.Context,
		logger:       config.Logger,
	}
	
	r.logger.Info("Receiver created (CRC32=%v, escapeCtrl=%v)", config.Use32BitCRC, config.EscapeControl)
	return r
}

// SendZRINIT sends a ZRINIT frame to initialize the receiver.
// This matches the ZRINIT sending in tryz() from lrz.c.
func (r *Receiver) SendZRINIT(frameType int) error {
	var hdr Header
	
	// Set buffer length (0 = use default)
	stohdr(0)
	
	// Set capability flags
	hdr[ZF0] = CANFC32 | CANFDX | CANOVIO
	if r.escapeCtrl {
		hdr[ZF0] |= ESCCTL // TESCCTL == ESCCTL
	}
	hdr[ZF1] = 0
	hdr[ZF2] = 0
	hdr[ZF3] = 0
	
	r.logger.Info("SendZRINIT: frame=%s(%d), flags=[%02x %02x %02x %02x]", 
		FrameTypeName(frameType), frameType, hdr[ZF0], hdr[ZF1], hdr[ZF2], hdr[ZF3])
	
	// Send hex header (initial frames use hex)
	err := zshhdr(r.writer, frameType, hdr)
	if err != nil {
		r.logger.Error("SendZRINIT: zshhdr error: %v", err)
	}
	return err
}

// WaitForZFILE waits for a ZFILE frame from the sender.
// This matches tryz() from lrz.c.
//
// Returns:
//   - fileHeader: the ZFILE header data (filename + metadata)
//   - error: any error that occurred
func (r *Receiver) WaitForZFILE() ([]byte, error) {
	maxTries := 15
	errors := 0
	
	r.logger.Info("WaitForZFILE: starting (maxTries=%d)", maxTries)
	
	for n := maxTries; n > 0 && r.zrqinitsReceived < 10; n-- {
		// Send ZRINIT
		if err := r.SendZRINIT(ZRINIT); err != nil {
			return nil, err
		}
		
		// Wait for response
		r.logger.Debug("WaitForZFILE: waiting for response (try %d/%d)", maxTries-n+1, maxTries)
		frameType, hdr, err := r.getHeader(0)
		if err != nil {
			if IsTimeout(err) {
				r.logger.Debug("WaitForZFILE: timeout waiting for response")
				continue
			}
			r.logger.Error("WaitForZFILE: getHeader error: %v", err)
			return nil, err
		}
		
		r.logger.Info("WaitForZFILE: received frame %s(%d), hdr=[%02x %02x %02x %02x]", 
			FrameTypeName(frameType), frameType, hdr[0], hdr[1], hdr[2], hdr[3])
		
		switch frameType {
		case ZRQINIT:
			// Sender is initializing - this is OK
			r.logger.Debug("WaitForZFILE: received ZRQINIT (count=%d)", r.zrqinitsReceived+1)
			r.zrqinitsReceived++
			continue
			
		case ZEOF:
			// Ignore EOF frames during init
			continue
			
		case TIMEOUT:
			continue
			
		case ZFILE:
			// Parse file header flags
			r.logger.Info("WaitForZFILE: received ZFILE frame")
			r.zconv = hdr[ZF0]
			if r.zconv == 0 {
				r.zconv = ZCBIN // Default to binary
			}
			
			// Check for skip-if-not-found flag
			skipIfNotFound := (hdr[ZF1] & ZF1_ZMSKNOLOC) != 0
			if skipIfNotFound {
				hdr[ZF1] &^= ZF1_ZMSKNOLOC
			}
			
			r.zmanag = hdr[ZF1]
			r.ztrans = hdr[ZF2]
			
			r.logger.Debug("WaitForZFILE: zconv=%02x, zmanag=%02x, ztrans=%02x", r.zconv, r.zmanag, r.ztrans)
			
			// Receive file header data
			fileHeader := make([]byte, r.bufferSize)
			r.logger.Debug("WaitForZFILE: receiving file header data")
			bytesReceived, frameEnd, err := zrdata(r.reader, r.unescaper, fileHeader, r.use32bitCRC)
			if err != nil {
				r.logger.Error("WaitForZFILE: zrdata error: %v", err)
				// Send NAK
				hdr = stohdr(0)
				if err := zshhdr(r.writer, ZNAK, hdr); err != nil {
					return nil, err
				}
				if errors++; errors > 20 {
					return nil, err
				}
				continue
			}
			
			r.logger.Debug("WaitForZFILE: received %d bytes, frameEnd=%d", bytesReceived, frameEnd)
			
			if frameEnd == GOTCRCW {
				r.logger.Info("WaitForZFILE: file header complete")
				return fileHeader[:bytesReceived], nil
			}
			
			// Send NAK for incomplete frame
			hdr = stohdr(0)
			if err := zshhdr(r.writer, ZNAK, hdr); err != nil {
				return nil, err
			}
			if errors++; errors > 20 {
				return nil, NewError(ErrProtocol, "file header incomplete")
			}
			continue
			
		case ZSINIT:
			// Sender is sending attention string
			r.escapeCtrl = r.escapeCtrl || (hdr[ZF0]&TESCCTL != 0)
			
			attnBuf := make([]byte, ZATTNLEN)
			bytesReceived, frameEnd, err := zrdata(r.reader, r.unescaper, attnBuf, r.use32bitCRC)
			if err != nil || frameEnd != GOTCRCW {
				// Send NAK
				hdr = stohdr(0)
				if err := zshhdr(r.writer, ZNAK, hdr); err != nil {
					return nil, err
				}
				continue
			}
			
			// Store attention string
			if bytesReceived > 0 {
				r.attn = attnBuf[:bytesReceived]
			}
			
			// Send ZACK
			hdr = stohdr(1)
			if err := zshhdr(r.writer, ZACK, hdr); err != nil {
				return nil, err
			}
			continue
			
		case ZFREECNT:
			// Sender wants free space count
			// For now, return a large value
			freeSpace := uint32(1024 * 1024 * 1024) // 1GB
			hdr = stohdr(freeSpace)
			if err := zshhdr(r.writer, ZACK, hdr); err != nil {
				return nil, err
			}
			continue
			
		case ZCOMMAND:
			// Remote command execution - not supported by default
			cmdBuf := make([]byte, r.bufferSize)
			bytesReceived, frameEnd, err := zrdata(r.reader, r.unescaper, cmdBuf, r.use32bitCRC)
			if err != nil || frameEnd != GOTCRCW {
				// Send NAK
				hdr = stohdr(0)
				if err := zshhdr(r.writer, ZNAK, hdr); err != nil {
					return nil, err
				}
				continue
			}
			
			// Deny command execution
			hdr = stohdr(0)
			if err := zshhdr(r.writer, ZCOMPL, hdr); err != nil {
				return nil, err
			}
			
			// Wait for ZFIN
			for errors := 0; errors < 20; errors++ {
				frameType, _, err := r.getHeader(1)
				if err != nil {
					continue
				}
				if frameType == ZFIN {
					// Send OO
					r.writer.Write([]byte("OO"))
					return nil, NewError(ErrRemoteCommandDenied, string(cmdBuf[:bytesReceived]))
				}
			}
			return nil, NewError(ErrRemoteCommandDenied, string(cmdBuf[:bytesReceived]))
			
		case ZCOMPL:
			// Transaction complete
			continue
			
		case ZFIN:
			// Session finished
			// Send OO
			r.writer.Write([]byte("OO"))
			return nil, NewError(ErrCancelled, "session finished")
			
		case ZRINIT:
			// Remote site is also a receiver
			return nil, NewError(ErrProtocol, "remote site is receiver")
			
		case ZCAN:
			return nil, NewError(ErrCancelled, "sender cancelled")
			
		default:
			continue
		}
	}
	
	return nil, NewError(ErrTimeout, "timeout waiting for ZFILE")
}

// ParseFileHeader parses the ZFILE header data.
// This matches procheader() from lrz.c.
//
// Format: filename\0size mtime mode 0 filesleft totalleft
func ParseFileHeader(data []byte) (filename string, size int64, mtime int64, mode os.FileMode, filesLeft, totalLeft int, err error) {
	// Find null terminator
	nullPos := -1
	for i, b := range data {
		if b == 0 {
			nullPos = i
			break
		}
	}
	
	if nullPos < 0 {
		return "", 0, 0, 0, 0, 0, NewError(ErrInvalidFrame, "no null terminator in file header")
	}
	
	filename = string(data[:nullPos])
	
	// Skip padding zeros
	infoStart := nullPos + 1
	for infoStart < len(data) && data[infoStart] == 0 {
		infoStart++
	}
	
	// Parse file info string: "size mtime mode 0 filesleft totalleft"
	if infoStart >= len(data) {
		// No file info - use defaults
		return filename, 0, 0, 0644, 0, 0, nil
	}
	
	infoStr := string(data[infoStart:])
	fields := strings.Fields(infoStr)
	
	if len(fields) >= 1 {
		fmt.Sscanf(fields[0], "%d", &size)
	}
	if len(fields) >= 2 {
		fmt.Sscanf(fields[1], "%d", &mtime)
	}
	if len(fields) >= 3 {
		var modeInt uint
		fmt.Sscanf(fields[2], "%o", &modeInt)
		mode = os.FileMode(modeInt)
	}
	if len(fields) >= 5 {
		fmt.Sscanf(fields[4], "%d", &filesLeft)
	}
	if len(fields) >= 6 {
		fmt.Sscanf(fields[5], "%d", &totalLeft)
	}
	
	return filename, size, mtime, mode, filesLeft, totalLeft, nil
}

// ReceiveFile receives a file using ZModem protocol.
// This matches rzfile() from lrz.c.
//
// Parameters:
//   - file: the file to write to
//   - expectedSize: expected file size (0 if unknown)
func (r *Receiver) ReceiveFile(file io.Writer, expectedSize int64) error {
	bytesReceived := int64(0)
	errors := 0
	maxErrors := 20
	
	for {
		// Send ZRPOS with current position
		hdr := stohdr(uint32(bytesReceived))
		if err := zshhdr(r.writer, ZRPOS, hdr); err != nil {
			return err
		}
		
		// Wait for response
		frameType, rxHdr, err := r.getHeader(0)
		if err != nil {
			if IsTimeout(err) {
				if errors++; errors > maxErrors {
					return NewError(ErrTimeout, "too many timeouts")
				}
				continue
			}
			return err
		}
		
		switch frameType {
		case ZNAK:
			if errors++; errors > maxErrors {
				return NewError(ErrProtocol, "too many NAKs")
			}
			continue
			
		case TIMEOUT:
			if errors++; errors > maxErrors {
				return NewError(ErrTimeout, "too many timeouts")
			}
			continue
			
		case ZFILE:
			// New file - discard data
			buf := make([]byte, r.bufferSize)
			zrdata(r.reader, r.unescaper, buf, r.use32bitCRC)
			continue
			
		case ZEOF:
			// Check if EOF is at correct position
			eofPos := rclhdr(rxHdr)
			if eofPos != uint32(bytesReceived) {
				// Wrong position - ignore and continue
				errors = 0
				continue
			}
			
			// File complete
			// Send ZACK
			hdr = stohdr(uint32(bytesReceived))
			if err := zshhdr(r.writer, ZACK, hdr); err != nil {
				return err
			}
			return nil
			
		case ZSKIP:
			// Sender skipped this file
			return NewError(ErrFileSkipped, "sender skipped file")
			
		case ZDATA:
			// Check if data is at correct position
			dataPos := rclhdr(rxHdr)
			if dataPos != uint32(bytesReceived) {
				// Out of sync - send attention and continue
				if errors++; errors > maxErrors {
					return NewError(ErrProtocol, "out of sync")
				}
				if len(r.attn) > 0 {
					r.writer.Write(r.attn)
				}
				continue
			}
			
			// Receive data
			buf := make([]byte, r.bufferSize)
			n, frameEnd, err := zrdata(r.reader, r.unescaper, buf, r.use32bitCRC)
			if err != nil {
				if errors++; errors > maxErrors {
					return err
				}
				if len(r.attn) > 0 {
					r.writer.Write(r.attn)
				}
				continue
			}
			
			switch frameEnd {
			case ZCAN:
				return NewError(ErrCancelled, "sender cancelled")
			case -1: // ERROR/CRC error
				if errors++; errors > maxErrors {
					return NewError(ErrCRC, "too many CRC errors")
				}
				if len(r.attn) > 0 {
					r.writer.Write(r.attn)
				}
				continue
			case TIMEOUT:
				if errors++; errors > maxErrors {
					return NewError(ErrTimeout, "timeout receiving data")
				}
				continue
			case GOTCRCW:
				// Write data and send ZACK
				if _, err := file.Write(buf[:n]); err != nil {
					return err
				}
				bytesReceived += int64(n)
				errors = 0
				
				hdr = stohdr(uint32(bytesReceived))
				if err := zshhdr(r.writer, ZACK|0x80, hdr); err != nil {
					return err
				}
				continue
			case GOTCRCQ:
				// Write data and send ZACK, continue receiving
				if _, err := file.Write(buf[:n]); err != nil {
					return err
				}
				bytesReceived += int64(n)
				errors = 0
				
				hdr = stohdr(uint32(bytesReceived))
				if err := zshhdr(r.writer, ZACK, hdr); err != nil {
					return err
				}
				continue
			case GOTCRCG:
				// Write data and continue receiving (no ACK)
				if _, err := file.Write(buf[:n]); err != nil {
					return err
				}
				bytesReceived += int64(n)
				errors = 0
				continue
			case GOTCRCE:
				// Write data and wait for next header
				if _, err := file.Write(buf[:n]); err != nil {
					return err
				}
				bytesReceived += int64(n)
				errors = 0
				continue
			}
			
		default:
			if errors++; errors > maxErrors {
				return NewError(ErrProtocol, fmt.Sprintf("unexpected frame type: %d", frameType))
			}
			continue
		}
	}
}

// getHeader receives a header frame (same as sender).
func (r *Receiver) getHeader(eflag int) (int, Header, error) {
	maxGarbage := 1400 + 2400 // Zrwindow + Baudrate (defaults)
	
	for {
		cancount := 5
		
		// Read first byte
		c, err := r.io.ReadByte()
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
				c2, err := r.io.ReadByte()
				if err != nil {
					return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
				}
				if c2 == CAN {
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
		}
		
		// Check for ZPAD
		if c == ZPAD || c == (ZPAD|0x80) {
			// Found ZPAD, look for more ZPADs and ZDLE
			for {
				cInt, err := r.io.noxrd7()
				if err != nil {
					return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
				}
				if cInt == ZPAD {
					continue
				}
				if cInt == ZDLE {
					// Read frame format
					cInt, err = r.io.noxrd7()
					if err != nil {
						return TIMEOUT, Header{}, NewError(ErrTimeout, "timeout")
					}
					
					switch cInt {
					case ZBIN:
						// Binary header (16-bit CRC)
						frameType, hdr, err := zrbhdr(r.reader, r.unescaper)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case ZBIN32:
						// Binary header (32-bit CRC)
						frameType, hdr, err := zrbhdr32(r.reader, r.unescaper)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case ZHEX:
						// Hex header
						frameType, hdr, err := zrhhdr(r.reader)
						if err != nil {
							return 0, Header{}, err
						}
						return frameType, hdr, nil
					case CAN:
						// Handle CAN sequence
						cancount = 5
						for cancount > 0 {
							cancount--
							cInt2, err := r.io.ReadByte()
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

