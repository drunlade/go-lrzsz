package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/drunlade/go-lrzsz/zmodem"
)

var (
	verbose   = flag.Bool("v", false, "verbose mode")
	quiet     = flag.Bool("q", false, "quiet mode")
	binary    = flag.Bool("b", false, "binary transfer")
	ascii     = flag.Bool("a", false, "ASCII transfer")
	escape    = flag.Bool("e", false, "escape control characters")
	timeout   = flag.Int("t", 100, "timeout in tenths of seconds")
	help      = flag.Bool("h", false, "show help")
	version   = flag.Bool("version", false, "show version")
)

const versionString = "gsz version 0.1.0"

func main() {
	flag.Parse()

	if *help {
		showUsage(0)
	}

	if *version {
		fmt.Println(versionString)
		os.Exit(0)
	}

	// Get files from command line
	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "%s: no files specified\n", os.Args[0])
		showUsage(1)
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := signalContext(sigChan)
	defer cancel()

	// Create sender configuration
	config := &zmodem.SenderConfig{
		Use32BitCRC:   true,
		EscapeControl: *escape,
		TurboEscape:   false,
		Timeout:       *timeout,
		WindowSize:    0,
		BlockSize:     1024,
		MaxBlockSize:  8192,
		ZNulls:        0,
		Attention:     []byte{0x03, 0x8E, 0}, // ^C + pause
		Context:       ctx,
	}

	// Create callbacks
	callbacks := &zmodem.Callbacks{
		OnProgress: func(filename string, transferred, total int64, rate float64) {
			if *quiet {
				return
			}
			if *verbose {
				percent := float64(0)
				if total > 0 {
					percent = float64(transferred) / float64(total) * 100
				}
				fmt.Fprintf(os.Stderr, "\r%s: %.1f%% (%.0f bytes/s)", filename, percent, rate)
			}
		},
		OnFileStart: func(filename string, size int64, mode os.FileMode) {
			if *verbose && !*quiet {
				fmt.Fprintf(os.Stderr, "Sending: %s (%d bytes)\n", filename, size)
			}
		},
		OnFileComplete: func(filename string, bytesTransferred int64, duration time.Duration) {
			if !*quiet {
				if *verbose {
					fmt.Fprintf(os.Stderr, "\nCompleted: %s (%d bytes in %v)\n",
						filename, bytesTransferred, duration)
				} else {
					fmt.Fprintf(os.Stderr, "%s\n", filename)
				}
			}
		},
		OnError: func(err error, context string) bool {
			fmt.Fprintf(os.Stderr, "Error in %s: %v\n", context, err)
			return false
		},
		OnFileOpen: func(filename string) (io.Reader, os.FileInfo, error) {
			file, err := os.Open(filename)
			if err != nil {
				return nil, nil, err
			}
			info, err := file.Stat()
			if err != nil {
				file.Close()
				return nil, nil, err
			}
			return file, info, nil
		},
	}

	// Create stdout writer wrapper
	stdoutWriter := &stdoutWriterWrapper{writer: os.Stdout}
	
	// Create stdin reader with timeout
	stdinReader := &stdinReaderWrapper{reader: os.Stdin}
	
	// Create session
	session := zmodem.NewSession(stdinReader, stdoutWriter,
		zmodem.WithConfig(&zmodem.Config{
			Use32BitCRC:   config.Use32BitCRC,
			EscapeControl: config.EscapeControl,
			TurboEscape:   config.TurboEscape,
			Timeout:       config.Timeout,
			BlockSize:     config.BlockSize,
			MaxBlockSize:  config.MaxBlockSize,
			ZNulls:        config.ZNulls,
			Attention:     config.Attention,
		}),
		zmodem.WithCallbacks(callbacks),
		zmodem.WithContext(ctx),
	)

	// Build file list
	fileInfos := make([]zmodem.FileInfo, 0, len(files))
	for _, filename := range files {
		// Resolve absolute path
		absPath, err := filepath.Abs(filename)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path %s: %v\n", filename, err)
			continue
		}
		
		// Get file info
		info, err := os.Stat(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error accessing %s: %v\n", filename, err)
			continue
		}
		
		if info.IsDir() {
			fmt.Fprintf(os.Stderr, "Skipping directory: %s\n", filename)
			continue
		}
		
		fileInfos = append(fileInfos, zmodem.FileInfo{
			Filename: absPath,
			Info:     info,
		})
	}

	if len(fileInfos) == 0 {
		fmt.Fprintf(os.Stderr, "No valid files to send\n")
		os.Exit(1)
	}

	// Send files
	if err := session.SendFiles(ctx, fileInfos); err != nil {
		if !*quiet {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}

// stdinReaderWrapper wraps os.Stdin to implement ReaderWithTimeout
type stdinReaderWrapper struct {
	reader io.Reader
}

func (r *stdinReaderWrapper) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *stdinReaderWrapper) SetReadDeadline(t time.Time) error {
	// Stdin doesn't support deadlines, but that's OK
	// The zmodemIO layer will handle timeouts
	return nil
}

// stdoutWriterWrapper wraps os.Stdout
type stdoutWriterWrapper struct {
	writer io.Writer
}

func (w *stdoutWriterWrapper) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func signalContext(sigChan chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		cancel()
	}()
	return ctx, cancel
}

func showUsage(exitcode int) {
	fmt.Fprintf(os.Stderr, `%s - send files with ZMODEM protocol

Usage: %s [options] file...

Options:
  -a, --ascii      ASCII transfer (change CR/LF to LF)
  -b, --binary     binary transfer (default)
  -e, --escape     escape control characters
  -h, --help       show this help message
  -q, --quiet      quiet mode, minimal output
  -t N             timeout in tenths of seconds (default: 100)
  -v, --verbose    verbose mode
  --version        show version

Examples:
  %s file.txt              # Send a single file
  %s file1.txt file2.txt   # Send multiple files
  %s -v *.txt              # Send all .txt files in verbose mode

`, versionString, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
	os.Exit(exitcode)
}

