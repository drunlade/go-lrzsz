// Package zmodem implements the ZModem file transfer protocol.
//
// ZModem is a file transfer protocol designed for use over serial connections,
// commonly used over SSH sessions. This package provides a complete implementation
// that maintains exact compatibility with the original C lrzsz implementation.
//
// The package is designed as a library that can wrap SSH clients and provides
// callback hooks for file prompting, progress tracking, and feedback.
package zmodem

// Frame format indicators
const (
	// ZPAD is the padding character that begins frames
	ZPAD = '*'
	
	// ZDLE is the ZModem escape character (Ctrl-X)
	ZDLE = 0x18
	
	// ZDLEE is the escaped ZDLE as transmitted
	ZDLEE = ZDLE ^ 0x40
	
	// ZBIN indicates a binary frame with 16-bit CRC
	ZBIN = 'A'
	
	// ZHEX indicates a hex-encoded frame
	ZHEX = 'B'
	
	// ZBIN32 indicates a binary frame with 32-bit CRC
	ZBIN32 = 'C'
)

// Frame types (see frametypes array in zm.c)
const (
	ZRQINIT = iota // Request receive init
	ZRINIT         // Receive init
	ZSINIT         // Send init sequence (optional)
	ZACK           // ACK to above
	ZFILE          // File name from sender
	ZSKIP          // To sender: skip this file
	ZNAK           // Last packet was garbled
	ZABORT         // Abort batch transfers
	ZFIN           // Finish session
	ZRPOS          // Resume data trans at this position
	ZDATA          // Data packet(s) follow
	ZEOF           // End of file
	ZFERR          // Fatal Read or Write error Detected
	ZCRC           // Request for file CRC and response
	ZCHALLENGE     // Receiver's Challenge
	ZCOMPL         // Request is complete
	ZCAN           // Other end canned session with CAN*5
	ZFREECNT       // Request for free bytes on filesystem
	ZCOMMAND       // Command from sending program
	ZSTDERR        // Output to standard error, data follows
)

// ZDLE sequences
const (
	// ZCRCE - CRC next, frame ends, header packet follows
	ZCRCE = 'h'
	
	// ZCRCG - CRC next, frame continues nonstop
	ZCRCG = 'i'
	
	// ZCRCQ - CRC next, frame continues, ZACK expected
	ZCRCQ = 'j'
	
	// ZCRCW - CRC next, ZACK expected, end of frame
	ZCRCW = 'k'
	
	// ZRUB0 - Translate to rubout 0177
	ZRUB0 = 'l'
	
	// ZRUB1 - Translate to rubout 0377
	ZRUB1 = 'm'
)

// zdlread return values (internal)
// -1 is general error, -2 is timeout
const (
	GOTOR   = 0x400
	GOTCRCE = ZCRCE | GOTOR // ZDLE-ZCRCE received
	GOTCRCG = ZCRCG | GOTOR // ZDLE-ZCRCG received
	GOTCRCQ = ZCRCQ | GOTOR // ZDLE-ZCRCQ received
	GOTCRCW = ZCRCW | GOTOR // ZDLE-ZCRCW received
	GOTCAN  = GOTOR | 0x18  // CAN*5 seen
)

// Byte positions within header array
const (
	// ZF0-ZF3 are flag bytes (ZF0 is first flags byte)
	ZF0 = 3
	ZF1 = 2
	ZF2 = 1
	ZF3 = 0
	
	// ZP0-ZP3 are position bytes (ZP0 is low order, ZP3 is high order)
	ZP0 = 0 // Low order 8 bits of position
	ZP1 = 1
	ZP2 = 2
	ZP3 = 3 // High order 8 bits of file position
)

// Bit Masks for ZRINIT flags byte ZF0
const (
	CANFDX  = 0x01 // Rx can send and receive true FDX
	CANOVIO = 0x02 // Rx can receive data during disk I/O
	CANBRK  = 0x04 // Rx can send a break signal
	CANCRY  = 0x08 // Receiver can decrypt
	CANLZW  = 0x10 // Receiver can uncompress
	CANFC32 = 0x20 // Receiver can use 32 bit Frame Check
	ESCCTL  = 0x40 // Receiver expects ctl chars to be escaped
	ESC8    = 0x80 // Receiver expects 8th bit to be escaped
)

// Bit Masks for ZRINIT flags byte ZF1
const (
	ZF1_CANVHDR  = 0x01 // Variable headers OK, unused in lrzsz
	ZF1_TIMESYNC = 0x02 // nonstandard, Receiver request timesync
)

// Parameters for ZSINIT frame
const (
	// ZATTNLEN is the max length of attention string
	ZATTNLEN = 32
)

// Bit Masks for ZSINIT flags byte ZF0
const (
	TESCCTL = 0x40 // Transmitter expects ctl chars to be escaped
	TESC8   = 0x80 // Transmitter expects 8th bit to be escaped
)

// Parameters for ZFILE frame
// Conversion options one of these in ZF0
const (
	ZCBIN   = 1 // Binary transfer - inhibit conversion
	ZCNL    = 2 // Convert NL to local end of line convention
	ZCRESUM = 3 // Resume interrupted file transfer
)

// Management include options, one of these ored in ZF1
const (
	ZF1_ZMSKNOLOC = 0x80 // Skip file if not present at rx
)

// Management options, one of these ored in ZF1
const (
	ZF1_ZMMASK = 0x1f // Mask for the choices below
	ZF1_ZMNEWL = 1    // Transfer if source newer or longer
	ZF1_ZMCRC  = 2    // Transfer if different file CRC or length
	ZF1_ZMAPND = 3    // Append contents to existing file (if any)
	ZF1_ZMCLOB = 4    // Replace existing file
	ZF1_ZMNEW  = 5    // Transfer if source newer
	ZF1_ZMDIFF = 6    // Transfer if dates or lengths different
	ZF1_ZMPROT = 7    // Protect destination file
	ZF1_ZMCHNG = 8    // Change filename if destination exists
)

// Transport options, one of these in ZF2
const (
	ZTLZW   = 1 // Lempel-Ziv compression
	ZTCRYPT = 2 // Encryption
	ZTRLE   = 3 // Run Length encoding
)

// Extended options for ZF3, bit encoded
const (
	ZXSPARS = 64 // Encoding for sparse file operations
)

// Parameters for ZCOMMAND frame ZF0 (otherwise 0)
const (
	ZCACK1 = 1 // Acknowledge, then do command
)

// Ward Christensen / CP/M parameters - Don't change these!
const (
	ENQ     = 0x05
	CAN     = 'X' & 0x1F
	XOFF    = 's' & 0x1F
	XON     = 'q' & 0x1F
	SOH     = 0x01
	STX     = 0x02
	EOT     = 0x04
	ACK     = 0x06
	NAK     = 0x15
	CPMEOF  = 0x1A
	WANTCRC = 0x43 // send C not NAK to get crc not checksum
	WANTG   = 0x47 // Send G not NAK to get nonstop batch xmsn
	TIMEOUT = -2   // Timeout error code
	RCDO    = -3   // Carrier lost
)

// frametypes provides human-readable names for frame types
// Used for debugging and logging
var frametypes = []string{
	"Carrier Lost", // -3
	"TIMEOUT",      // -2
	"ERROR",        // -1
	"ZRQINIT",
	"ZRINIT",
	"ZSINIT",
	"ZACK",
	"ZFILE",
	"ZSKIP",
	"ZNAK",
	"ZABORT",
	"ZFIN",
	"ZRPOS",
	"ZDATA",
	"ZEOF",
	"ZFERR",
	"ZCRC",
	"ZCHALLENGE",
	"ZCOMPL",
	"ZCAN",
	"ZFREECNT",
	"ZCOMMAND",
	"ZSTDERR",
}

// FrameTypeName returns the human-readable name for a frame type.
// Returns "UNKNOWN" for invalid frame types.
func FrameTypeName(frameType int) string {
	offset := 3 // Offset for negative entries
	if frameType < -3 || frameType >= len(frametypes)-offset {
		return "UNKNOWN"
	}
	return frametypes[frameType+offset]
}

