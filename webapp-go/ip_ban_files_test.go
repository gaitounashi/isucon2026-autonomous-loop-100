package isureal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBanFileSyncer_FilterAndCreation(t *testing.T) {
	dir := t.TempDir()
	s := newBanFileSyncer(dir)
	if s == nil {
		t.Fatalf("expected non-nil syncer")
	}
	defer s.stop()

	now := time.Now()
	bans := map[string]time.Time{
		"10.0.0.1":              now.Add(5 * time.Second),
		"2001:db8::1":           now.Add(5 * time.Second),
		"10.0.0.2":              now.Add(-5 * time.Second),
		"10.0.0.3":              now.Add(banFileLead - 10*time.Millisecond),
		"10.0.0.4":              now.Add(banFileLead),
		"../x":                  now.Add(5 * time.Second),
		"a/b":                   now.Add(5 * time.Second),
		"":                      now.Add(5 * time.Second),
		strings.Repeat("a", 46): now.Add(5 * time.Second),
	}

	s.sync(func() []map[string]time.Time { return []map[string]time.Time{bans} }, nil)

	for _, ip := range []string{"10.0.0.1", "2001:db8::1"} {
		if strings.Contains(ip, ":") && runtime.GOOS == "windows" {
			continue
		}
		path := filepath.Join(dir, ip)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected file %s to exist: %v", path, err)
		}
		if info.Size() != 0 {
			t.Fatalf("expected file %s to be empty, got size %d", path, info.Size())
		}
	}

	for _, ip := range []string{"10.0.0.2", "10.0.0.3", "10.0.0.4", "../x", "a/b"} {
		path := filepath.Join(dir, ip)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected file %s to not exist", path)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir failed: %v", err)
	}
	expectedCount := 2
	if runtime.GOOS == "windows" {
		expectedCount = 1
	}
	if len(entries) != expectedCount {
		t.Fatalf("expected %d files in dir, got %d", expectedCount, len(entries))
	}
}

func TestBanFileSyncer_StaleFilesAndSubdirectories(t *testing.T) {
	dir := t.TempDir()
	staleFile := filepath.Join(dir, "stale.txt")
	if err := os.WriteFile(staleFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	nestedFile := filepath.Join(subDir, "nested.txt")
	if err := os.WriteFile(nestedFile, []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	s := newBanFileSyncer(dir)
	defer s.stop()

	now := time.Now()
	s.sync(func() []map[string]time.Time {
		return []map[string]time.Time{{"192.168.1.1": now.Add(10 * time.Second)}}
	}, nil)

	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be removed")
	}
	info, err := os.Stat(subDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected subdir to remain intact")
	}
	if _, err := os.Stat(nestedFile); err != nil {
		t.Fatalf("expected nested file to remain intact")
	}
	if _, err := os.Stat(filepath.Join(dir, "192.168.1.1")); err != nil {
		t.Fatalf("expected active ban file to exist")
	}
}

func TestBanFileSyncer_TimerExpiry(t *testing.T) {
	dir := t.TempDir()
	s := newBanFileSyncer(dir)
	defer s.stop()

	now := time.Now()
	until := now.Add(300 * time.Millisecond)
	bans := []map[string]time.Time{{"10.0.0.2": until}}

	source := func() []map[string]time.Time {
		return bans
	}
	var onExpire func()
	onExpire = func() {
		s.sync(source, onExpire)
	}

	s.sync(source, onExpire)

	target := filepath.Join(dir, "10.0.0.2")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file %s to exist immediately", target)
	}

	deadline := time.Now().Add(2 * time.Second)
	removed := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			removed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !removed {
		t.Fatalf("file %s was not removed after expiry", target)
	}
}

func TestBanFileSyncer_NilAndEmptyDir(t *testing.T) {
	var s *banFileSyncer
	s.sync(nil, nil)
	s.stop()

	empty := newBanFileSyncer("")
	if empty != nil {
		t.Fatalf("expected nil syncer for empty dir, got %v", empty)
	}
}

func TestBanFileSyncer_LatestStateWins(t *testing.T) {
	dir := t.TempDir()
	s := newBanFileSyncer(dir)
	defer s.stop()

	var currentBans []map[string]time.Time
	source := func() []map[string]time.Time {
		return currentBans
	}

	currentBans = []map[string]time.Time{{"10.0.0.1": time.Now().Add(10 * time.Second)}}
	s.sync(source, nil)

	target := filepath.Join(dir, "10.0.0.1")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file %s to exist: %v", target, err)
	}

	currentBans = nil
	s.sync(source, nil)

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be removed when source returns no bans", target)
	}
}

func TestIPBanList_Integration(t *testing.T) {
	t.Setenv("ISUREAL_DISABLE_INPROC_BAN", "")
	dir := t.TempDir()
	t.Setenv("ISUREAL_IPBAN_DIR", dir)

	l := NewIPBanList(nil)
	if l.files == nil {
		l.files = newBanFileSyncer(dir)
	}
	defer l.files.stop()

	ip := "10.0.0.1"
	for i := 0; i < 10; i++ {
		l.ObserveActionFailure("login", 401, ip, "")
	}

	target := filepath.Join(dir, ip)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file %s to exist after tripping threshold: %v", target, err)
	}

	l.ResetLocalState()

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be removed after ResetLocalState", target)
	}
}
