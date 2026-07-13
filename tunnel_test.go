package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanForTunnelURLFindsFirstMatchOnly(t *testing.T) {
	input := strings.Join([]string{
		"2026-07-13T00:00:00Z INF Starting tunnel",
		"2026-07-13T00:00:00Z INF +--------------------------------------------------------------------------------------------+",
		"2026-07-13T00:00:00Z INF |  Your quick Tunnel has been created! Visit it at: https://witty-otters-jump.trycloudflare.com |",
		"2026-07-13T00:00:00Z INF +--------------------------------------------------------------------------------------------+",
		"2026-07-13T00:00:01Z INF Registered tunnel connection",
		"2026-07-13T00:00:02Z INF another line mentioning https://second-should-not-fire.trycloudflare.com",
	}, "\n")

	found := make(chan string, 1)
	scanForTunnelURL(strings.NewReader(input), found)

	select {
	case url := <-found:
		if url != "https://witty-otters-jump.trycloudflare.com" {
			t.Errorf("found = %q, want the first matching URL", url)
		}
	default:
		t.Fatal("expected a URL to be found")
	}

	select {
	case url := <-found:
		t.Fatalf("expected only one URL to be sent, got a second: %q", url)
	default:
	}
}

func TestScanForTunnelURLNoMatch(t *testing.T) {
	found := make(chan string, 1)
	scanForTunnelURL(strings.NewReader("no url on any of these lines\njust logs\n"), found)

	select {
	case url := <-found:
		t.Fatalf("expected no URL, got %q", url)
	default:
	}
}

func TestQuickTunnelWebSocketURL(t *testing.T) {
	tun := &QuickTunnel{URL: "https://witty-otters-jump.trycloudflare.com"}
	want := "wss://witty-otters-jump.trycloudflare.com/ws"
	if got := tun.WebSocketURL(); got != want {
		t.Errorf("WebSocketURL() = %q, want %q", got, want)
	}
}

func TestVerifyChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// sha256("hello world")
	const want = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if err := verifyChecksum(path, want); err != nil {
		t.Errorf("verifyChecksum with correct hash: %v", err)
	}

	if err := verifyChecksum(path, "0000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Error("expected mismatch error for wrong checksum")
	}

	if err := verifyChecksum(filepath.Join(t.TempDir(), "missing"), want); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestExtractCloudflaredBinaryFindsEntryAmongOthers(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	writeEntry := func(name string, content []byte) {
		hdr := &tar.Header{Name: name, Mode: 0755, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("Write(%q): %v", name, err)
		}
	}

	writeEntry("LICENSE", []byte("license text"))
	writeEntry("cloudflared", []byte("fake binary contents"))
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "archive.tgz")
	if err := os.WriteFile(tgzPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	destPath := filepath.Join(dir, "cloudflared-out")
	if err := extractCloudflaredBinary(tgzPath, destPath); err != nil {
		t.Fatalf("extractCloudflaredBinary: %v", err)
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "fake binary contents" {
		t.Errorf("extracted content = %q, want %q", got, "fake binary contents")
	}
}

func TestExtractCloudflaredBinaryMissingEntry(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{Name: "README.md", Mode: 0644, Size: 4}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write([]byte("read")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}

	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "archive.tgz")
	if err := os.WriteFile(tgzPath, buf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := extractCloudflaredBinary(tgzPath, filepath.Join(dir, "out")); err == nil {
		t.Error("expected error when archive has no cloudflared entry")
	}
}

func TestCloudflaredAssetsWellFormed(t *testing.T) {
	for platform, asset := range cloudflaredAssets {
		if len(asset.sha256) != 64 {
			t.Errorf("%s: sha256 length = %d, want 64", platform, len(asset.sha256))
		}
		if !strings.Contains(asset.url, cloudflaredVersion) {
			t.Errorf("%s: url %q does not reference pinned version %q", platform, asset.url, cloudflaredVersion)
		}
		if asset.archived != strings.HasSuffix(asset.url, ".tgz") {
			t.Errorf("%s: archived=%v inconsistent with url %q", platform, asset.archived, asset.url)
		}
	}
}
