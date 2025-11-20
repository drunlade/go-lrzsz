package zmodem

import (
	"io"
	"os"
	"time"
)

// Callbacks provides hooks for ZModem transfer events.
// All callbacks are optional - nil callbacks use default behavior.
type Callbacks struct {
	// OnFilePrompt is called when a file transfer is about to start.
	// Return true to accept the file, false to skip it.
	// If an error is returned, the transfer is aborted.
	OnFilePrompt func(filename string, size int64, mode os.FileMode) (bool, error)

	// OnProgress is called periodically during file transfer.
	// filename: name of the file being transferred
	// transferred: bytes transferred so far
	// total: total bytes to transfer (0 if unknown)
	// rate: transfer rate in bytes per second
	OnProgress func(filename string, transferred, total int64, rate float64)
	
	// OnFileStart is called when a file transfer starts.
	OnFileStart func(filename string, size int64, mode os.FileMode)

	// OnFileComplete is called when a file transfer completes.
	// duration: time taken for the transfer
	OnFileComplete func(filename string, bytesTransferred int64, duration time.Duration)

	// OnError is called when an error occurs.
	// context: description of where the error occurred
	// Return true to retry, false to abort.
	OnError func(err error, context string) bool
	
	// OnEvent is called for protocol events (debugging/logging).
	OnEvent func(event Event)

	// OnFileList is called when the remote initiates a receive (runs 'rz').
	// It should return a list of files to send, or nil/empty slice if no files to send.
	OnFileList func() ([]string, error)

	// OnFileOpen is called when opening a file for reading (sender).
	// If nil, uses default file opening.
	OnFileOpen func(filename string) (io.Reader, os.FileInfo, error)

	// OnFileCreate is called when creating a file for writing (receiver).
	// If nil, uses default file creation.
	OnFileCreate func(filename string, size int64, mode os.FileMode) (io.Writer, error)
}

// Event represents a protocol event for logging/debugging.
type Event struct {
	Type      EventType
	Message   string
	FrameType int
	Timestamp time.Time
}

// EventType categorizes protocol events.
type EventType int

const (
	EventFrameSent EventType = iota
	EventFrameReceived
	EventFileStart
	EventFileComplete
	EventError
	EventTimeout
	EventCancelled
)

// defaultCallbacks returns a set of callbacks with default implementations.
func defaultCallbacks() *Callbacks {
	return &Callbacks{
		OnFilePrompt: func(string, int64, os.FileMode) (bool, error) {
			return true, nil // Accept all files by default
		},
		OnProgress:     func(string, int64, int64, float64) {},
		OnFileStart:    func(string, int64, os.FileMode) {},
		OnFileComplete: func(string, int64, time.Duration) {},
		OnError: func(error, string) bool {
			return false // Don't retry by default
		},
		OnEvent: func(Event) {},
		OnFileOpen:   nil, // Use default
		OnFileCreate: nil, // Use default
	}
}

// mergeCallbacks merges user callbacks with defaults.
// User callbacks override defaults, nil callbacks use defaults.
func mergeCallbacks(user *Callbacks) *Callbacks {
	if user == nil {
		return defaultCallbacks()
	}

	def := defaultCallbacks()

	result := &Callbacks{}

	// File prompt
	if user.OnFilePrompt != nil {
		result.OnFilePrompt = user.OnFilePrompt
	} else {
		result.OnFilePrompt = def.OnFilePrompt
	}

	// Progress
	if user.OnProgress != nil {
		result.OnProgress = user.OnProgress
	} else {
		result.OnProgress = def.OnProgress
	}

	// File start
	if user.OnFileStart != nil {
		result.OnFileStart = user.OnFileStart
	} else {
		result.OnFileStart = def.OnFileStart
	}

	// File complete
	if user.OnFileComplete != nil {
		result.OnFileComplete = user.OnFileComplete
	} else {
		result.OnFileComplete = def.OnFileComplete
	}

	// Error
	if user.OnError != nil {
		result.OnError = user.OnError
	} else {
		result.OnError = def.OnError
	}

	// Event
	if user.OnEvent != nil {
		result.OnEvent = user.OnEvent
	} else {
		result.OnEvent = def.OnEvent
	}

	// File operations (nil means use default)
	result.OnFileList = user.OnFileList
	result.OnFileOpen = user.OnFileOpen
	result.OnFileCreate = user.OnFileCreate

	return result
}
