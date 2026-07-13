package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	watchInterval  = 200 * time.Millisecond
	settleDuration = 150 * time.Millisecond
)

var isNetworkWriting atomic.Int32

// NetworkWriteFile writes data to path atomically: it writes to a temp file
// in the same directory, then renames over the destination, so a crash or
// interrupt mid-write can never leave a truncated file at path.
func NetworkWriteFile(path string, data []byte, perm os.FileMode) error {
	isNetworkWriting.Store(1)
	defer isNetworkWriting.Store(0)

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempName := temp.Name()

	var writeErr error
	defer func() {
		if writeErr != nil {
			os.Remove(tempName)
		}
	}()

	if _, writeErr = temp.Write(data); writeErr != nil {
		temp.Close()
		return fmt.Errorf("failed to write file: %w", writeErr)
	}

	if writeErr = temp.Close(); writeErr != nil {
		return fmt.Errorf("failed to close temp file: %w", writeErr)
	}

	if writeErr = os.Chmod(tempName, perm); writeErr != nil {
		return fmt.Errorf("failed to set file permissions: %w", writeErr)
	}

	if writeErr = os.Rename(tempName, path); writeErr != nil {
		return fmt.Errorf("failed to move file to destination: %w", writeErr)
	}

	return nil
}

// FileWatcher polls a single file for local modifications and sends the
// file's path on the returned channel whenever a genuine local edit is
// detected. It exits cleanly when stop is closed.
//
// The caller owns the returned channel and should drain it promptly;
// the watcher drops events if the channel is full (non-blocking send)
// to avoid blocking the poll loop.
type FileWatcher struct {
	path     string
	onChange chan<- string // receives path on local edit
	stop     <-chan struct{}
	mu       sync.Mutex // guards lastMod
	lastMod  time.Time  // last observed modtime
}

// NewFileWatcher constructs a FileWatcher and immediately snapshots the
// file's current modtime so the first poll cycle has a baseline to compare.
func NewFileWatcher(path string, onChange chan<- string, stop <-chan struct{}) (*FileWatcher, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("newFileWatcher: initial stat of %q: %w", path, err)
	}

	return &FileWatcher{
		path:     path,
		onChange: onChange,
		stop:     stop,
		lastMod:  info.ModTime(),
	}, nil
}

// Run starts the poll loop. Call it in its own goroutine.
//
//	stop := make(chan struct{})
//	changes := make(chan string, 1)
//	watcher, _ := NewFileWatcher("transfer.zip", changes, stop)
//	go watcher.Run()
func (fw *FileWatcher) Run() {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	// pendingChange tracks whether we have seen a modtime change that has
	// not yet settled. Non-zero means we are in the settle window.
	var pendingChange time.Time

	for {
		select {
		case <-fw.stop:
			return

		case now := <-ticker.C:
			fw.poll(now, &pendingChange)
		}
	}
}

// poll is called on every ticker tick. It handles both detection
// (modtime changed since last known-good) and settlement (modtime
// has been stable for at least settleDuration).
func (fw *FileWatcher) poll(now time.Time, pendingChange *time.Time) {
	info, err := os.Stat(fw.path)
	if err != nil {
		// File temporarily missing (editor swap file, rename in progress).
		// Don't update lastMod; retry on the next tick.
		return
	}

	modTime := info.ModTime()

	fw.mu.Lock()
	lastMod := fw.lastMod
	fw.mu.Unlock()

	if modTime.Equal(lastMod) {
		// No change since last check — check if a pending event has settled.
		if !pendingChange.IsZero() && now.Sub(*pendingChange) >= settleDuration {
			fw.dispatchIfLocal()
			*pendingChange = time.Time{} // reset
		}
		return
	}

	// Modtime changed. Update our baseline immediately so a rapid sequence
	// of writes doesn't fire multiple events — only the last settled state
	// will be dispatched.
	fw.mu.Lock()
	fw.lastMod = modTime
	fw.mu.Unlock()

	// Start (or restart) the settle window.
	*pendingChange = now
}

// dispatchIfLocal checks the network-write lock and, if the flag is clear,
// sends the file path on the onChange channel. If the channel is full the
// event is dropped — the caller is responsible for draining promptly.
func (fw *FileWatcher) dispatchIfLocal() {
	if isNetworkWriting.Load() == 1 {
		// This modtime change was caused by NetworkWriteFile.
		// Suppress it to prevent the feedback loop.
		return
	}

	// Non-blocking send: if the consumer is slow we drop rather than block
	// the watcher goroutine.
	select {
	case fw.onChange <- fw.path:
	default:
		fmt.Fprintf(os.Stderr, "watcher: onChange channel full, dropping event for %q\n", fw.path)
	}
}
