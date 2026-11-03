package isureal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// All initial snapshot rows have created_at <= 2026-10-28 01:36:31Z.
var imageInitBoundary = time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)

type inflightImageLoad struct {
	done chan struct{}
	data []byte
	err  error
}

type ImageCache struct {
	baseDir     string
	createdDirs sync.Map
	tmpSeq      atomic.Uint64

	mu       sync.Mutex
	inflight map[string]*inflightImageLoad
}

func NewImageCache(baseDir string) *ImageCache {
	c := &ImageCache{
		baseDir:  baseDir,
		inflight: make(map[string]*inflightImageLoad),
	}
	if baseDir != "" {
		_ = os.MkdirAll(filepath.Join(baseDir, "init"), 0o755)
		_ = os.MkdirAll(filepath.Join(baseDir, "run"), 0o755)
		go c.gcLoop()
	}
	return c
}

func validCacheImageID(id string) bool {
	if len(id) < 2 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func (c *ImageCache) filePath(id string, createdAt time.Time) (dirPath string, fullPath string, ok bool) {
	if c == nil || c.baseDir == "" || !validCacheImageID(id) {
		return "", "", false
	}
	tier := "run"
	if createdAt.Before(imageInitBoundary) {
		tier = "init"
	}
	shard := strings.ToLower(id[:2])
	dirPath = filepath.Join(c.baseDir, tier, shard)
	fullPath = filepath.Join(dirPath, id)
	return dirPath, fullPath, true
}

func (c *ImageCache) LocalPath(id string, createdAt time.Time) (string, bool) {
	_, fullPath, ok := c.filePath(id, createdAt)
	if !ok {
		return "", false
	}
	st, err := os.Stat(fullPath)
	if err != nil || st.IsDir() {
		return "", false
	}
	return fullPath, true
}

func (c *ImageCache) Get(id string, createdAt time.Time) ([]byte, bool) {
	_, fullPath, ok := c.filePath(id, createdAt)
	if !ok {
		return nil, false
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *ImageCache) Put(id string, createdAt time.Time, data []byte) {
	dirPath, fullPath, ok := c.filePath(id, createdAt)
	if !ok || len(data) == 0 {
		return
	}
	if _, loaded := c.createdDirs.Load(dirPath); !loaded {
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return
		}
		c.createdDirs.Store(dirPath, struct{}{})
	}
	seq := c.tmpSeq.Add(1)
	tmpPath := fmt.Sprintf("%s.tmp.%d", fullPath, seq)
	err := os.WriteFile(tmpPath, data, 0o644)
	if errors.Is(err, fs.ErrNotExist) {
		// the shard dir was removed behind our back (createdDirs is only a hint): recreate and retry
		if mkErr := os.MkdirAll(dirPath, 0o755); mkErr == nil {
			err = os.WriteFile(tmpPath, data, 0o644)
		}
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func (c *ImageCache) Delete(id string, createdAt time.Time) {
	_, fullPath, ok := c.filePath(id, createdAt)
	if !ok {
		return
	}
	_ = os.Remove(fullPath)
}

func (c *ImageCache) GetOrLoad(ctx context.Context, id string, createdAt time.Time, loader func(context.Context) ([]byte, error)) ([]byte, error) {
	if data, ok := c.Get(id, createdAt); ok {
		return data, nil
	}
	if c == nil {
		return loader(ctx)
	}

	c.mu.Lock()
	if call, exists := c.inflight[id]; exists {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.data, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &inflightImageLoad{done: make(chan struct{})}
	c.inflight[id] = call
	c.mu.Unlock()

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	data, err := loader(loadCtx)
	cancel()
	if err == nil {
		c.Put(id, createdAt, data)
	}

	call.data = data
	call.err = err
	close(call.done)

	c.mu.Lock()
	delete(c.inflight, id)
	c.mu.Unlock()

	return data, err
}

func (c *ImageCache) ResetRunCache() {
	if c == nil || c.baseDir == "" {
		return
	}
	c.createdDirs.Clear()
	runDir := filepath.Join(c.baseDir, "run")
	staleDir := fmt.Sprintf("%s.old.%d", runDir, time.Now().UnixNano())
	if err := os.Rename(runDir, staleDir); err == nil {
		_ = os.MkdirAll(runDir, 0o755)
		go func() {
			_ = os.RemoveAll(staleDir)
		}()
	} else {
		_ = os.MkdirAll(runDir, 0o755)
	}
}

func (c *ImageCache) gcLoop() {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		c.sweepOldRunFiles(12 * time.Minute)
	}
}

func (c *ImageCache) sweepOldRunFiles(maxAge time.Duration) {
	if c == nil || c.baseDir == "" {
		return
	}
	runDir := filepath.Join(c.baseDir, "run")
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.WalkDir(runDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, infoErr := d.Info(); infoErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
}
