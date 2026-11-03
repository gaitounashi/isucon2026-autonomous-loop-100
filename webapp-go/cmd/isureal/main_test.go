package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

func TestServeUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "isureal.sock")
	// A stale file left by a previous process must be replaced.
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("failed to create stale file: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	})
	ln, err := serveUnixSocket(path, handler)
	if err != nil {
		t.Fatalf("serveUnixSocket returned error: %v", err)
	}
	defer ln.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("socket file not found: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("expected a socket file, got mode %v", info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o666 {
		t.Fatalf("expected permission 0666, got %o", perm)
	}

	resp, err := unixClient(path).Get("http://unix/api/ping")
	if err != nil {
		t.Fatalf("request over unix socket failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok /api/ping" {
		t.Fatalf("unexpected response: %d %q", resp.StatusCode, body)
	}
}

func TestServeUnixSocket_InvalidPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "isureal.sock")
	if _, err := serveUnixSocket(path, http.NotFoundHandler()); err == nil {
		t.Fatalf("expected an error for a socket in a missing directory")
	}
}
