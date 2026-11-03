package isureal

import (
	"testing"
	"time"
)

func TestMyCurrentPostCache_BasicAndTTLExpiry(t *testing.T) {
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllMyCurrentPostCache()

	origNow := myCurrentPostCacheNow
	defer func() { myCurrentPostCacheNow = origNow }()
	origTTL := myCurrentPostCacheTTL
	myCurrentPostCacheTTL = 10 * time.Second
	defer func() { myCurrentPostCacheTTL = origTTL }()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	myCurrentPostCacheNow = func() time.Time { return baseTime }

	p := &Post{
		ID:              "post-1",
		AuthorID:        "user-1",
		PostingWindowID: "pw-1",
		ImageID:         "img-1",
		Caption:         "Hello ISUCON",
		Visibility:      "public",
		PostedAt:        baseTime,
		OnTimeDeadline:  baseTime.Add(24 * time.Hour),
	}

	myCurrentPostCacheMu.Lock()
	myCurrentPostCacheByUserID["user-1"] = myCurrentPostCacheEntry{
		post:     p,
		cachedAt: baseTime,
	}
	myCurrentPostCacheMu.Unlock()

	// 1. 即時ヒット
	myCurrentPostCacheMu.RLock()
	cached, ok := myCurrentPostCacheByUserID["user-1"]
	myCurrentPostCacheMu.RUnlock()
	if !ok || cached.post == nil || cached.post.ID != "post-1" {
		t.Fatalf("expected post cache hit, got ok=%v, cached=%+v", ok, cached)
	}

	// 2. TTL (10s) 内での有効性 (9.9s後)
	myCurrentPostCacheNow = func() time.Time { return baseTime.Add(9900 * time.Millisecond) }
	now := myCurrentPostCacheNow()
	if !(now.Sub(cached.cachedAt) < myCurrentPostCacheTTL && !now.Before(cached.cachedAt)) {
		t.Fatalf("expected cache to be valid at 9.9s")
	}

	// 3. TTL 超過 (10s + 1ms後)
	myCurrentPostCacheNow = func() time.Time { return baseTime.Add(10001 * time.Millisecond) }
	now = myCurrentPostCacheNow()
	if now.Sub(cached.cachedAt) < myCurrentPostCacheTTL {
		t.Fatalf("expected cache to expire after 10s")
	}
}

func TestMyCurrentPostCache_404NilPostCaching(t *testing.T) {
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllMyCurrentPostCache()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	// 404 (post == nil) のキャッシュ
	myCurrentPostCacheMu.Lock()
	myCurrentPostCacheByUserID["user-no-post"] = myCurrentPostCacheEntry{
		post:     nil,
		cachedAt: baseTime,
	}
	myCurrentPostCacheMu.Unlock()

	myCurrentPostCacheMu.RLock()
	cached, ok := myCurrentPostCacheByUserID["user-no-post"]
	myCurrentPostCacheMu.RUnlock()

	if !ok {
		t.Fatalf("expected 404 entry to be cached")
	}
	if cached.post != nil {
		t.Fatalf("expected cached.post to be nil (404), got %+v", cached.post)
	}
}

func TestMyCurrentPostCache_InvalidateForUser(t *testing.T) {
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllMyCurrentPostCache()

	p := &Post{ID: "post-common"}
	myCurrentPostCacheMu.Lock()
	myCurrentPostCacheByUserID["user-a"] = myCurrentPostCacheEntry{post: p, cachedAt: time.Now()}
	myCurrentPostCacheByUserID["user-b"] = myCurrentPostCacheEntry{post: p, cachedAt: time.Now()}
	myCurrentPostCacheMu.Unlock()

	InvalidateMyCurrentPostCacheForUser("user-a")

	myCurrentPostCacheMu.RLock()
	_, okA := myCurrentPostCacheByUserID["user-a"]
	cachedB, okB := myCurrentPostCacheByUserID["user-b"]
	myCurrentPostCacheMu.RUnlock()

	if okA {
		t.Fatalf("user-a should be invalidated")
	}
	if !okB || cachedB.post == nil || cachedB.post.ID != "post-common" {
		t.Fatalf("user-b should remain cached")
	}
}

func TestMyCurrentPostCache_InvalidateAll(t *testing.T) {
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllMyCurrentPostCache()

	myCurrentPostCacheMu.Lock()
	myCurrentPostCacheByUserID["user-1"] = myCurrentPostCacheEntry{cachedAt: time.Now()}
	myCurrentPostCacheByUserID["user-2"] = myCurrentPostCacheEntry{cachedAt: time.Now()}
	myCurrentPostCacheMu.Unlock()

	InvalidateAllMyCurrentPostCache()

	myCurrentPostCacheMu.RLock()
	count := len(myCurrentPostCacheByUserID)
	myCurrentPostCacheMu.RUnlock()

	if count != 0 {
		t.Fatalf("expected empty cache after InvalidateAll, got %d", count)
	}
}
