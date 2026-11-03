package isureal

import (
	"sync/atomic"
	"testing"
	"time"
)

// A load that started before an invalidation (e.g. posting window opened via NOTIFY)
// must not be cached; with the 60s TTL a stale "no window" would hide the window.
func TestPostingWindowAndMyPostCache_InvalidationBumpsGeneration(t *testing.T) {
	pwGen := atomic.LoadUint64(&postingWindowCacheGen)
	InvalidateAllPostingWindowCache()
	if atomic.LoadUint64(&postingWindowCacheGen) == pwGen {
		t.Fatalf("InvalidateAllPostingWindowCache must bump generation")
	}
	pwGen = atomic.LoadUint64(&postingWindowCacheGen)
	InvalidatePostingWindowCacheForUser("gen-user")
	if atomic.LoadUint64(&postingWindowCacheGen) == pwGen {
		t.Fatalf("InvalidatePostingWindowCacheForUser must bump generation")
	}

	mpGen := atomic.LoadUint64(&myCurrentPostCacheGen)
	InvalidateAllMyCurrentPostCache()
	if atomic.LoadUint64(&myCurrentPostCacheGen) == mpGen {
		t.Fatalf("InvalidateAllMyCurrentPostCache must bump generation")
	}
	mpGen = atomic.LoadUint64(&myCurrentPostCacheGen)
	InvalidateMyCurrentPostCacheForUser("gen-user")
	if atomic.LoadUint64(&myCurrentPostCacheGen) == mpGen {
		t.Fatalf("InvalidateMyCurrentPostCacheForUser must bump generation")
	}
}

func TestPostingWindowAndMyPostCache_TTLsAreLong(t *testing.T) {
	if postingWindowCacheTTL < 30*time.Second || myCurrentPostCacheTTL < 30*time.Second {
		t.Fatalf("expected long TTLs, got window=%v myPost=%v", postingWindowCacheTTL, myCurrentPostCacheTTL)
	}
}
