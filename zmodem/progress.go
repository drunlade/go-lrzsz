package zmodem

import (
	"sync"
	"time"
)

// ProgressTracker tracks transfer progress and invokes progress callbacks.
type ProgressTracker struct {
	mu sync.Mutex
	
	filename         string
	bytesTransferred int64
	bytesTotal      int64
	startTime       time.Time
	lastUpdate      time.Time
	lastBytes       int64
	
	callback        func(string, int64, int64, float64)
	updateInterval  time.Duration
}

// NewProgressTracker creates a new progress tracker.
func NewProgressTracker(callback func(string, int64, int64, float64), interval time.Duration) *ProgressTracker {
	if interval <= 0 {
		interval = 100 * time.Millisecond // Default: update every 100ms
	}
	
	return &ProgressTracker{
		callback:       callback,
		updateInterval: interval,
	}
}

// Start begins tracking a new file transfer.
func (pt *ProgressTracker) Start(filename string, bytesTotal int64) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	
	pt.filename = filename
	pt.bytesTotal = bytesTotal
	pt.bytesTransferred = 0
	pt.startTime = time.Now()
	pt.lastUpdate = pt.startTime
	pt.lastBytes = 0
}

// Update updates the progress and invokes the callback if enough time has passed.
func (pt *ProgressTracker) Update(bytesTransferred int64) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	
	pt.bytesTransferred = bytesTransferred
	
	now := time.Now()
	if now.Sub(pt.lastUpdate) < pt.updateInterval {
		return // Too soon for an update
	}
	
	// Calculate rate
	elapsed := now.Sub(pt.lastUpdate).Seconds()
	var rate float64
	if elapsed > 0 {
		rate = float64(bytesTransferred-pt.lastBytes) / elapsed
	}
	
	// Invoke callback
	if pt.callback != nil {
		pt.callback(pt.filename, bytesTransferred, pt.bytesTotal, rate)
	}
	
	pt.lastUpdate = now
	pt.lastBytes = bytesTransferred
}

// Complete marks the transfer as complete and returns the duration.
func (pt *ProgressTracker) Complete() time.Duration {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	
	duration := time.Since(pt.startTime)
	
	// Final update
	if pt.callback != nil {
		pt.callback(pt.filename, pt.bytesTransferred, pt.bytesTotal, 0)
	}
	
	return duration
}

// GetStats returns current progress statistics.
func (pt *ProgressTracker) GetStats() (filename string, transferred, total int64, rate float64, duration time.Duration) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	
	filename = pt.filename
	transferred = pt.bytesTransferred
	total = pt.bytesTotal
	duration = time.Since(pt.startTime)
	
	if duration.Seconds() > 0 {
		rate = float64(transferred) / duration.Seconds()
	}
	
	return
}

