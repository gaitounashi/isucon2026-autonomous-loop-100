package isureal

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestImageCacheOperations(t *testing.T) {
	cache := NewImageCache(t.TempDir())

	initTime := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	runTime := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)

	initPayload := []byte("init-jpeg-bytes")
	runPayload := []byte("run-jpeg-bytes")

	cache.Put("11111111-2222-3333-4444-555555555555", initTime, initPayload)
	cache.Put("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", runTime, runPayload)

	if got, ok := cache.Get("11111111-2222-3333-4444-555555555555", initTime); !ok || !bytes.Equal(got, initPayload) {
		t.Fatalf("expected init image hit, got ok=%v data=%q", ok, got)
	}
	if got, ok := cache.Get("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", runTime); !ok || !bytes.Equal(got, runPayload) {
		t.Fatalf("expected run image hit, got ok=%v data=%q", ok, got)
	}

	// Path traversal IDs must always be rejected.
	if _, ok := cache.Get("../etc/passwd", runTime); ok {
		t.Fatalf("unexpected hit on unsafe image ID")
	}

	// ResetRunCache must evict run tier while keeping init tier warm.
	cache.ResetRunCache()
	if got, ok := cache.Get("11111111-2222-3333-4444-555555555555", initTime); !ok || !bytes.Equal(got, initPayload) {
		t.Fatalf("expected init image to remain after ResetRunCache, got ok=%v", ok)
	}
	if _, ok := cache.Get("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", runTime); ok {
		t.Fatalf("expected run image to be cleared after ResetRunCache")
	}

	// Concurrent GetOrLoad should coalesce into a single loader invocation.
	var loads atomic.Int32
	var wg sync.WaitGroup
	targetID := "ffffffff-1111-2222-3333-444444444444"
	expected := []byte("coalesced-image-data")
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.GetOrLoad(context.Background(), targetID, runTime, func(ctx context.Context) ([]byte, error) {
				loads.Add(1)
				time.Sleep(20 * time.Millisecond)
				return expected, nil
			})
			if err != nil || !bytes.Equal(got, expected) {
				t.Errorf("unexpected GetOrLoad result: err=%v got=%q", err, got)
			}
		}()
	}
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("expected 1 coalesced DB load, got %d", n)
	}
}
