package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// cloudflaredVersion is pinned so downloads are reproducible and verifiable
// against a known checksum, rather than trusting whatever "latest" resolves
// to at install time. Bump alongside the sha256 table below.
const cloudflaredVersion = "2026.7.1"

// cloudflaredAsset describes one platform's release artifact, taken from
// GitHub's published (and API-reported) sha256 digest for that exact file.
type cloudflaredAsset struct {
	url    string
	sha256 string
	// archived is true when the download is a .tgz containing a "cloudflared"
	// binary rather than the raw executable.
	archived bool
}

var cloudflaredAssets = map[string]cloudflaredAsset{
	"linux/amd64": {
		url:    "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/cloudflared-linux-amd64",
		sha256: "79a0ade7fc854f62c1aaef48424d9d979e8c2fcd039189d24db82b84cd146be1",
	},
	"linux/arm64": {
		url:    "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/cloudflared-linux-arm64",
		sha256: "18f2c9bfc7a67a971bd96f1a5a1935def3c1e52aa386626f1566f04e9b5478d6",
	},
	"darwin/amd64": {
		url:      "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/cloudflared-darwin-amd64.tgz",
		sha256:   "05871d772745b0f8398c7be89113a0b178474936ff20638b3b07c0e7262f717e",
		archived: true,
	},
	"darwin/arm64": {
		url:      "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/cloudflared-darwin-arm64.tgz",
		sha256:   "6d4b59383cdad387834d7ae5704fc512882b2d078074bf5770e02b186a0981ed",
		archived: true,
	},
	"windows/amd64": {
		url:    "https://github.com/cloudflare/cloudflared/releases/download/" + cloudflaredVersion + "/cloudflared-windows-amd64.exe",
		sha256: "ccb0756de288d3c2c076d19764ca53e0849a10f2dd9c23f8656ac42bdeb45001",
	},
}

// quickTunnelURLPattern matches the public hostname cloudflared prints to
// stderr once a quick tunnel is established, e.g.
// "https://random-words-1234.trycloudflare.com".
var quickTunnelURLPattern = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

// QuickTunnel wraps a running cloudflared subprocess exposing a local
// address on a random, free *.trycloudflare.com hostname.
type QuickTunnel struct {
	URL string // e.g. "https://random-words-1234.trycloudflare.com"
	cmd *exec.Cmd
}

// WebSocketURL returns the value a peer should set DEADDROP_URL to in order
// to reach the tunnel's /ws endpoint.
func (t *QuickTunnel) WebSocketURL() string {
	return "wss" + strings.TrimPrefix(t.URL, "https") + "/ws"
}

// Close terminates the cloudflared subprocess, tearing down the tunnel.
func (t *QuickTunnel) Close() error {
	if t.cmd.Process == nil {
		return nil
	}
	return t.cmd.Process.Kill()
}

// StartQuickTunnel downloads (if needed) and launches cloudflared to expose
// localAddr (e.g. ":8080" or "localhost:8080") on a free, ephemeral public
// URL — no Cloudflare account required. It blocks until the URL is parsed
// from cloudflared's output, the context is cancelled, or a fixed timeout
// elapses.
func StartQuickTunnel(ctx context.Context, localAddr string) (*QuickTunnel, error) {
	bin, err := ensureCloudflared(ctx)
	if err != nil {
		return nil, fmt.Errorf("startQuickTunnel: %w", err)
	}

	target := localAddr
	if strings.HasPrefix(target, ":") {
		target = "localhost" + target
	}

	cmd := exec.Command(bin, "tunnel", "--url", "http://"+target, "--no-autoupdate")

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("startQuickTunnel: stderr pipe: %w", err)
	}
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("startQuickTunnel: start cloudflared: %w", err)
	}

	urlCh := make(chan string, 1)
	go scanForTunnelURL(stderr, urlCh)

	select {
	case url := <-urlCh:
		return &QuickTunnel{URL: url, cmd: cmd}, nil
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return nil, errors.New("startQuickTunnel: timed out waiting for tunnel URL")
	case <-ctx.Done():
		cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

// scanForTunnelURL reads cloudflared's stderr line by line, sending the
// first trycloudflare.com URL it finds on found, then keeps draining the
// stream so cloudflared's output pipe never fills up and blocks the
// subprocess.
func scanForTunnelURL(r io.Reader, found chan<- string) {
	scanner := bufio.NewScanner(r)
	sent := false
	for scanner.Scan() {
		if sent {
			continue
		}
		if m := quickTunnelURLPattern.FindString(scanner.Text()); m != "" {
			sent = true
			found <- m
		}
	}
}

// ensureCloudflared returns the path to a cloudflared binary, preferring one
// already on PATH and otherwise downloading and checksum-verifying the
// pinned release into the user's cache directory. Subsequent calls reuse the
// cached, already-verified binary instead of re-downloading it.
func ensureCloudflared(ctx context.Context) (string, error) {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p, nil
	}

	asset, ok := cloudflaredAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no cloudflared build available for %s/%s — install cloudflared manually and put it on PATH", runtime.GOOS, runtime.GOARCH)
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	destDir := filepath.Join(cacheDir, "deaddrop")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	binName := "cloudflared-" + cloudflaredVersion
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	destPath := filepath.Join(destDir, binName)

	if verifyChecksum(destPath, asset.sha256) == nil {
		return destPath, nil
	}

	if err := downloadCloudflared(ctx, asset, destPath); err != nil {
		return "", err
	}
	return destPath, nil
}

// downloadCloudflared fetches asset.url, verifies it against asset.sha256
// before trusting a single byte of it, then installs it (extracting from
// the .tgz first, if archived) to destPath.
func downloadCloudflared(ctx context.Context, asset cloudflaredAsset, destPath string) error {
	fmt.Fprintf(os.Stderr, "  Downloading cloudflared %s for %s/%s (first run only)...\n", cloudflaredVersion, runtime.GOOS, runtime.GOARCH)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download cloudflared: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download cloudflared: unexpected status %s", resp.Status)
	}

	rawPath := destPath + ".download"
	raw, err := os.OpenFile(rawPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create temp download file: %w", err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(raw, hasher), resp.Body)
	closeErr := raw.Close()
	if copyErr != nil {
		os.Remove(rawPath)
		return fmt.Errorf("write downloaded file: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(rawPath)
		return fmt.Errorf("close downloaded file: %w", closeErr)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != asset.sha256 {
		os.Remove(rawPath)
		return fmt.Errorf("checksum mismatch for cloudflared download: got %s, want %s", got, asset.sha256)
	}

	if asset.archived {
		err = extractCloudflaredBinary(rawPath, destPath)
		os.Remove(rawPath)
		if err != nil {
			return err
		}
	} else if err := os.Rename(rawPath, destPath); err != nil {
		os.Remove(rawPath)
		return fmt.Errorf("install cloudflared binary: %w", err)
	}

	return os.Chmod(destPath, 0o755)
}

// extractCloudflaredBinary pulls the "cloudflared" entry out of a .tar.gz
// archive and writes it to destPath.
func extractCloudflaredBinary(tgzPath, destPath string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("cloudflared binary not found in archive")
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(hdr.Name) != "cloudflared" {
			continue
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("create extracted binary: %w", err)
		}
		defer out.Close()

		if _, err := io.Copy(out, tr); err != nil {
			return fmt.Errorf("extract binary: %w", err)
		}
		return nil
	}
}

// verifyChecksum reports a non-nil error unless path exists and its sha256
// matches want.
func verifyChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}
