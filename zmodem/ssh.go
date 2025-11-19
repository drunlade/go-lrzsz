package zmodem

import (
	"context"
	"io"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHSession wraps an SSH session for ZModem transfers.
// It manages stdin/stdout/stderr pipes and provides a high-level API.
type SSHSession struct {
	*Session
	sshSession *ssh.Session
	stdin      io.WriteCloser
	stdout     io.Reader
	stderr     io.Reader
}

// NewSSHSession creates a ZModem session from an SSH session.
func NewSSHSession(sshSession *ssh.Session, opts ...Option) (*SSHSession, error) {
	// Get pipes
	stdin, err := sshSession.StdinPipe()
	if err != nil {
		return nil, err
	}
	
	stdout, err := sshSession.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	
	stderr, err := sshSession.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	
	// Create a reader with timeout wrapper
	reader := &sshReader{
		reader:  stdout,
		timeout: 100, // 10 seconds default
	}
	
	// Create underlying ZModem session
	session := NewSession(reader, stdin, opts...)
	
	return &SSHSession{
		Session:    session,
		sshSession: sshSession,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
	}, nil
}

// sshReader wraps an io.Reader to implement ReaderWithTimeout.
type sshReader struct {
	reader  io.Reader
	timeout int
}

func (r *sshReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *sshReader) ReadByte() (byte, error) {
	var buf [1]byte
	n, err := r.reader.Read(buf[:])
	if n == 0 {
		return 0, err
	}
	return buf[0], err
}

func (r *sshReader) SetTimeout(timeout int) {
	r.timeout = timeout
}

func (r *sshReader) SetReadDeadline(t time.Time) error {
	// SSH readers don't support deadlines directly
	// The timeout is handled by the zmodemIO layer
	return nil
}

// SendFiles sends multiple files over SSH using ZModem.
// This starts the remote sz command and sends files.
func (s *SSHSession) SendFiles(ctx context.Context, files []FileInfo) error {
	// Start remote sz command
	if err := s.sshSession.Start("sz --zmodem"); err != nil {
		return err
	}
	
	// Wait for command to finish in background
	done := make(chan error, 1)
	go func() {
		done <- s.sshSession.Wait()
	}()
	
	// Send files
	err := s.Session.SendFiles(ctx, files)
	
	// Close stdin to signal completion
	s.stdin.Close()
	
	// Wait for command to finish
	select {
	case err2 := <-done:
		if err == nil {
			err = err2
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	
	return err
}

// ReceiveFiles receives multiple files over SSH using ZModem.
// This starts the remote rz command and receives files.
func (s *SSHSession) ReceiveFiles(ctx context.Context, maxFiles int) error {
	// Start remote rz command
	if err := s.sshSession.Start("rz --zmodem"); err != nil {
		return err
	}
	
	// Wait for command to finish in background
	done := make(chan error, 1)
	go func() {
		done <- s.sshSession.Wait()
	}()
	
	// Receive files
	err := s.Session.ReceiveFiles(ctx, maxFiles)
	
	// Close stdin to signal completion
	s.stdin.Close()
	
	// Wait for command to finish
	select {
	case err2 := <-done:
		if err == nil {
			err = err2
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	
	return err
}

// SendFile sends a single file over SSH using ZModem.
func (s *SSHSession) SendFile(ctx context.Context, filename string, file io.Reader, fileInfo os.FileInfo) error {
	// Start remote sz command
	if err := s.sshSession.Start("sz --zmodem"); err != nil {
		return err
	}
	
	// Wait for command to finish in background
	done := make(chan error, 1)
	go func() {
		done <- s.sshSession.Wait()
	}()
	
	// Send file
	err := s.Session.SendFile(ctx, filename, file, fileInfo)
	
	// Close stdin to signal completion
	s.stdin.Close()
	
	// Wait for command to finish
	select {
	case err2 := <-done:
		if err == nil {
			err = err2
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	
	return err
}

// ReceiveFile receives a single file over SSH using ZModem.
func (s *SSHSession) ReceiveFile(ctx context.Context) error {
	// Start remote rz command
	if err := s.sshSession.Start("rz --zmodem"); err != nil {
		return err
	}
	
	// Wait for command to finish in background
	done := make(chan error, 1)
	go func() {
		done <- s.sshSession.Wait()
	}()
	
	// Receive file
	err := s.Session.ReceiveFile(ctx)
	
	// Close stdin to signal completion
	s.stdin.Close()
	
	// Wait for command to finish
	select {
	case err2 := <-done:
		if err == nil {
			err = err2
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	
	return err
}

// Close closes the SSH session and cleans up resources.
func (s *SSHSession) Close() error {
	var errs []error
	
	if s.stdin != nil {
		if err := s.stdin.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	
	if s.sshSession != nil {
		if err := s.sshSession.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	
	if len(errs) > 0 {
		return errs[0] // Return first error
	}
	
	return nil
}

// Stderr returns the stderr reader for monitoring remote command output.
func (s *SSHSession) Stderr() io.Reader {
	return s.stderr
}

// SendFileWhenRemoteReceives sends a file when the remote runs 'rz' (receiver).
// This is the opposite of SendFiles - the remote is ready to receive, not send.
func (s *SSHSession) SendFileWhenRemoteReceives(ctx context.Context, file FileInfo) error {
	// Start remote rz command (they will receive)
	if err := s.sshSession.Start("rz --zmodem"); err != nil {
		return err
	}
	
	// Wait for command to finish in background
	done := make(chan error, 1)
	go func() {
		done <- s.sshSession.Wait()
	}()
	
	// Initialize sender - this will wait for ZRQINIT from remote and respond with ZRINIT
	if err := s.Session.sender.GetReceiverInit(); err != nil {
		s.stdin.Close()
		return err
	}
	
	// Open file
	var fileReader io.Reader
	var err error
	if s.Session.callbacks.OnFileOpen != nil {
		fileReader, _, err = s.Session.callbacks.OnFileOpen(file.Filename)
	} else {
		f, err2 := os.Open(file.Filename)
		if err2 != nil {
			s.stdin.Close()
			return err2
		}
		fileReader = f
		defer f.Close()
	}
	if err != nil {
		s.stdin.Close()
		return err
	}
	
	// Build file header
	fileHeader := BuildFileHeader(file.Filename, file.Info, 0, 0)
	
	// Send file using the sender directly
	if err := s.Session.sender.SendFile(file.Filename, fileReader, file.Info, fileHeader); err != nil {
		s.stdin.Close()
		return err
	}
	
	// Close stdin to signal completion
	s.stdin.Close()
	
	// Wait for remote command to finish
	select {
	case err2 := <-done:
		if err2 != nil {
			return err2
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	
	return nil
}

