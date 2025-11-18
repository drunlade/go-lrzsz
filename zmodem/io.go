package zmodem

import (
	"context"
	"io"
	"time"
)

// ReaderWithTimeout is an interface for reading with timeout support.
// It extends io.Reader with timeout capabilities.
type ReaderWithTimeout interface {
	io.Reader
	SetReadDeadline(time.Time) error
}

// zmodemIO provides buffered I/O with timeout support for ZModem protocol.
// This matches the functionality from zreadline.c in the C implementation.
type zmodemIO struct {
	reader    ReaderWithTimeout
	writer    io.Writer
	rbuf      []byte
	rpos      int
	rleft     int
	timeout   time.Duration
	noTimeout bool
	ctx       context.Context
}

// newZmodemIO creates a new ZModem I/O handler.
//
// Parameters:
//   - reader: the underlying reader (must support SetReadDeadline)
//   - writer: the underlying writer
//   - readnum: number of bytes to read in each batch
//   - bufsize: size of the read buffer
//   - timeout: default timeout for reads (in tenths of seconds, 0 = no timeout)
func newZmodemIO(reader ReaderWithTimeout, writer io.Writer, readnum int, bufsize int, timeout int) *zmodemIO {
	if bufsize < readnum {
		bufsize = readnum
	}
	
	timeoutDuration := time.Duration(timeout) * 100 * time.Millisecond // Convert tenths to duration
	if timeout == 0 {
		timeoutDuration = 0
	}
	
	return &zmodemIO{
		reader:    reader,
		writer:    writer,
		rbuf:      make([]byte, bufsize),
		rpos:      0,
		rleft:     0,
		timeout:   timeoutDuration,
		noTimeout: timeout == 0,
		ctx:       context.Background(),
	}
}

// SetContext sets the context for cancellation.
func (z *zmodemIO) SetContext(ctx context.Context) {
	z.ctx = ctx
}

// ReadByte reads a single byte with timeout handling.
// This matches READLINE_PF() macro from zreadline.c.
func (z *zmodemIO) ReadByte() (byte, error) {
	if z.rleft > 0 {
		z.rleft--
		b := z.rbuf[z.rpos]
		z.rpos++
		return b & 0xFF, nil
	}
	
	return z.readByteInternal()
}

// readByteInternal performs the actual read operation with timeout.
func (z *zmodemIO) readByteInternal() (byte, error) {
	// Check context cancellation
	if z.ctx != nil {
		select {
		case <-z.ctx.Done():
			return 0, z.ctx.Err()
		default:
		}
	}
	
	// Set read deadline if timeout is enabled
	if !z.noTimeout && z.timeout > 0 {
		deadline := time.Now().Add(z.timeout)
		if err := z.reader.SetReadDeadline(deadline); err != nil {
			return 0, err
		}
	}
	
	// Read into buffer
	z.rpos = 0
	n, err := z.reader.Read(z.rbuf)
	if err != nil {
		return 0, err
	}
	
	if n == 0 {
		return 0, io.EOF
	}
	
	z.rleft = n - 1
	if z.rleft > 0 {
		z.rpos = 1
		return z.rbuf[0] & 0xFF, nil
	}
	
	return z.rbuf[0] & 0xFF, nil
}

// Read reads bytes into the provided buffer.
func (z *zmodemIO) Read(buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		b, err := z.ReadByte()
		if err != nil {
			return total, err
		}
		buf[total] = b
		total++
	}
	return total, nil
}

// Write writes bytes to the underlying writer.
func (z *zmodemIO) Write(buf []byte) (int, error) {
	return z.writer.Write(buf)
}

// WriteByte writes a single byte.
func (z *zmodemIO) WriteByte(b byte) error {
	_, err := z.writer.Write([]byte{b})
	return err
}

// Flush flushes any buffered writes.
// For now, this is a no-op as we don't buffer writes.
func (z *zmodemIO) Flush() error {
	if f, ok := z.writer.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// PurgeLine purges any buffered input.
// This matches purgeline() from rbsb.c.
func (z *zmodemIO) PurgeLine() {
	z.rleft = 0
	z.rpos = 0
}

// noxrd7 reads a character, eating parity, XON, and XOFF characters.
// This matches the C function noxrd7() from zm.c.
func (z *zmodemIO) noxrd7() (int, error) {
	for {
		c, err := z.ReadByte()
		if err != nil {
			return 0, err
		}
		
		c &= 0x7F // Mask to 7 bits
		
		switch c {
		case XON, XOFF:
			// Skip flow control characters
			continue
		case '\r', '\n', ZDLE:
			// Always return these
			return int(c), nil
		default:
			// Check if control character escaping is enabled
			// For now, we'll return it - escaping logic is handled elsewhere
			return int(c), nil
		}
	}
}

