package zmodem

import (
	"fmt"
	"io"
)

// Header represents a ZModem frame header.
// The header contains 4 bytes that can represent:
// - Position (file offset) - little-endian 32-bit value
// - Flags (ZF0-ZF3) - various flag bytes depending on frame type
type Header [4]byte

// stohdr stores a position value in a header using little-endian byte order.
// This matches the C function stohdr() from zm.c.
//
// Position is stored as:
//   hdr[ZP0] = pos (low byte)
//   hdr[ZP1] = pos >> 8
//   hdr[ZP2] = pos >> 16
//   hdr[ZP3] = pos >> 24 (high byte)
func stohdr(pos uint32) Header {
	var hdr Header
	hdr[ZP0] = byte(pos)
	hdr[ZP1] = byte(pos >> 8)
	hdr[ZP2] = byte(pos >> 16)
	hdr[ZP3] = byte(pos >> 24)
	return hdr
}

// rclhdr recovers a 32-bit position value from a header using little-endian byte order.
// This matches the C function rclhdr() from zm.c.
//
// Position is recovered as:
//   pos = hdr[ZP0] | (hdr[ZP1] << 8) | (hdr[ZP2] << 16) | (hdr[ZP3] << 24)
func rclhdr(hdr Header) uint32 {
	return uint32(hdr[ZP0]) |
		uint32(hdr[ZP1])<<8 |
		uint32(hdr[ZP2])<<16 |
		uint32(hdr[ZP3])<<24
}

// zputhex writes a byte as two lowercase hex digits.
// This matches the C function zputhex() from zm.c.
func zputhex(c byte, pos []byte) {
	digits := "0123456789abcdef"
	pos[0] = digits[(c&0xF0)>>4]
	pos[1] = digits[c&0x0F]
}

// zgethex reads two hex digits and decodes them into a byte.
// Returns the decoded byte or an error if invalid hex digits are encountered.
func zgethex(r io.Reader) (byte, error) {
	var buf [2]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	
	n := hexDigitValue(buf[0])
	if n < 0 {
		return 0, fmt.Errorf("invalid hex digit: %c", buf[0])
	}
	
	c := hexDigitValue(buf[1])
	if c < 0 {
		return 0, fmt.Errorf("invalid hex digit: %c", buf[1])
	}
	
	return byte((n << 4) | c), nil
}

// hexDigitValue converts a hex digit character to its numeric value.
// Returns -1 if the character is not a valid hex digit.
func hexDigitValue(c byte) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}
	if c >= 'a' && c <= 'f' {
		return int(c - 'a' + 10)
	}
	if c >= 'A' && c <= 'F' {
		return int(c - 'A' + 10)
	}
	return -1
}

// FrameWriter is an interface for writing ZModem frames.
// It extends io.Writer with methods for sending individual bytes
// that may need ZDLE escaping.
type FrameWriter interface {
	io.Writer
	WriteByte(byte) error
	Flush() error
}

// FrameReader is an interface for reading ZModem frames.
// It extends io.Reader with methods for reading individual bytes
// that may need ZDLE unescaping.
type FrameReader interface {
	io.Reader
	ReadByte() (byte, error)
}

// zsbhdr sends a ZModem binary header.
// This matches the C function zsbhdr() from zm.c.
//
// Parameters:
//   - w: the writer (with ZDLE escaping)
//   - frameType: the frame type (ZRQINIT, ZRINIT, etc.)
//   - hdr: the 4-byte header (position or flags)
//   - use32bitCRC: if true, use 32-bit CRC (ZBIN32), otherwise 16-bit (ZBIN)
//   - znulls: number of null bytes to send before ZDATA frames
func zsbhdr(w FrameWriter, frameType int, hdr Header, use32bitCRC bool, znulls int) error {
	// Send nulls for ZDATA frames
	if frameType == ZDATA {
		for i := 0; i < znulls; i++ {
			if err := w.WriteByte(0); err != nil {
				return err
			}
		}
	}
	
	// Send ZPAD and ZDLE (raw, no escaping - match xsendline in C)
	if _, err := w.Write([]byte{ZPAD, ZDLE}); err != nil {
		return err
	}
	
	if use32bitCRC {
		return zsbhdr32(w, frameType, hdr)
	}
	
	// 16-bit CRC binary header (raw, no escaping - match xsendline in C)
	if _, err := w.Write([]byte{ZBIN}); err != nil {
		return err
	}
	
	// Send frame type with escaping
	escaper := newZsendlineEscaper(w, false, false)
	if err := escaper.WriteByte(byte(frameType)); err != nil {
		return err
	}
	
	// Calculate CRC
	crc := updcrc16(byte(frameType), 0)
	
	// Send header bytes with escaping
	for i := 0; i < 4; i++ {
		if err := escaper.WriteByte(hdr[i]); err != nil {
			return err
		}
		crc = updcrc16(hdr[i], crc)
	}
	
	// Finalize CRC
	crc = CRC16Finalize(crc)
	
	// Send CRC bytes with escaping
	if err := escaper.WriteByte(byte(crc >> 8)); err != nil {
		return err
	}
	if err := escaper.WriteByte(byte(crc)); err != nil {
		return err
	}
	
	// Flush if not ZDATA
	if frameType != ZDATA {
		return w.Flush()
	}
	
	return nil
}

// zsbhdr32 sends a ZModem binary header with 32-bit CRC.
// This matches the C function zsbh32() from zm.c.
func zsbhdr32(w FrameWriter, frameType int, hdr Header) error {
	// Send ZBIN32 (raw, no escaping - match xsendline in C)
	if _, err := w.Write([]byte{ZBIN32}); err != nil {
		return err
	}
	
	// Send frame type with escaping
	escaper := newZsendlineEscaper(w, false, false)
	if err := escaper.WriteByte(byte(frameType)); err != nil {
		return err
	}
	
	// Calculate CRC (initialized to 0xFFFFFFFF)
	crc := uint32(0xFFFFFFFF)
	crc = updcrc32(byte(frameType), crc)
	
	// Send header bytes with escaping
	for i := 0; i < 4; i++ {
		crc = updcrc32(hdr[i], crc)
		if err := escaper.WriteByte(hdr[i]); err != nil {
			return err
		}
	}
	
	// Finalize CRC
	crc = CRC32Finalize(crc)
	
	// Send CRC bytes (little-endian) with escaping
	for i := 0; i < 4; i++ {
		crcByte := byte(crc)
		if err := escaper.WriteByte(crcByte); err != nil {
			return err
		}
		crc >>= 8
	}
	
	return nil
}

// zshhdr sends a ZModem hex header.
// This matches the C function zshhdr() from zm.c.
func zshhdr(w io.Writer, frameType int, hdr Header) error {
	buf := make([]byte, 30)
	pos := 0
	
	// Build hex header
	buf[pos] = ZPAD
	pos++
	buf[pos] = ZPAD
	pos++
	buf[pos] = ZDLE
	pos++
	buf[pos] = ZHEX
	pos++
	
	// Encode frame type as hex
	zputhex(byte(frameType&0x7F), buf[pos:])
	pos += 2
	
	// Calculate CRC
	crc := updcrc16(byte(frameType&0x7F), 0)
	
	// Encode header bytes as hex
	for i := 0; i < 4; i++ {
		zputhex(hdr[i], buf[pos:])
		pos += 2
		crc = updcrc16(hdr[i], crc)
	}
	
	// Finalize CRC
	crc = CRC16Finalize(crc)
	
	// Encode CRC as hex
	zputhex(byte(crc>>8), buf[pos:])
	pos += 2
	zputhex(byte(crc), buf[pos:])
	pos += 2
	
	// Add CR and LF
	buf[pos] = 0x0D // CR
	pos++
	buf[pos] = 0x8A // LF (0x0A | 0x80)
	pos++
	
	// Add XON for non-FIN/ACK frames (uncork remote)
	if frameType != ZFIN && frameType != ZACK {
		buf[pos] = 0x21 // XON
		pos++
	}
	
	// Write the header
	_, err := w.Write(buf[:pos])
	return err
}

// zrbhdr receives a binary header (16-bit CRC).
// This matches the C function zrbhdr() from zm.c.
func zrbhdr(r FrameReader, unescaper *zdlreadUnescaper) (int, Header, error) {
	var hdr Header
	
	// Read frame type
	c, err := unescaper.ReadByte()
	if err != nil {
		return 0, hdr, err
	}
	if c < 0 {
		return 0, hdr, NewError(ErrInvalidFrame, "invalid frame type")
	}
	
	frameType := int(c)
	crc := updcrc16(byte(frameType), 0)
	
	// Read header bytes
	for i := 0; i < 4; i++ {
		c, err := unescaper.ReadByte()
		if err != nil {
			return 0, hdr, err
		}
		if c < 0 {
			return 0, hdr, NewError(ErrInvalidFrame, "invalid header byte")
		}
		hdr[i] = byte(c)
		crc = updcrc16(hdr[i], crc)
	}
	
	// Read CRC bytes
	crcHigh, err := unescaper.ReadByte()
	if err != nil {
		return 0, hdr, err
	}
	if crcHigh < 0 {
		return 0, hdr, NewError(ErrInvalidFrame, "invalid CRC byte")
	}
	crc = updcrc16(byte(crcHigh), crc)
	
	crcLow, err := unescaper.ReadByte()
	if err != nil {
		return 0, hdr, err
	}
	if crcLow < 0 {
		return 0, hdr, NewError(ErrInvalidFrame, "invalid CRC byte")
	}
	crc = updcrc16(byte(crcLow), crc)
	
	// Verify CRC
	if crc != 0 {
		return 0, hdr, NewError(ErrCRC, "bad CRC")
	}
	
	return frameType, hdr, nil
}

// zrbhdr32 receives a binary header (32-bit CRC).
// This matches the C function zrbhdr32() from zm.c.
func zrbhdr32(r FrameReader, unescaper *zdlreadUnescaper) (int, Header, error) {
	var hdr Header
	
	// Read frame type
	c, err := unescaper.ReadByte()
	if err != nil {
		return 0, hdr, err
	}
	if c < 0 {
		return 0, hdr, NewError(ErrInvalidFrame, "invalid frame type")
	}
	
	frameType := int(c)
	crc := uint32(0xFFFFFFFF)
	crc = updcrc32(byte(frameType), crc)
	
	// Read header bytes
	for i := 0; i < 4; i++ {
		c, err := unescaper.ReadByte()
		if err != nil {
			return 0, hdr, err
		}
		if c < 0 {
			return 0, hdr, NewError(ErrInvalidFrame, "invalid header byte")
		}
		hdr[i] = byte(c)
		crc = updcrc32(hdr[i], crc)
	}
	
	// Read CRC bytes (4 bytes, little-endian)
	for i := 0; i < 4; i++ {
		c, err := unescaper.ReadByte()
		if err != nil {
			return 0, hdr, err
		}
		if c < 0 {
			return 0, hdr, NewError(ErrInvalidFrame, "invalid CRC byte")
		}
		crc = updcrc32(byte(c), crc)
	}
	
	// Verify CRC (should be 0xDEBB20E3 after finalization)
	if crc != CRC32CheckValue {
		return 0, hdr, NewError(ErrCRC, "bad CRC")
	}
	
	return frameType, hdr, nil
}

// zrhhdr receives a hex header.
// This matches the C function zrhhdr() from zm.c.
func zrhhdr(r io.Reader) (int, Header, error) {
	var hdr Header
	
	// Read frame type (hex)
	frameTypeByte, err := zgethex(r)
	if err != nil {
		return 0, hdr, err
	}
	
	frameType := int(frameTypeByte)
	crc := updcrc16(frameTypeByte, 0)
	
	// Read header bytes (hex)
	for i := 0; i < 4; i++ {
		b, err := zgethex(r)
		if err != nil {
			return 0, hdr, err
		}
		hdr[i] = b
		crc = updcrc16(b, crc)
	}
	
	// Read CRC (hex)
	crcHigh, err := zgethex(r)
	if err != nil {
		return 0, hdr, err
	}
	crc = updcrc16(crcHigh, crc)
	
	crcLow, err := zgethex(r)
	if err != nil {
		return 0, hdr, err
	}
	crc = updcrc16(crcLow, crc)
	
	// Verify CRC
	if crc != 0 {
		return 0, hdr, NewError(ErrCRC, "bad CRC")
	}
	
	// Read and discard CR/LF
	var buf [2]byte
	n, _ := r.Read(buf[:])
	if n >= 1 && (buf[0] == 0x0D || buf[0] == 0x15) {
		// Read second byte if available
		if n < 2 {
			r.Read(buf[1:])
		}
	}
	
	return frameType, hdr, nil
}

// zsdata sends a data frame with 16-bit CRC.
// This matches the C function zsdata() from zm.c.
//
// Parameters:
//   - w: the writer (with ZDLE escaping)
//   - buf: the data to send
//   - frameend: the frame end sequence (ZCRCE, ZCRCG, ZCRCQ, or ZCRCW)
//   - use32bitCRC: if true, use 32-bit CRC, otherwise 16-bit
func zsdata(w FrameWriter, buf []byte, frameend int, use32bitCRC bool) error {
	if use32bitCRC {
		return zsda32(w, buf, frameend)
	}
	
	// 16-bit CRC
	crc := uint16(0)
	escaper := newZsendlineEscaper(w, false, false)
	
	// Send data bytes with escaping
	for _, b := range buf {
		if err := escaper.WriteByte(b); err != nil {
			return err
		}
		crc = updcrc16(b, crc)
	}
	
	// Send ZDLE and frame end (both raw - match xsendline in C)
	if _, err := w.Write([]byte{ZDLE, byte(frameend)}); err != nil {
		return err
	}
	crc = updcrc16(byte(frameend), crc)
	
	// Finalize CRC
	crc = CRC16Finalize(crc)
	
	// Send CRC bytes with escaping
	if err := escaper.WriteByte(byte(crc >> 8)); err != nil {
		return err
	}
	if err := escaper.WriteByte(byte(crc)); err != nil {
		return err
	}
	
	// Send XON for ZCRCW frames (raw - match xsendline in C)
	if frameend == ZCRCW {
		if _, err := w.Write([]byte{XON}); err != nil {
			return err
		}
		return w.Flush()
	}
	
	return nil
}

// zsda32 sends a data frame with 32-bit CRC.
// This matches the C function zsda32() from zm.c.
func zsda32(w FrameWriter, buf []byte, frameend int) error {
	// 32-bit CRC
	crc := uint32(0xFFFFFFFF)
	escaper := newZsendlineEscaper(w, false, false)
	
	// Send data bytes with escaping
	for _, b := range buf {
		if err := escaper.WriteByte(b); err != nil {
			return err
		}
		crc = updcrc32(b, crc)
	}
	
	// Send ZDLE and frame end (both raw - match xsendline in C)
	if _, err := w.Write([]byte{ZDLE, byte(frameend)}); err != nil {
		return err
	}
	crc = updcrc32(byte(frameend), crc)
	
	// Finalize CRC
	crc = CRC32Finalize(crc)
	
	// Send CRC bytes (little-endian) with escaping
	// Match C code: if (c & 0140) xsendline else zsendline
	// 0140 octal = 0x60 hex = bits 5 & 6
	for i := 0; i < 4; i++ {
		crcByte := byte(crc)
		// Check if byte needs escaping (match C code logic)
		if crcByte&0x60 != 0 {
			// Bits 5 or 6 set - send raw (xsendline in C)
			if _, err := w.Write([]byte{crcByte}); err != nil {
				return err
			}
		} else {
			// May need escaping (zsendline in C)
			if err := escaper.WriteByte(crcByte); err != nil {
				return err
			}
		}
		crc >>= 8
	}
	
	// Send XON for ZCRCW frames (raw - match xsendline in C)
	if frameend == ZCRCW {
		if _, err := w.Write([]byte{XON}); err != nil {
			return err
		}
		return w.Flush()
	}
	
	return nil
}

// zrdata receives a data frame.
// This matches the C function zrdata() from zm.c.
//
// Returns:
//   - bytesReceived: number of bytes received
//   - frameend: the frame end sequence (GOTCRCE, GOTCRCG, GOTCRCQ, or GOTCRCW)
//   - error: any error that occurred
func zrdata(r FrameReader, unescaper *zdlreadUnescaper, buf []byte, use32bitCRC bool) (int, int, error) {
	if use32bitCRC {
		return zrdat32(r, unescaper, buf)
	}
	
	// 16-bit CRC
	crc := uint16(0)
	pos := 0
	end := len(buf)
	
	for pos <= end {
		c, err := unescaper.ReadByte()
		if err != nil {
			return pos, 0, err
		}
		
		// Check for frame end sequences
		if c&GOTOR != 0 {
			// Frame end sequence detected
			frameend := c
			c &= 0xFF // Get the actual byte value
			
			// Update CRC with frame end byte
			crc = updcrc16(byte(c), crc)
			
			// Read CRC bytes
			crcHigh, err := unescaper.ReadByte()
			if err != nil {
				return pos, 0, err
			}
			if crcHigh < 0 {
				return pos, 0, NewError(ErrInvalidFrame, "invalid CRC byte")
			}
			crc = updcrc16(byte(crcHigh), crc)
			
			crcLow, err := unescaper.ReadByte()
			if err != nil {
				return pos, 0, err
			}
			if crcLow < 0 {
				return pos, 0, NewError(ErrInvalidFrame, "invalid CRC byte")
			}
			crc = updcrc16(byte(crcLow), crc)
			
			// Verify CRC
			if crc != 0 {
				return pos, 0, NewError(ErrCRC, "bad CRC")
			}
			
			return pos, frameend, nil
		}
		
		// Check for special sequences
		if c == GOTCAN {
			return pos, ZCAN, nil
		}
		
		if c < 0 {
			return pos, 0, NewError(ErrInvalidFrame, "bad data subpacket")
		}
		
		// Store data byte
		if pos < end {
			buf[pos] = byte(c)
			pos++
			crc = updcrc16(byte(c), crc)
		} else {
			return pos, 0, NewError(ErrInvalidFrame, "data subpacket too long")
		}
	}
	
	return pos, 0, NewError(ErrInvalidFrame, "data subpacket too long")
}

// zrdat32 receives a data frame with 32-bit CRC.
// This matches the C function zrdat32() from zm.c.
func zrdat32(r FrameReader, unescaper *zdlreadUnescaper, buf []byte) (int, int, error) {
	// 32-bit CRC
	crc := uint32(0xFFFFFFFF)
	pos := 0
	end := len(buf)
	
	for pos <= end {
		c, err := unescaper.ReadByte()
		if err != nil {
			return pos, 0, err
		}
		
		// Check for frame end sequences
		if c&GOTOR != 0 {
			// Frame end sequence detected
			frameend := c
			c &= 0xFF // Get the actual byte value
			
			// Update CRC with frame end byte
			crc = updcrc32(byte(c), crc)
			
			// Read CRC bytes (4 bytes, little-endian)
			for i := 0; i < 4; i++ {
				crcByte, err := unescaper.ReadByte()
				if err != nil {
					return pos, 0, err
				}
				if crcByte < 0 {
					return pos, 0, NewError(ErrInvalidFrame, "invalid CRC byte")
				}
				crc = updcrc32(byte(crcByte), crc)
			}
			
			// Verify CRC (should be 0xDEBB20E3)
			if crc != CRC32CheckValue {
				return pos, 0, NewError(ErrCRC, "bad CRC")
			}
			
			return pos, frameend, nil
		}
		
		// Check for special sequences
		if c == GOTCAN {
			return pos, ZCAN, nil
		}
		
		if c < 0 {
			return pos, 0, NewError(ErrInvalidFrame, "bad data subpacket")
		}
		
		// Store data byte
		if pos < end {
			buf[pos] = byte(c)
			pos++
			crc = updcrc32(byte(c), crc)
		} else {
			return pos, 0, NewError(ErrInvalidFrame, "data subpacket too long")
		}
	}
	
	return pos, 0, NewError(ErrInvalidFrame, "data subpacket too long")
}

