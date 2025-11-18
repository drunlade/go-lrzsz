# go-lrzsz

A Go implementation of the ZModem file transfer protocol, converted from the C lrzsz codebase with exact protocol compatibility.

## Status

✅ **Core Implementation Complete** - All protocol and library components are implemented.

### Completed
- ✅ Protocol constants and frame types
- ✅ CRC calculation (16-bit and 32-bit) with exact table matching
- ✅ ZDLE escaping/unescaping
- ✅ I/O layer with timeout support
- ✅ Frame encoding/decoding (binary and hex headers)
- ✅ Data frame encoding/decoding
- ✅ Sender implementation (state machine)
- ✅ Receiver implementation (state machine)
- ✅ Library API and session management
- ✅ SSH integration wrapper
- ✅ Callback system
- ✅ Progress tracking

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
├── sender.go    # Sender implementation
├── receiver.go  # Receiver implementation
├── session.go   # Session management and high-level API
├── ssh.go       # SSH integration wrapper
├── callbacks.go # Callback system
├── progress.go  # Progress tracking
└── errors.go    # Error types
```

## Example Usage

```go
package main

import (
    "context"
    "fmt"
    "os"
    
    "golang.org/x/crypto/ssh"
    "github.com/drunlade/go-lrzsz/zmodem"
)

func main() {
    // Setup SSH connection
    config := &ssh.ClientConfig{
        User: "user",
        Auth: []ssh.AuthMethod{
            ssh.Password("password"),
        },
        HostKeyCallback: ssh.InsecureIgnoreHostKey(),
    }
    
    conn, err := ssh.Dial("tcp", "host:22", config)
    if err != nil {
        panic(err)
    }
    defer conn.Close()
    
    session, err := conn.NewSession()
    if err != nil {
        panic(err)
    }
    defer session.Close()
    
    // Create ZModem session with callbacks
    zmodemSession, err := zmodem.NewSSHSession(session,
        zmodem.WithCallbacks(&zmodem.Callbacks{
            OnFilePrompt: func(filename string, size int64, mode os.FileMode) (bool, error) {
                fmt.Printf("Receive file: %s (%d bytes)? [y/n]: ", filename, size)
                var response string
                fmt.Scanln(&response)
                return response == "y" || response == "Y", nil
            },
            OnProgress: func(filename string, transferred, total int64, rate float64) {
                percent := float64(0)
                if total > 0 {
                    percent = float64(transferred) / float64(total) * 100
                }
                fmt.Printf("\r%s: %.1f%% (%.0f bytes/s)", filename, percent, rate)
            },
        }),
    )
    if err != nil {
        panic(err)
    }
    defer zmodemSession.Close()
    
    // Receive files
    ctx := context.Background()
    if err := zmodemSession.ReceiveFiles(ctx, 0); err != nil {
        panic(err)
    }
}
```

## References

- Original C implementation: lrzsz-0.12.20
- Protocol specification: ZModem protocol by Chuck Forsberg

## License

[To be determined - will match original lrzsz license]

