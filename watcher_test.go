package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestWatcher(t *testing.T, path string) (*FileWatcher, chan string) {
	t.Helper()
	changes := make(chan string, 1)
	stop := make(chan struct{})
	fw, err := NewFileWatcher(path, changes, stop)
	if err != nil {
		t.Fatalf("NewFileWatcher: %v", err)
	}
	return fw, changes
}

func TestNewFileWatcherMissingFile(t *testing.T) {
	changes := make(chan string, 1)
	stop := make(chan struct{})
	if _, err := NewFileWatcher(filepath.Join(t.TempDir(), "does-not-exist"), changes, stop); err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestPollDispatchesAfterSettle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched.txt")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fw, changes := newTestWatcher(t, path)

	base := time.Now()
	var pending time.Time

	// No change yet: poll should be a no-op.
	fw.poll(base, &pending)
	select {
	case p := <-changes:
		t.Fatalf("unexpected dispatch before any edit: %q", p)
	default:
	}

	// Simulate a local edit by advancing the file's modtime.
	editTime := base.Add(1 * time.Second)
	if err := os.Chtimes(path, editTime, editTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// First poll after the edit: detected, but not yet settled.
	fw.poll(editTime, &pending)
	if pending.IsZero() {
		t.Fatal("expected pendingChange to be set after modtime change")
	}
	select {
	case p := <-changes:
		t.Fatalf("dispatched before settle window elapsed: %q", p)
	default:
	}

	// Still within the settle window: no dispatch.
	fw.poll(editTime.Add(settleDuration-10*time.Millisecond), &pending)
	select {
	case p := <-changes:
		t.Fatalf("dispatched before settle duration elapsed: %q", p)
	default:
	}

	// Settle window elapsed with no further changes: dispatch now.
	fw.poll(editTime.Add(settleDuration+10*time.Millisecond), &pending)
	select {
	case p := <-changes:
		if p != path {
			t.Errorf("dispatched path = %q, want %q", p, path)
		}
	default:
		t.Fatal("expected dispatch after settle window elapsed")
	}
	if !pending.IsZero() {
		t.Error("pendingChange should reset to zero after dispatch")
	}
}

func TestPollSuppressesNetworkWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched.txt")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fw, changes := newTestWatcher(t, path)

	isNetworkWriting.Store(1)
	defer isNetworkWriting.Store(0)

	base := time.Now()
	editTime := base.Add(1 * time.Second)
	if err := os.Chtimes(path, editTime, editTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	var pending time.Time
	fw.poll(editTime, &pending)
	fw.poll(editTime.Add(settleDuration+10*time.Millisecond), &pending)

	select {
	case p := <-changes:
		t.Fatalf("dispatch should be suppressed during network write, got %q", p)
	default:
	}
}

func TestPollIgnoresMissingFileTransiently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched.txt")
	if err := os.WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fw, _ := newTestWatcher(t, path)
	lastMod := fw.lastMod

	os.Remove(path)

	var pending time.Time
	fw.poll(time.Now(), &pending)

	if fw.lastMod != lastMod {
		t.Error("lastMod should be unchanged when stat fails (file transiently missing)")
	}
}

func TestNetworkWriteFileIsAtomicAndCleansUpTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "received_file")

	if err := NetworkWriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("NetworkWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q, want %q", got, "hello world")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only the final file to remain, found %d entries", len(entries))
	}
}
