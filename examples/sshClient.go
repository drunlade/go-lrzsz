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
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

var (
	host     = flag.String("host", "", "SSH host (hostname:port)")
	user     = flag.String("user", "", "SSH username")
	password = flag.String("password", "", "SSH password (or use SSH_PASSWORD env var)")
	sendFile = flag.String("send", "", "File to send when remote requests it (via 'rz')")
	verbose  = flag.Bool("v", false, "Verbose mode")
	quiet    = flag.Bool("q", false, "Quiet mode")
	help     = flag.Bool("h", false, "Show help")
	logFile  = flag.String("log", "", "ZModem protocol log file (for debugging)")
)

func showUsage(exitCode int) {
	fmt.Fprintf(os.Stderr, `Usage: %s [options]

Options:
  -host string      SSH host (hostname:port)
  -user string      SSH username
  -password string  SSH password (or use SSH_PASSWORD env var)
  -send string      File to send when remote requests it (via 'rz')
  -log string       ZModem protocol log file for debugging (optional)
  -v                Verbose mode
  -q                Quiet mode
  -h                Show help

Example:
  %s -host example.com:22 -user myuser -password mypass
  
With logging:
  %s -host example.com:22 -user myuser -password mypass -log /tmp/zmodem.log
`, os.Args[0], os.Args[0], os.Args[0])
	os.Exit(exitCode)
}

func main() {
	flag.Parse()

	if *help {
		showUsage(0)
	}

	if *host == "" {
		fmt.Fprintf(os.Stderr, "Error: -host is required\n")
		showUsage(1)
	}

	if *user == "" {
		fmt.Fprintf(os.Stderr, "Error: -user is required\n")
		showUsage(1)
	}

	// Get password from flag or environment
	pass := *password
	if pass == "" {
		pass = os.Getenv("SSH_PASSWORD")
	}
	if pass == "" {
		fmt.Fprintf(os.Stderr, "Error: -password or SSH_PASSWORD environment variable is required\n")
		showUsage(1)
	}

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigChan
		cancel()
	}()

	// Create SSH client config
	config := &ssh.ClientConfig{
		User: *user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // For example only - use proper verification in production
		Timeout:         10 * time.Second,
	}

	// Connect to SSH server
	if !*quiet {
		fmt.Fprintf(os.Stderr, "Connecting to %s...\n", *host)
	}
	client, err := ssh.Dial("tcp", *host, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	if !*quiet {
		fmt.Fprintf(os.Stderr, "Connected successfully\n")
	}

	// Create SSH session
	session, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	// Set up terminal modes
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,     // enable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set raw terminal mode: %v\n", err)
		os.Exit(1)
	}
	defer term.Restore(fd, oldState)

	width, height, err := term.GetSize(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get terminal size: %v\n", err)
		os.Exit(1)
	}

	if err := session.RequestPty("xterm", height, width, modes); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to request PTY: %v\n", err)
		os.Exit(1)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get stdin pipe: %v\n", err)
		os.Exit(1)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get stdout pipe: %v\n", err)
		os.Exit(1)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get stderr pipe: %v\n", err)
		os.Exit(1)
	}

	// Handle window resizing
	winCh := make(chan os.Signal, 1)
	signal.Notify(winCh, syscall.SIGWINCH)
	go func() {
		for range winCh {
			width, height, err := term.GetSize(fd)
			if err != nil {
				continue
			}
			session.WindowChange(height, width)
		}
	}()

	if err := session.Shell(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start shell: %v\n", err)
		os.Exit(1)
	}

	// Create ZModem callbacks
	callbacks := &zmodem.Callbacks{
		OnFilePrompt: func(filename string, size int64, mode os.FileMode) (bool, error) {
			if *quiet {
				return true, nil
			}
			// Auto-accept for this example
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
			if !*quiet {
				if *verbose {
					fmt.Fprintf(os.Stderr, "\nStarting: %s (%d bytes)\n", filename, size)
				} else {
					fmt.Fprintf(os.Stderr, "Transferring: %s\n", filename)
				}
			}
		},
		OnFileComplete: func(filename string, bytesTransferred int64, duration time.Duration) {
			if !*quiet {
				if *verbose {
					fmt.Fprintf(os.Stderr, "\nCompleted: %s (%d bytes in %v)\n",
						filename, bytesTransferred, duration)
				} else {
					fmt.Fprintf(os.Stderr, "Completed: %s\n", filename)
				}
			}
		},
		OnError: func(err error, context string) bool {
			if !*quiet {
				fmt.Fprintf(os.Stderr, "Error in %s: %v\n", context, err)
			}
			return false
		},
		OnFileCreate: func(filename string, size int64, mode os.FileMode) (io.Writer, error) {
			localFilename := filepath.Base(filename)
			if *verbose {
				fmt.Fprintf(os.Stderr, "Creating local file: %s\n", localFilename)
			}
			file, err := os.Create(localFilename)
			if err != nil {
				return nil, err
			}
			if err := file.Chmod(mode); err != nil {
				file.Close()
				return nil, err
			}
			return file, nil
		},
	}

	// If we have a file to send, set up the callbacks
	if *sendFile != "" {
		// OnFileList is called when remote runs 'rz' to ask what files to send
		callbacks.OnFileList = func() ([]string, error) {
			if *verbose {
				fmt.Fprintf(os.Stderr, "Remote requested files, sending: %s\n", *sendFile)
			}
			return []string{*sendFile}, nil
		}

		// OnFileOpen is called to open each file for sending
		callbacks.OnFileOpen = func(filename string) (io.Reader, os.FileInfo, error) {
			if *verbose {
				fmt.Fprintf(os.Stderr, "Opening file for sending: %s\n", filename)
			}
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
		}
	}

	// Create TerminalIO middleware - this automatically handles ZModem detection
	zConfig := zmodem.DefaultConfig()
	zConfig.Timeout = 30 // 3 seconds

	var termIO *zmodem.TerminalIO
	if *logFile != "" {
		logger, err := zmodem.NewFileLogger(*logFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create log file: %v\n", err)
			os.Exit(1)
		}
		defer logger.Close()
		logger.Info("SSH client started, connecting to %s@%s", *user, *host)
		termIO = zmodem.NewTerminalIOWithLogger(stdout, stdin, logger,
			zmodem.WithCallbacks(callbacks),
			zmodem.WithContext(ctx),
			zmodem.WithConfig(zConfig),
		)
	} else {
		termIO = zmodem.NewTerminalIO(stdout, stdin,
			zmodem.WithCallbacks(callbacks),
			zmodem.WithContext(ctx),
			zmodem.WithConfig(zConfig),
		)
	}

	// Copy terminal output to stdout
	go func() {
		io.Copy(os.Stdout, termIO.TerminalReader())
	}()

	// Copy terminal input from stdin
	go func() {
		io.Copy(termIO.TerminalWriter(), os.Stdin)
	}()

	// Copy stderr
	go func() {
		io.Copy(os.Stderr, stderr)
	}()

	// Wait for session to end
	if err := session.Wait(); err != nil {
		if !*quiet {
			fmt.Fprintf(os.Stderr, "Session ended: %v\n", err)
		}
	}
}
