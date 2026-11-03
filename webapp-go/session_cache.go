package isureal

import (
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sessionCacheMaxEntries = 200000
)

var sessionCacheTTL = func() time.Duration {
	if v := os.Getenv("ISUREAL_SESSION_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}()

var sessionCacheNow = time.Now

type sessionCacheEntry struct {
	session  *Session
	cachedAt time.Time
}

var (
	sessionCacheMu          sync.RWMutex
	sessionCacheByTokenHash = make(map[string]sessionCacheEntry)
	sessionCacheByUserID    = make(map[string]map[string]struct{})
	sessionCacheGen         uint64
)

func sessionCacheGeneration() uint64 {
	return atomic.LoadUint64(&sessionCacheGen)
}

func copySession(s *Session) *Session {
	if s == nil {
		return nil
	}
	cp := *s
	if s.User.ProfileImageID != nil {
		pid := *s.User.ProfileImageID
		cp.User.ProfileImageID = &pid
	}
	if s.User.ImageKey != nil {
		ik := make([]byte, len(s.User.ImageKey))
		copy(ik, s.User.ImageKey)
		cp.User.ImageKey = ik
	}
	return &cp
}

func getCachedSession(tokenHash string) *Session {
	if tokenHash == "" {
		return nil
	}
	now := sessionCacheNow()

	sessionCacheMu.RLock()
	entry, ok := sessionCacheByTokenHash[tokenHash]
	sessionCacheMu.RUnlock()

	if !ok {
		return nil
	}
	if now.Sub(entry.cachedAt) > sessionCacheTTL || now.Before(entry.cachedAt) {
		return nil
	}
	return copySession(entry.session)
}

func setCachedSession(tokenHash string, sess *Session, gen uint64) {
	if tokenHash == "" || sess == nil {
		return
	}
	if sessionCacheGeneration() != gen {
		return
	}
	cp := copySession(sess)
	now := sessionCacheNow()

	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()

	if atomic.LoadUint64(&sessionCacheGen) != gen {
		return
	}

	if len(sessionCacheByTokenHash) >= sessionCacheMaxEntries {
		clearAllSessionsLocked()
	}

	if old, exists := sessionCacheByTokenHash[tokenHash]; exists && old.session != nil {
		if old.session.UserID != sess.UserID {
			if tokens, ok := sessionCacheByUserID[old.session.UserID]; ok {
				delete(tokens, tokenHash)
				if len(tokens) == 0 {
					delete(sessionCacheByUserID, old.session.UserID)
				}
			}
		}
	}

	sessionCacheByTokenHash[tokenHash] = sessionCacheEntry{
		session:  cp,
		cachedAt: now,
	}

	tokens, ok := sessionCacheByUserID[sess.UserID]
	if !ok {
		tokens = make(map[string]struct{})
		sessionCacheByUserID[sess.UserID] = tokens
	}
	tokens[tokenHash] = struct{}{}
}

func InvalidateSessionsForUser(userID string) {
	if userID == "" {
		return
	}
	InvalidateImageMetaForUser(userID)
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()
	atomic.AddUint64(&sessionCacheGen, 1)

	tokens, ok := sessionCacheByUserID[userID]
	if !ok {
		return
	}
	for tokenHash := range tokens {
		delete(sessionCacheByTokenHash, tokenHash)
	}
	delete(sessionCacheByUserID, userID)
}

func InvalidateAllSessions() {
	InvalidateAllImageMeta()
	sessionCacheMu.Lock()
	defer sessionCacheMu.Unlock()
	atomic.AddUint64(&sessionCacheGen, 1)
	clearAllSessionsLocked()
}

func clearAllSessionsLocked() {
	sessionCacheByTokenHash = make(map[string]sessionCacheEntry)
	sessionCacheByUserID = make(map[string]map[string]struct{})
}
