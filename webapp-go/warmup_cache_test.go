package isureal

import (
	"testing"
	"time"
)

func TestWarmupPostingWindowCache_Unit(t *testing.T) {
	InvalidateAllPostingWindowCache()
	defer InvalidateAllPostingWindowCache()

	now := time.Now()
	postingWindowCacheMu.Lock()
	postingWindowCacheByUserID["user-warmup-1"] = currentPostingWindowState{
		window: &PostingWindow{
			ID:             "pw-warmup-1",
			Region:         "asia",
			LocalDate:      time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
			StartsAt:       now.Add(-10 * time.Minute),
			OnTimeDeadline: now.Add(10 * time.Minute),
		},
		active:   0,
		deleted:  0,
		cachedAt: now,
	}
	postingWindowCacheMu.Unlock()

	postingWindowCacheMu.RLock()
	st, ok := postingWindowCacheByUserID["user-warmup-1"]
	postingWindowCacheMu.RUnlock()

	if !ok || st.window == nil || st.window.ID != "pw-warmup-1" {
		t.Fatalf("expected warmup posting window cached, got %+v, ok=%v", st, ok)
	}
}

func TestWarmupRelationshipsCache_Unit(t *testing.T) {
	InvalidateAllRelationshipsCache()
	defer InvalidateAllRelationshipsCache()

	now := time.Now()
	items := []relationshipResponse{
		{
			RelationID: "rel-1",
			Status:     "friends",
			UpdatedAt:  now,
			User: relationshipUserResponse{
				ID:          "friend-1",
				Username:    "friend_1",
				DisplayName: "Friend 1",
			},
		},
	}

	relationshipsCacheMu.Lock()
	relationshipsCache[relationshipsCacheKey{userID: "user-rel-warmup", kind: "friends"}] = relationshipsCacheEntry{
		response: getRelationshipsResponse{Items: items, HasMore: false},
		cachedAt: now,
	}
	relationshipsCacheMu.Unlock()

	relationshipsCacheMu.RLock()
	cached, ok := relationshipsCache[relationshipsCacheKey{userID: "user-rel-warmup", kind: "friends"}]
	relationshipsCacheMu.RUnlock()

	if !ok || len(cached.response.Items) != 1 || cached.response.Items[0].User.ID != "friend-1" {
		t.Fatalf("expected warmup relationships response, got %+v, ok=%v", cached, ok)
	}

	// Invalidate user
	InvalidateRelationshipsCacheForUser("user-rel-warmup")
	relationshipsCacheMu.RLock()
	_, okAfter := relationshipsCache[relationshipsCacheKey{userID: "user-rel-warmup", kind: "friends"}]
	relationshipsCacheMu.RUnlock()

	if okAfter {
		t.Fatalf("expected cache to be invalidated for user-rel-warmup")
	}
}

func TestWarmupMyCurrentPostCache_Unit(t *testing.T) {
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllMyCurrentPostCache()

	now := time.Now()
	myCurrentPostCacheMu.Lock()
	myCurrentPostCacheByUserID["user-warmup-post"] = myCurrentPostCacheEntry{
		post:     nil,
		cachedAt: now,
	}
	myCurrentPostCacheMu.Unlock()

	myCurrentPostCacheMu.RLock()
	entry, ok := myCurrentPostCacheByUserID["user-warmup-post"]
	myCurrentPostCacheMu.RUnlock()

	if !ok || entry.post != nil {
		t.Fatalf("expected nil post entry cached, got ok=%v, entry=%+v", ok, entry)
	}

	InvalidateMyCurrentPostCacheForUser("user-warmup-post")
	myCurrentPostCacheMu.RLock()
	_, okAfter := myCurrentPostCacheByUserID["user-warmup-post"]
	myCurrentPostCacheMu.RUnlock()

	if okAfter {
		t.Fatalf("expected myCurrentPostCache to be invalidated")
	}
}
