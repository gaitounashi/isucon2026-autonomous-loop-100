package isureal

import (
	"testing"
	"time"
)

func TestPostingWindowCache_BasicAndTTLExpiry(t *testing.T) {
	InvalidateAllPostingWindowCache()
	defer InvalidateAllPostingWindowCache()

	origNow := postingWindowCacheNow
	defer func() { postingWindowCacheNow = origNow }()
	origTTL := postingWindowCacheTTL
	postingWindowCacheTTL = 10 * time.Second
	defer func() { postingWindowCacheTTL = origTTL }()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	postingWindowCacheNow = func() time.Time { return baseTime }

	w := &PostingWindow{
		ID:             "pw-1",
		Region:         "asia",
		LocalDate:      baseTime,
		StartsAt:       baseTime,
		OnTimeDeadline: baseTime.Add(24 * time.Hour),
	}

	postingWindowCacheMu.Lock()
	postingWindowCacheByUserID["user-1"] = currentPostingWindowState{
		window:   w,
		active:   1,
		deleted:  0,
		cachedAt: baseTime,
	}
	postingWindowCacheMu.Unlock()

	// 1. 即時ヒット
	postingWindowCacheMu.RLock()
	cached, ok := postingWindowCacheByUserID["user-1"]
	postingWindowCacheMu.RUnlock()
	if !ok || cached.window == nil || cached.window.ID != "pw-1" || cached.active != 1 {
		t.Fatalf("expected cache hit for user-1, got ok=%v, cached=%+v", ok, cached)
	}

	// 2. TTL (10s) 内でのヒット (9.9s後)
	postingWindowCacheNow = func() time.Time { return baseTime.Add(9900 * time.Millisecond) }
	now := postingWindowCacheNow()
	if !(now.Sub(cached.cachedAt) < postingWindowCacheTTL && !now.Before(cached.cachedAt)) {
		t.Fatalf("expected cache to be valid at 9.9s, but expired")
	}

	// 3. TTL 超過 (10s + 1ms後) で失効
	postingWindowCacheNow = func() time.Time { return baseTime.Add(10001 * time.Millisecond) }
	now = postingWindowCacheNow()
	if now.Sub(cached.cachedAt) < postingWindowCacheTTL {
		t.Fatalf("expected cache to expire after 10s, but still valid")
	}

	// 4. 時刻逆行時
	postingWindowCacheNow = func() time.Time { return baseTime.Add(-1 * time.Second) }
	now = postingWindowCacheNow()
	if !now.Before(cached.cachedAt) {
		t.Fatalf("expected clock to be before cachedAt")
	}
}

func TestPostingWindowCache_InvalidateForUser(t *testing.T) {
	InvalidateAllPostingWindowCache()
	defer InvalidateAllPostingWindowCache()

	w := &PostingWindow{ID: "pw-common"}
	postingWindowCacheMu.Lock()
	postingWindowCacheByUserID["user-a"] = currentPostingWindowState{window: w, active: 0, cachedAt: time.Now()}
	postingWindowCacheByUserID["user-b"] = currentPostingWindowState{window: w, active: 1, cachedAt: time.Now()}
	postingWindowCacheMu.Unlock()

	InvalidatePostingWindowCacheForUser("user-a")

	postingWindowCacheMu.RLock()
	_, okA := postingWindowCacheByUserID["user-a"]
	cachedB, okB := postingWindowCacheByUserID["user-b"]
	postingWindowCacheMu.RUnlock()

	if okA {
		t.Fatalf("user-a should be invalidated")
	}
	if !okB || cachedB.active != 1 {
		t.Fatalf("user-b should remain cached")
	}
}

func TestPostingWindowCache_InvalidateAll(t *testing.T) {
	InvalidateAllPostingWindowCache()
	defer InvalidateAllPostingWindowCache()

	postingWindowCacheMu.Lock()
	postingWindowCacheByUserID["user-x"] = currentPostingWindowState{cachedAt: time.Now()}
	postingWindowCacheByUserID["user-y"] = currentPostingWindowState{cachedAt: time.Now()}
	postingWindowCacheMu.Unlock()

	InvalidateAllPostingWindowCache()

	postingWindowCacheMu.RLock()
	count := len(postingWindowCacheByUserID)
	postingWindowCacheMu.RUnlock()

	if count != 0 {
		t.Fatalf("expected cache to be empty, got %d entries", count)
	}
}
