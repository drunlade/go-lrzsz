# go-lrzsz

A Go implementation of the ZModem file transfer protocol, converted from the C lrzsz codebase with exact protocol compatibility.

## Status

🚧 **Work in Progress** - Core protocol infrastructure is complete.

### Completed
- ✅ Protocol constants and frame types
- ✅ CRC calculation (16-bit and 32-bit) with exact table matching
- ✅ ZDLE escaping/unescaping
- ✅ I/O layer with timeout support
- ✅ Frame encoding/decoding (binary and hex headers)
- ✅ Data frame encoding/decoding

### In Progress
- ⏳ Sender implementation (state machine)
- ⏳ Receiver implementation (state machine)
- ⏳ Library API and session management
- ⏳ SSH integration
- ⏳ Callback system

## Overview

This library provides a complete ZModem implementation that:
- Maintains exact protocol compatibility with the original C lrzsz
- Preserves byte order (little-endian) and all protocol details
- Can wrap SSH clients for file transfer over SSH sessions
- Provides callback hooks for file prompting, progress tracking, and feedback

## Protocol Fidelity

The implementation is designed to match the C implementation exactly:
- **Byte Order**: All multi-byte values are little-endian
- **CRC Calculations**: Exact table values and algorithms
- **Frame Formats**: Binary and hex frames match byte-for-byte
- **ZDLE Escaping**: Exact escape sequence handling
- **State Machines**: All protocol state transitions preserved

## Package Structure

```
zmodem/
├── zmodem.go    # Protocol constants and frame types
├── crc.go       # CRC tables and calculation functions
├── frame.go     # Frame encoding/decoding (headers and data)
├── escape.go    # ZDLE escaping/unescaping
├── io.go        # I/O layer with timeout support
└── errors.go    # Error types
```

## Example Usage (Planned)

```go
import (
    "golang.org/x/crypto/ssh"
    "github.com/drunlade/go-lrzsz/zmodem"
)

// Create SSH session
session, _ := conn.NewSession()

// Create ZModem session with callbacks
zmodemSession, _ := zmodem.NewSSHSession(session,
    zmodem.WithCallbacks(&zmodem.Callbacks{
        OnFilePrompt: func(filename string, size int64, mode os.FileMode) (bool, error) {
            // Prompt user to accept/reject file
            return true, nil
        },
        OnProgress: func(filename string, transferred, total int64, rate float64) {
            // Update progress display
        },
    }),
)

// Receive files
ctx := context.Background()
zmodemSession.ReceiveFiles(ctx, nil)
```

## References

- Original C implementation: lrzsz-0.12.20
- Protocol specification: ZModem protocol by Chuck Forsberg

## License

[To be determined - will match original lrzsz license]

