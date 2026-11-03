package isureal

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func makeTestSession(userID, tokenHash, region string) *Session {
	imgKey := []byte("key-" + userID)
	profileID := "img-" + userID
	return &Session{
		Region:    region,
		Token:     "token-" + tokenHash,
		UserID:    userID,
		TokenHash: tokenHash,
		CreatedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		User: User{
			ID:             userID,
			Username:       "user_" + userID,
			DisplayName:    "Display " + userID,
			Bio:            "Bio for " + userID,
			ProfileImageID: &profileID,
			Region:         region,
			AccountType:    "regular",
			Status:         "active",
			ImageKey:       imgKey,
		},
	}
}

func TestSessionCacheStoreAndLookup(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	tests := []struct {
		name         string
		storeToken   string
		storeSession *Session
		lookupToken  string
		expectHit    bool
		expectedUser string
	}{
		{
			name:        "miss on non-existent token",
			lookupToken: "unknown-hash",
			expectHit:   false,
		},
		{
			name:         "store and hit",
			storeToken:   "hash-1",
			storeSession: makeTestSession("user-1", "hash-1", "asia"),
			lookupToken:  "hash-1",
			expectHit:    true,
			expectedUser: "user-1",
		},
		{
			name:         "store another and hit both",
			storeToken:   "hash-2",
			storeSession: makeTestSession("user-2", "hash-2", "europe"),
			lookupToken:  "hash-2",
			expectHit:    true,
			expectedUser: "user-2",
		},
		{
			name:        "lookup first token still hits",
			lookupToken: "hash-1",
			expectHit:   true,
			expectedUser: "user-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.storeSession != nil {
				setCachedSession(tc.storeToken, tc.storeSession, sessionCacheGeneration())
			}
			got := getCachedSession(tc.lookupToken)
			if tc.expectHit {
				if got == nil {
					t.Fatalf("expected hit for %s, got nil", tc.lookupToken)
				}
				if got.UserID != tc.expectedUser {
					t.Fatalf("expected UserID %s, got %s", tc.expectedUser, got.UserID)
				}
			} else {
				if got != nil {
					t.Fatalf("expected miss for %s, got %+v", tc.lookupToken, got)
				}
			}
		})
	}
}

func TestSessionCacheMutationIsolation(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	sess := makeTestSession("user-iso", "hash-iso", "asia")
	setCachedSession("hash-iso", sess, sessionCacheGeneration())

	got1 := getCachedSession("hash-iso")
	if got1 == nil {
		t.Fatalf("expected session to be cached")
	}

	// Mutate returned session fields
	got1.UserID = "mutated-user"
	got1.User.DisplayName = "mutated-name"
	*got1.User.ProfileImageID = "mutated-image"
	got1.User.ImageKey[0] = 0xff

	// Fetch again from cache
	got2 := getCachedSession("hash-iso")
	if got2 == nil {
		t.Fatalf("expected session to still be cached")
	}
	if got2.UserID != "user-iso" {
		t.Fatalf("cached session UserID was mutated: got %s, want user-iso", got2.UserID)
	}
	if got2.User.DisplayName != "Display user-iso" {
		t.Fatalf("cached session DisplayName was mutated: got %s, want Display user-iso", got2.User.DisplayName)
	}
	if *got2.User.ProfileImageID != "img-user-iso" {
		t.Fatalf("cached session ProfileImageID was mutated: got %s, want img-user-iso", *got2.User.ProfileImageID)
	}
	if bytes.Equal(got2.User.ImageKey, got1.User.ImageKey) {
		t.Fatalf("cached session ImageKey was mutated")
	}
}

func TestSessionCacheTTLExpiry(t *testing.T) {
	origTTL := sessionCacheTTL
	sessionCacheTTL = 2 * time.Second
	defer func() { sessionCacheTTL = origTTL }()

	InvalidateAllSessions()
	defer InvalidateAllSessions()

	origNow := sessionCacheNow
	defer func() { sessionCacheNow = origNow }()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	currentTime := baseTime
	sessionCacheNow = func() time.Time {
		return currentTime
	}

	token := "ttl-hash"
	setCachedSession(token, makeTestSession("user-ttl", token, "asia"), sessionCacheGeneration())

	tests := []struct {
		name      string
		timeShift time.Duration
		expectHit bool
	}{
		{
			name:      "immediately at t=0s",
			timeShift: 0,
			expectHit: true,
		},
		{
			name:      "after 1s (under TTL)",
			timeShift: 1 * time.Second,
			expectHit: true,
		},
		{
			name:      "after 2s exactly (boundary)",
			timeShift: 2 * time.Second,
			expectHit: true,
		},
		{
			name:      "after 2s + 1ms (expired)",
			timeShift: 2*time.Second + time.Millisecond,
			expectHit: false,
		},
		{
			name:      "after 5s (expired)",
			timeShift: 5 * time.Second,
			expectHit: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			currentTime = baseTime.Add(tc.timeShift)
			got := getCachedSession(token)
			if tc.expectHit && got == nil {
				t.Fatalf("expected cache hit at %v, got nil", tc.timeShift)
			}
			if !tc.expectHit && got != nil {
				t.Fatalf("expected cache miss at %v, got %+v", tc.timeShift, got)
			}
		})
	}
}

func TestSessionCacheInvalidateSessionsForUser(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	// Setup: User A has 2 sessions, User B has 1 session, User C has 1 session
	setCachedSession("token-a1", makeTestSession("user-a", "token-a1", "asia"), sessionCacheGeneration())
	setCachedSession("token-a2", makeTestSession("user-a", "token-a2", "asia"), sessionCacheGeneration())
	setCachedSession("token-b1", makeTestSession("user-b", "token-b1", "europe"), sessionCacheGeneration())
	setCachedSession("token-c1", makeTestSession("user-c", "token-c1", "america"), sessionCacheGeneration())

	tests := []struct {
		name         string
		action       func()
		checkToken   string
		expectHit    bool
		expectedUser string
	}{
		{
			name:         "before invalidation: token-a1 hit",
			action:       func() {},
			checkToken:   "token-a1",
			expectHit:    true,
			expectedUser: "user-a",
		},
		{
			name: "invalidate user-a",
			action: func() {
				InvalidateSessionsForUser("user-a")
			},
			checkToken: "token-a1",
			expectHit:  false,
		},
		{
			name:       "token-a2 also dropped",
			action:     func() {},
			checkToken: "token-a2",
			expectHit:  false,
		},
		{
			name:         "token-b1 still hits",
			action:       func() {},
			checkToken:   "token-b1",
			expectHit:    true,
			expectedUser: "user-b",
		},
		{
			name:         "token-c1 still hits",
			action:       func() {},
			checkToken:   "token-c1",
			expectHit:    true,
			expectedUser: "user-c",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.action()
			got := getCachedSession(tc.checkToken)
			if tc.expectHit {
				if got == nil {
					t.Fatalf("expected hit for %s, got nil", tc.checkToken)
				}
				if got.UserID != tc.expectedUser {
					t.Fatalf("expected UserID %s, got %s", tc.expectedUser, got.UserID)
				}
			} else {
				if got != nil {
					t.Fatalf("expected miss for %s, got %+v", tc.checkToken, got)
				}
			}
		})
	}
}

func TestSessionCacheInvalidateAllSessions(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	setCachedSession("token-x1", makeTestSession("user-x", "token-x1", "asia"), sessionCacheGeneration())
	setCachedSession("token-y1", makeTestSession("user-y", "token-y1", "europe"), sessionCacheGeneration())

	if getCachedSession("token-x1") == nil || getCachedSession("token-y1") == nil {
		t.Fatalf("precondition failed: sessions should be cached")
	}

	InvalidateAllSessions()

	tests := []struct {
		name       string
		checkToken string
	}{
		{name: "token-x1 invalidated", checkToken: "token-x1"},
		{name: "token-y1 invalidated", checkToken: "token-y1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getCachedSession(tc.checkToken); got != nil {
				t.Fatalf("expected miss for %s after InvalidateAllSessions, got %+v", tc.checkToken, got)
			}
		})
	}
}

func TestSessionCacheCapacityBound(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	sessionCacheMu.Lock()
	// Artificially populate up to max entries
	for i := range sessionCacheMaxEntries {
		key := fmt.Sprintf("tok-%d", i)
		sessionCacheByTokenHash[key] = sessionCacheEntry{
			session:  &Session{UserID: "u"},
			cachedAt: time.Now(),
		}
	}
	sessionCacheMu.Unlock()

	// Next store should trigger clearAllSessionsLocked
	setCachedSession("overflow-token", makeTestSession("user-overflow", "overflow-token", "asia"), sessionCacheGeneration())

	sessionCacheMu.RLock()
	count := len(sessionCacheByTokenHash)
	sessionCacheMu.RUnlock()

	if count != 1 {
		t.Fatalf("expected cache to clear on capacity reached and have 1 entry, got %d", count)
	}
	if got := getCachedSession("overflow-token"); got == nil || got.UserID != "user-overflow" {
		t.Fatalf("expected overflow token to be stored, got %+v", got)
	}
	if got := getCachedSession("tok-0"); got != nil {
		t.Fatalf("expected previous entries to be cleared, got %+v", got)
	}
}

func TestSessionCacheGenerationRace(t *testing.T) {
	InvalidateAllSessions()
	defer InvalidateAllSessions()

	token := "race-hash"
	sess := makeTestSession("user-race", token, "asia")

	// Simulate request A reading generation before DB query
	gen := sessionCacheGeneration()

	// Logout B commits and invalidates sessions for user-race
	InvalidateSessionsForUser("user-race")

	// Request A completes DB query and attempts to store with stale generation
	setCachedSession(token, sess, gen)

	// Lookup must miss because the store was skipped due to generation mismatch
	if got := getCachedSession(token); got != nil {
		t.Fatalf("expected cache miss due to generation mismatch, got %+v", got)
	}

	// InvalidateAllSessions also increments generation
	gen2 := sessionCacheGeneration()
	InvalidateAllSessions()
	setCachedSession("race-hash-2", sess, gen2)
	if got := getCachedSession("race-hash-2"); got != nil {
		t.Fatalf("expected cache miss due to generation mismatch after InvalidateAllSessions, got %+v", got)
	}
}
