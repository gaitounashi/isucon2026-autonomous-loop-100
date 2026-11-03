package isureal

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func seedRelAndMem(userID string) {
	now := time.Now()
	relationshipsCacheMu.Lock()
	relationshipsCache[relationshipsCacheKey{userID: userID, kind: "friends"}] = relationshipsCacheEntry{cachedAt: now}
	relationshipsCacheMu.Unlock()
	memoryCacheMu.Lock()
	memoryCache[memoryCacheKey{userID: userID, period: "month:2026-10"}] = memoryCacheEntry{cachedAt: now}
	memoryCacheMu.Unlock()
}

func hasRel(userID string) bool {
	relationshipsCacheMu.RLock()
	defer relationshipsCacheMu.RUnlock()
	_, ok := relationshipsCache[relationshipsCacheKey{userID: userID, kind: "friends"}]
	return ok
}

func hasMem(userID string) bool {
	memoryCacheMu.RLock()
	defer memoryCacheMu.RUnlock()
	_, ok := memoryCache[memoryCacheKey{userID: userID, period: "month:2026-10"}]
	return ok
}

func TestHandleCacheNotify_Tokens(t *testing.T) {
	InvalidateAllRelationshipsCache()
	InvalidateAllMemoriesCache()
	defer InvalidateAllRelationshipsCache()
	defer InvalidateAllMemoriesCache()

	for _, u := range []string{"u1", "u2", "u3"} {
		seedRelAndMem(u)
	}

	HandleCacheNotify("rel:u1,mem:u2,bogus,rel:")
	if hasRel("u1") || !hasMem("u1") {
		t.Fatalf("rel:u1 should drop only u1's relationships")
	}
	if hasMem("u2") || !hasRel("u2") {
		t.Fatalf("mem:u2 should drop only u2's memories")
	}
	if !hasRel("u3") || !hasMem("u3") {
		t.Fatalf("u3 should be untouched")
	}

	HandleCacheNotify("rel:*")
	if hasRel("u2") || hasRel("u3") || !hasMem("u3") {
		t.Fatalf("rel:* should clear all relationships only")
	}

	HandleCacheNotify("mem:*")
	if hasMem("u1") || hasMem("u3") {
		t.Fatalf("mem:* should clear all memories")
	}

	seedRelAndMem("u4")
	HandleCacheNotify("")
	if hasRel("u4") || hasMem("u4") {
		t.Fatalf("empty payload should clear everything")
	}
}

func TestCacheInvalidation_BumpsGeneration(t *testing.T) {
	relGen := atomic.LoadUint64(&relationshipsCacheGen)
	memGen := atomic.LoadUint64(&memoryCacheGen)

	HandleCacheNotify("rel:someone,mem:someone")

	if atomic.LoadUint64(&relationshipsCacheGen) == relGen {
		t.Fatalf("relationships invalidation must bump generation so in-flight loads are not cached")
	}
	if atomic.LoadUint64(&memoryCacheGen) == memGen {
		t.Fatalf("memories invalidation must bump generation so in-flight loads are not cached")
	}
}

func TestCacheTTLsAreLong(t *testing.T) {
	if relationshipsCacheTTL < 30*time.Second || memoryCacheTTL < 30*time.Second {
		t.Fatalf("expected long TTLs backed by isureal_cache NOTIFY, got rel=%v mem=%v", relationshipsCacheTTL, memoryCacheTTL)
	}
}

// The 025-cache-notify.sql triggers must fire on real writes (runs when ISUREAL_TEST_DB=1).
func TestCacheNotifyTriggers_FireOnRelationshipWrites(t *testing.T) {
	env := newTestEnv(t)
	a := env.registerUser("cache_notify_a")
	b := env.registerUser("cache_notify_b")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, ConfigFromEnv().DatabaseDSN())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN isureal_cache"); err != nil {
		t.Fatalf("listen: %v", err)
	}

	if rec := env.do(http.MethodPut, "/api/blocks/"+b.ID, a.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("block failed: %d %s", rec.Code, rec.Body.String())
	}

	want := []string{"rel:" + a.ID, "rel:" + b.ID, "mem:" + a.ID, "mem:" + b.ID}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			t.Fatalf("expected isureal_cache notification for block, got error: %v", err)
		}
		tokens := strings.Split(n.Payload, ",")
		if slices.Contains(tokens, want[0]) {
			for _, w := range want {
				if !slices.Contains(tokens, w) {
					t.Fatalf("payload %q missing %q", n.Payload, w)
				}
			}
			return
		}
	}
}
