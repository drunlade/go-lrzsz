package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drunlade/go-lrzsz/zmodem"
)

var (
	verbose   = flag.Bool("v", false, "verbose mode")
	quiet     = flag.Bool("q", false, "quiet mode")
	binary    = flag.Bool("b", false, "binary transfer")
	ascii     = flag.Bool("a", false, "ASCII transfer")
	overwrite = flag.Bool("y", false, "overwrite existing files")
	protect   = flag.Bool("p", false, "protect existing files")
	escape    = flag.Bool("e", false, "escape control characters")
	timeout   = flag.Int("t", 100, "timeout in tenths of seconds")
	help      = flag.Bool("h", false, "show help")
	version   = flag.Bool("version", false, "show version")
)

const versionString = "grz version 0.1.0"

func main() {
	flag.Parse()

	if *help {
		showUsage(0)
	}

	if *version {
		fmt.Println(versionString)
		os.Exit(0)
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := signalContext(sigChan)
	defer cancel()

	// Create receiver configuration
	config := &zmodem.ReceiverConfig{
		Use32BitCRC:   true,
		EscapeControl: *escape,
		TurboEscape:   false,
		Timeout:       *timeout,
		BufferSize:    8192,
		Attention:     []byte{0x03, 0x8E, 0}, // ^C + pause
		Context:       ctx,
	}

	// Create callbacks
	callbacks := &zmodem.Callbacks{
		OnFilePrompt: func(filename string, size int64, mode os.FileMode) (bool, error) {
			if *quiet {
				return true, nil
			}
			if *overwrite {
				return true, nil
			}
			if *protect {
				// Check if file exists
				if _, err := os.Stat(filename); err == nil {
					if *verbose {
						fmt.Fprintf(os.Stderr, "Skipping %s (protected)\n", filename)
					}
					return false, nil
				}
			}
			if *verbose {
				fmt.Fprintf(os.Stderr, "Receiving: %s (%d bytes)\n", filename, size)
			}
			return true, nil
		},
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
				fmt.Fprintf(os.Stderr, "Starting: %s\n", filename)
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
		OnFileCreate: func(filename string, size int64, mode os.FileMode) (io.Writer, error) {
			// Create file
			file, err := os.Create(filename)
			if err != nil {
				return nil, err
			}
			// Set permissions
			if err := file.Chmod(mode); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		},
	}

	// Create stdin reader with timeout
	stdinReader := &stdinReaderWrapper{reader: os.Stdin}
	
	// Create session
	session := zmodem.NewSession(stdinReader, os.Stdout,
		zmodem.WithConfig(&zmodem.Config{
			Use32BitCRC:   config.Use32BitCRC,
			EscapeControl: config.EscapeControl,
			TurboEscape:   config.TurboEscape,
			Timeout:       config.Timeout,
			MaxBlockSize:  config.BufferSize,
			Attention:     config.Attention,
		}),
		zmodem.WithCallbacks(callbacks),
		zmodem.WithContext(ctx),
	)

	// Receive files
	if err := session.ReceiveFiles(ctx, 0); err != nil {
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

func signalContext(sigChan chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		cancel()
	}()
	return ctx, cancel
}

func showUsage(exitcode int) {
	fmt.Fprintf(os.Stderr, `%s - receive files with ZMODEM protocol

Usage: %s [options]

Options:
  -a, --ascii      ASCII transfer (change CR/LF to LF)
  -b, --binary     binary transfer (default)
  -e, --escape     escape control characters
  -h, --help       show this help message
  -p, --protect    protect existing files
  -q, --quiet      quiet mode, minimal output
  -t N             timeout in tenths of seconds (default: 100)
  -v, --verbose    verbose mode
  -y, --overwrite  overwrite existing files
  --version        show version

Examples:
  %s                    # Receive files from stdin
  %s -v                 # Verbose mode
  %s -q                 # Quiet mode

`, versionString, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
	os.Exit(exitcode)
}

