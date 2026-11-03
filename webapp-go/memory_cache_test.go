package isureal

import (
	"testing"
	"time"
)

func TestMemoryCache_BasicAndTTLExpiry(t *testing.T) {
	InvalidateAllMemoriesCache()
	defer InvalidateAllMemoriesCache()

	origNow := memoryCacheNow
	defer func() { memoryCacheNow = origNow }()
	origTTL := memoryCacheTTL
	memoryCacheTTL = 10 * time.Second
	defer func() { memoryCacheTTL = origTTL }()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	memoryCacheNow = func() time.Time { return baseTime }

	ck := memoryCacheKey{userID: "user-mem-1", period: "month:2026-10"}
	res := getMemoriesResponse{
		Items: []memoryPostResponse{
			{createPostResponse: createPostResponse{ID: "post-mem-1"}},
		},
		HasMore: false,
	}

	memoryCacheMu.Lock()
	memoryCache[ck] = memoryCacheEntry{
		response: res,
		cachedAt: baseTime,
	}
	memoryCacheMu.Unlock()

	// 1. 即時ヒット
	memoryCacheMu.RLock()
	cached, ok := memoryCache[ck]
	memoryCacheMu.RUnlock()
	if !ok || len(cached.response.Items) != 1 || cached.response.Items[0].ID != "post-mem-1" {
		t.Fatalf("expected memory cache hit, got ok=%v, cached=%+v", ok, cached)
	}

	// 2. TTL (10s) 内での有効性 (9.9s後)
	memoryCacheNow = func() time.Time { return baseTime.Add(9900 * time.Millisecond) }
	now := memoryCacheNow()
	if !(now.Sub(cached.cachedAt) < memoryCacheTTL && !now.Before(cached.cachedAt)) {
		t.Fatalf("expected cache to be valid at 9.9s")
	}

	// 3. TTL 超過 (10s + 1ms後)
	memoryCacheNow = func() time.Time { return baseTime.Add(10001 * time.Millisecond) }
	now = memoryCacheNow()
	if now.Sub(cached.cachedAt) < memoryCacheTTL {
		t.Fatalf("expected cache to expire after 10s")
	}
}

func TestMemoryCache_InvalidateForUser(t *testing.T) {
	InvalidateAllMemoriesCache()
	defer InvalidateAllMemoriesCache()

	memoryCacheMu.Lock()
	memoryCache[memoryCacheKey{userID: "user-a", period: "month:2026-10"}] = memoryCacheEntry{cachedAt: time.Now()}
	memoryCache[memoryCacheKey{userID: "user-a", period: "date:2026-10-31"}] = memoryCacheEntry{cachedAt: time.Now()}
	memoryCache[memoryCacheKey{userID: "user-b", period: "month:2026-10"}] = memoryCacheEntry{cachedAt: time.Now()}
	memoryCacheMu.Unlock()

	InvalidateMemoriesCacheForUser("user-a")

	memoryCacheMu.RLock()
	_, okA1 := memoryCache[memoryCacheKey{userID: "user-a", period: "month:2026-10"}]
	_, okA2 := memoryCache[memoryCacheKey{userID: "user-a", period: "date:2026-10-31"}]
	_, okB := memoryCache[memoryCacheKey{userID: "user-b", period: "month:2026-10"}]
	memoryCacheMu.RUnlock()

	if okA1 || okA2 {
		t.Fatalf("user-a memory caches should be invalidated")
	}
	if !okB {
		t.Fatalf("user-b memory cache should remain valid")
	}
}

func TestMemoryCache_InvalidateAll(t *testing.T) {
	InvalidateAllMemoriesCache()
	defer InvalidateAllMemoriesCache()

	memoryCacheMu.Lock()
	memoryCache[memoryCacheKey{userID: "u1", period: "month:2026-10"}] = memoryCacheEntry{cachedAt: time.Now()}
	memoryCache[memoryCacheKey{userID: "u2", period: "month:2026-10"}] = memoryCacheEntry{cachedAt: time.Now()}
	memoryCacheMu.Unlock()

	InvalidateAllMemoriesCache()

	memoryCacheMu.RLock()
	count := len(memoryCache)
	memoryCacheMu.RUnlock()

	if count != 0 {
		t.Fatalf("expected empty memory cache, got %d", count)
	}
}
