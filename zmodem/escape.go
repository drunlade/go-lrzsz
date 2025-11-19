package zmodem

import (
	"io"
)

// escapeType indicates how a byte should be escaped when sending
type escapeType int

const (
	escapeNone escapeType = iota // No escaping needed
	escapeAlways                 // Always escape (XOR with 0x40)
	escapeConditional            // Escape if previous byte was '@'
)

// zsendlineTab is the escape table for determining which bytes need escaping.
// Initialized by initZsendlineTab() to match zsendline_init() from zm.c.
var zsendlineTab [256]escapeType

// initZsendlineTab initializes the escape table.
// This matches the C function zsendline_init() from zm.c exactly.
//
// Parameters:
//   - zctlesc: if true, escape all control characters
//   - turboEscape: if true, use turbo escape mode (fewer escapes)
func initZsendlineTab(zctlesc bool, turboEscape bool) {
	for i := 0; i < 256; i++ {
		// Match C code: if (i & 0140) - 0140 octal = 0x60 = bits 5 & 6
		if i&0x60 != 0 {
			// Bits 5 or 6 set - no escaping needed
			zsendlineTab[i] = escapeNone
		} else {
			// Neither bit 5 nor 6 set - check if escaping needed
			switch i {
			case ZDLE:
				zsendlineTab[i] = escapeAlways
			case XOFF: // ^Q
				zsendlineTab[i] = escapeAlways
			case XON: // ^S
				zsendlineTab[i] = escapeAlways
			case XOFF | 0x80:
				zsendlineTab[i] = escapeAlways
			case XON | 0x80:
				zsendlineTab[i] = escapeAlways
			case 0x20, 0xA0: // ^P
				if turboEscape {
					zsendlineTab[i] = escapeNone
				} else {
					zsendlineTab[i] = escapeAlways
				}
			case 0x0D, 0x8D: // CR
				if zctlesc {
					zsendlineTab[i] = escapeAlways
				} else if !turboEscape {
					zsendlineTab[i] = escapeConditional
				} else {
					zsendlineTab[i] = escapeNone
				}
			default:
				if zctlesc {
					zsendlineTab[i] = escapeAlways
				} else {
					zsendlineTab[i] = escapeNone
				}
			}
		}
	}
}

// zsendlineEscaper handles ZDLE escaping when sending data.
// It tracks the last sent byte for conditional escaping.
type zsendlineEscaper struct {
	writer      io.Writer
	lastSent    byte
	escapeTable [256]escapeType
}

// newZsendlineEscaper creates a new ZDLE escaper.
func newZsendlineEscaper(writer io.Writer, zctlesc bool, turboEscape bool) *zsendlineEscaper {
	tab := [256]escapeType{}
	for i := 0; i < 256; i++ {
		// Match C code: if (i & 0140) - 0140 octal = 0x60 = bits 5 & 6
		if i&0x60 != 0 {
			tab[i] = escapeNone
		} else {
			switch i {
			case ZDLE, XOFF, XON, XOFF | 0x80, XON | 0x80:
				tab[i] = escapeAlways
			case 0x20, 0xA0:
				if turboEscape {
					tab[i] = escapeNone
				} else {
					tab[i] = escapeAlways
				}
			case 0x0D, 0x8D:
				if zctlesc {
					tab[i] = escapeAlways
				} else if !turboEscape {
					tab[i] = escapeConditional
				} else {
					tab[i] = escapeNone
				}
			default:
				if zctlesc {
					tab[i] = escapeAlways
				} else {
					tab[i] = escapeNone
				}
			}
		}
	}
	
	return &zsendlineEscaper{
		writer:      writer,
		escapeTable: tab,
	}
}

// WriteByte writes a single byte with ZDLE escaping if needed.
// This matches the C function zsendline() from zm.c.
func (z *zsendlineEscaper) WriteByte(c byte) error {
	c &= 0xFF // Ensure single byte
	
	switch z.escapeTable[c] {
	case escapeNone:
		_, err := z.writer.Write([]byte{c})
		if err == nil {
			z.lastSent = c
		}
		return err
		
	case escapeAlways:
		// Send ZDLE, then escaped byte
		if _, err := z.writer.Write([]byte{ZDLE}); err != nil {
			return err
		}
		escaped := c ^ 0x40
		_, err := z.writer.Write([]byte{escaped})
		if err == nil {
			z.lastSent = escaped
		}
		return err
		
	case escapeConditional:
		// Escape if last sent byte was '@'
		if (z.lastSent & 0x7F) == '@' {
			// Send ZDLE, then escaped byte
			if _, err := z.writer.Write([]byte{ZDLE}); err != nil {
				return err
			}
			escaped := c ^ 0x40
			_, err := z.writer.Write([]byte{escaped})
			if err == nil {
				z.lastSent = escaped
			}
			return err
		}
		// No escaping needed
		_, err := z.writer.Write([]byte{c})
		if err == nil {
			z.lastSent = c
		}
		return err
		
	default:
		_, err := z.writer.Write([]byte{c})
		if err == nil {
			z.lastSent = c
		}
		return err
	}
}

// Write writes bytes with ZDLE escaping.
// This is optimized to write non-escaped bytes in batches.
func (z *zsendlineEscaper) Write(buf []byte) (int, error) {
	written := 0
	for _, b := range buf {
		if err := z.WriteByte(b); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// zdlreadUnescaper handles ZDLE unescaping when receiving data.
type zdlreadUnescaper struct {
	reader io.Reader
}

// newZdlreadUnescaper creates a new ZDLE unescaper.
func newZdlreadUnescaper(reader io.Reader) *zdlreadUnescaper {
	return &zdlreadUnescaper{
		reader: reader,
	}
}

// ReadByte reads a single byte with ZDLE unescaping.
// Returns the unescaped byte, or a special value if an escape sequence is detected.
//
// Special return values:
//   - GOTCRCE, GOTCRCG, GOTCRCQ, GOTCRCW: Frame end sequences
//   - GOTCAN: CAN*5 sequence detected (cancellation)
//   - Error: I/O error or timeout
//
// This matches the C function zdlread() and zdlread2() from zm.c.
func (z *zdlreadUnescaper) ReadByte() (int, error) {
	var buf [1]byte
	n, err := z.reader.Read(buf[:])
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, io.ErrUnexpectedEOF
	}
	
	c := buf[0]
	
	// Quick check for non-control characters
	// Match C code: if (i & 0140) - 0140 octal = 0x60 = bits 5 & 6
	// If either bit 5 or 6 is set, it's a printable char that doesn't need special handling
	if c&0x60 != 0 {
		return int(c), nil
	}
	
	return z.readByte2(c)
}

// readByte2 handles the second byte of a potential escape sequence.
// This matches the C function zdlread2() from zm.c.
func (z *zdlreadUnescaper) readByte2(c byte) (int, error) {
	switch c {
	case ZDLE:
		// Potential escape sequence - read next byte
		return z.readEscapeSequence()
		
	case XON, XON | 0x80, XOFF, XOFF | 0x80:
		// Flow control - skip and read next
		return z.ReadByte()
		
	default:
		// Regular byte - return as-is
		return int(c), nil
	}
}

// readEscapeSequence reads and processes a ZDLE escape sequence.
func (z *zdlreadUnescaper) readEscapeSequence() (int, error) {
	var buf [1]byte
	
	// Read the escape sequence byte
	n, err := z.reader.Read(buf[:])
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, io.ErrUnexpectedEOF
	}
	
	c := buf[0]
	
	// Check for CAN*5 sequence (cancellation)
	if c == CAN {
		// Read next 4 CAN bytes
		for i := 0; i < 4; i++ {
			n, err := z.reader.Read(buf[:])
			if err != nil {
				return 0, err
			}
			if n != 1 || buf[0] != CAN {
				// Not a full CAN*5 sequence
				return int(c), nil
			}
		}
		return GOTCAN, nil
	}
	
	// Check for frame end sequences
	switch c {
	case ZCRCE:
		return GOTCRCE, nil
	case ZCRCG:
		return GOTCRCG, nil
	case ZCRCQ:
		return GOTCRCQ, nil
	case ZCRCW:
		return GOTCRCW, nil
	case ZRUB0:
		return 0x7F, nil // Rubout 0177
	case ZRUB1:
		return 0xFF, nil // Rubout 0377
	case XON, XON | 0x80, XOFF, XOFF | 0x80:
		// Flow control in escape sequence - skip and continue
		return z.readEscapeSequence()
	default:
		// Escaped byte - unescape by XOR with 0x40
		if (c & 0x80) == 0x40 {
			return int(c ^ 0x40), nil
		}
		// Invalid escape sequence
		return 0, NewError(ErrInvalidFrame, "bad escape sequence")
	}
}

