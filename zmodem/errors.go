package zmodem

import "fmt"

// Error represents a ZModem protocol error
type Error struct {
	// Type is the error type
	Type ErrorType
	
	// Message is a human-readable error message
	Message string
	
	// FrameType is the frame type that caused the error (if applicable)
	FrameType int
}

// ErrorType categorizes ZModem errors
type ErrorType int

const (
	// ErrProtocol indicates a protocol violation
	ErrProtocol ErrorType = iota
	
	// ErrCRC indicates a CRC mismatch
	ErrCRC
	
	// ErrTimeout indicates a timeout occurred
	ErrTimeout
	
	// ErrIO indicates an I/O error
	ErrIO
	
	// ErrCancelled indicates the transfer was cancelled
	ErrCancelled
	
	// ErrInvalidFrame indicates an invalid frame was received
	ErrInvalidFrame
	
	// ErrFileSkipped indicates a file transfer was skipped
	ErrFileSkipped
	
	// ErrRemoteCommandDenied indicates a remote command was denied
	ErrRemoteCommandDenied
)

func (e *Error) Error() string {
	if e.FrameType >= 0 {
		return fmt.Sprintf("zmodem %s: %s (frame: %s)", e.Type, e.Message, FrameTypeName(e.FrameType))
	}
	return fmt.Sprintf("zmodem %s: %s", e.Type, e.Message)
}

func (t ErrorType) String() string {
	switch t {
	case ErrProtocol:
		return "protocol error"
	case ErrCRC:
		return "CRC error"
	case ErrTimeout:
		return "timeout"
	case ErrIO:
		return "I/O error"
	case ErrCancelled:
		return "cancelled"
	case ErrInvalidFrame:
		return "invalid frame"
	case ErrFileSkipped:
		return "file skipped"
	case ErrRemoteCommandDenied:
		return "remote command denied"
	default:
		return "unknown error"
	}
}

// NewError creates a new ZModem error
func NewError(errType ErrorType, message string) *Error {
	return &Error{
		Type:    errType,
		Message: message,
		FrameType: -1,
	}
}

// NewFrameError creates a new ZModem error with frame type information
func NewFrameError(errType ErrorType, message string, frameType int) *Error {
	return &Error{
		Type:      errType,
		Message:   message,
		FrameType: frameType,
	}
}

// IsTimeout checks if an error is a timeout error
func IsTimeout(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.Type == ErrTimeout
	}
	return false
}

// IsCRC checks if an error is a CRC error
func IsCRC(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.Type == ErrCRC
	}
	return false
}

// IsCancelled checks if an error indicates cancellation
func IsCancelled(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.Type == ErrCancelled
	}
	return false
}

