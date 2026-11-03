package isureal

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
)

func TestImageMetaCache_TTL(t *testing.T) {
	origTTL := imageMetaCacheTTL
	imageMetaCacheTTL = 1500 * time.Millisecond
	defer func() { imageMetaCacheTTL = origTTL }()

	origNow := imageMetaCacheNow
	defer func() { imageMetaCacheNow = origNow }()

	InvalidateAllImageMeta()

	baseTime := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	imageMetaCacheNow = func() time.Time { return baseTime }

	gen := imageMetaCacheGeneration()
	entry := cachedImageMeta{
		OwnerUserID: "user_ttl",
		SigningKey:  []byte("ttl_signing_key"),
		MediaType:   "image/jpeg",
		CreatedAt:   baseTime,
	}

	setCachedImageMeta("img_ttl", entry, gen)

	// Hit immediately
	got, ok := getCachedImageMeta("img_ttl")
	if !ok {
		t.Fatalf("expected cache hit immediately, got miss")
	}
	if got.OwnerUserID != "user_ttl" || got.MediaType != "image/jpeg" {
		t.Fatalf("unexpected cached entry: %+v", got)
	}

	// Hit within 1.5s TTL (at 1400ms)
	imageMetaCacheNow = func() time.Time { return baseTime.Add(1400 * time.Millisecond) }
	_, ok = getCachedImageMeta("img_ttl")
	if !ok {
		t.Fatalf("expected cache hit at 1400ms (< 1500ms TTL), got miss")
	}

	// Miss after 1.5s TTL (at 1501ms)
	imageMetaCacheNow = func() time.Time { return baseTime.Add(1501 * time.Millisecond) }
	_, ok = getCachedImageMeta("img_ttl")
	if ok {
		t.Fatalf("expected cache miss at 1501ms (> 1500ms TTL), got hit")
	}

	// Miss if clock goes backwards
	imageMetaCacheNow = func() time.Time { return baseTime.Add(-100 * time.Millisecond) }
	_, ok = getCachedImageMeta("img_ttl")
	if ok {
		t.Fatalf("expected cache miss when clock went backwards, got hit")
	}
}

func TestImageMetaCache_GenerationGuard(t *testing.T) {
	origNow := imageMetaCacheNow
	defer func() { imageMetaCacheNow = origNow }()
	imageMetaCacheNow = time.Now

	InvalidateAllImageMeta()

	// Case 1: Stale set after InvalidateImageMetaForUser
	genUser := imageMetaCacheGeneration()
	InvalidateImageMetaForUser("user_gen_1")

	entry1 := cachedImageMeta{
		OwnerUserID: "user_gen_1",
		SigningKey:  []byte("key1"),
		MediaType:   "image/png",
		CreatedAt:   time.Now(),
	}
	setCachedImageMeta("img_gen_1", entry1, genUser)
	if _, ok := getCachedImageMeta("img_gen_1"); ok {
		t.Fatalf("expected stale setCachedImageMeta to be rejected after InvalidateImageMetaForUser")
	}

	// Case 2: Stale set after InvalidateAllImageMeta
	genAll := imageMetaCacheGeneration()
	InvalidateAllImageMeta()

	entry2 := cachedImageMeta{
		OwnerUserID: "user_gen_2",
		SigningKey:  []byte("key2"),
		MediaType:   "image/png",
		CreatedAt:   time.Now(),
	}
	setCachedImageMeta("img_gen_2", entry2, genAll)
	if _, ok := getCachedImageMeta("img_gen_2"); ok {
		t.Fatalf("expected stale setCachedImageMeta to be rejected after InvalidateAllImageMeta")
	}
}

func TestImageMetaCache_InvalidateSessionsForUserWithoutLoginSession(t *testing.T) {
	origNow := imageMetaCacheNow
	defer func() { imageMetaCacheNow = origNow }()
	imageMetaCacheNow = time.Now

	InvalidateAllSessions()

	userWithoutSession := "user_no_session_123"
	gen := imageMetaCacheGeneration()
	entry := cachedImageMeta{
		OwnerUserID: userWithoutSession,
		SigningKey:  []byte("some_key"),
		MediaType:   "image/jpeg",
		CreatedAt:   time.Now(),
	}

	setCachedImageMeta("img_no_session", entry, gen)
	if _, ok := getCachedImageMeta("img_no_session"); !ok {
		t.Fatalf("expected cache hit after setting entry")
	}

	// InvalidateSessionsForUser must invalidate image meta even without an active session
	InvalidateSessionsForUser(userWithoutSession)

	if _, ok := getCachedImageMeta("img_no_session"); ok {
		t.Fatalf("expected image meta to be invalidated by InvalidateSessionsForUser")
	}
}

func TestImageMetaCache_KillSwitch(t *testing.T) {
	origNow := imageMetaCacheNow
	defer func() { imageMetaCacheNow = origNow }()
	imageMetaCacheNow = time.Now

	InvalidateAllImageMeta()

	entry := cachedImageMeta{
		OwnerUserID: "user_kill",
		SigningKey:  []byte("kill_key"),
		MediaType:   "image/jpeg",
		CreatedAt:   time.Now(),
	}

	// 1. Kill-switch disables setCachedImageMeta
	t.Setenv("ISUREAL_DISABLE_IMAGE_META_CACHE", "1")
	gen := imageMetaCacheGeneration()
	setCachedImageMeta("img_kill_1", entry, gen)
	if _, ok := getCachedImageMeta("img_kill_1"); ok {
		t.Fatalf("expected cache miss when ISUREAL_DISABLE_IMAGE_META_CACHE=1")
	}

	// 2. Kill-switch disables getCachedImageMeta for already cached entry
	t.Setenv("ISUREAL_DISABLE_IMAGE_META_CACHE", "")
	gen = imageMetaCacheGeneration()
	setCachedImageMeta("img_kill_2", entry, gen)
	if _, ok := getCachedImageMeta("img_kill_2"); !ok {
		t.Fatalf("expected cache hit when kill-switch is inactive")
	}

	t.Setenv("ISUREAL_DISABLE_IMAGE_META_CACHE", "1")
	if _, ok := getCachedImageMeta("img_kill_2"); ok {
		t.Fatalf("expected getCachedImageMeta to return false when ISUREAL_DISABLE_IMAGE_META_CACHE=1")
	}
}

func TestImageMetaCache_SingleflightCoalescing(t *testing.T) {
	const concurrency = 20
	var loadCalls atomic.Int64
	var ready sync.WaitGroup
	var start = make(chan struct{})
	var done sync.WaitGroup

	ready.Add(concurrency)
	done.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start

			meta, err := singleflightLoadImageMeta("coalesce-img-id", func() (loadedImageMeta, error) {
				loadCalls.Add(1)
				time.Sleep(30 * time.Millisecond)
				return loadedImageMeta{
					OwnerUserID: "user_sf",
					SigningKey:  []byte("sf_signing_key"),
					MediaType:   "image/png",
					CreatedAt:   time.Now(),
					AliveKind:   2,
				}, nil
			})
			if err != nil {
				t.Errorf("singleflight returned unexpected error: %v", err)
			}
			if meta.OwnerUserID != "user_sf" || meta.MediaType != "image/png" || meta.AliveKind != 2 {
				t.Errorf("singleflight returned unexpected meta: %+v", meta)
			}
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	if calls := loadCalls.Load(); calls != 1 {
		t.Fatalf("expected exactly 1 loader invocation from %d concurrent callers, got %d", concurrency, calls)
	}
}

func TestImageMetaCache_HTTPHandlerCachedHitValidation(t *testing.T) {
	origNow := imageMetaCacheNow
	defer func() { imageMetaCacheNow = origNow }()
	imageMetaCacheNow = time.Now

	InvalidateAllImageMeta()
	t.Setenv("ISUREAL_DISABLE_IMAGE_META_CACHE", "")

	signingKey := []byte("secret-key-32-bytes-long-padding!!")
	imageID := "test-cached-post-image"
	gen := imageMetaCacheGeneration()
	setCachedImageMeta(imageID, cachedImageMeta{
		OwnerUserID: "user_handler_test",
		SigningKey:  signingKey,
		MediaType:   "image/jpeg",
		CreatedAt:   time.Now().Add(-1 * time.Hour),
	}, gen)

	h := &HandlerContext{} // nil DB to guarantee cache hit does not touch DB
	handler := getImageHandler(h)
	e := echo.New()

	// 1. Invalid signature on cache hit -> 403 image_forbidden and c.Get("abuse") == "invalid_signature"
	futureExp := time.Now().Add(10 * time.Minute).Unix()
	futureExpStr := strconv.FormatInt(futureExp, 10)
	invalidSigBytes := sha256.Sum256([]byte("wrong-signature-bytes"))
	invalidSigStr := base64.RawURLEncoding.EncodeToString(invalidSigBytes[:])

	reqInvalid := httptest.NewRequest(http.MethodGet, "/api/images/"+imageID+"?exp="+futureExpStr+"&sig="+invalidSigStr, nil)
	recInvalid := httptest.NewRecorder()
	cInvalid := e.NewContext(reqInvalid, recInvalid)
	cInvalid.SetPathValues(echo.PathValues{{Name: "imageId", Value: imageID}})

	errInvalid := handler(cInvalid)
	if errInvalid == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
	var apiErr *APIError
	if !errors.As(errInvalid, &apiErr) {
		t.Fatalf("expected APIError, got %T: %v", errInvalid, errInvalid)
	}
	if apiErr.Status != http.StatusForbidden || apiErr.Code != "image_forbidden" {
		t.Errorf("expected 403 image_forbidden, got %d %s", apiErr.Status, apiErr.Code)
	}
	if abuse := cInvalid.Get("abuse"); abuse != "invalid_signature" {
		t.Errorf("expected c.Get(\"abuse\") == \"invalid_signature\", got %v", abuse)
	}

	// 2. Expired exp -> 403 image_forbidden
	pastExp := time.Now().Add(-10 * time.Minute).Unix()
	pastExpStr := strconv.FormatInt(pastExp, 10)
	validPastSig := imageSignature(signingKey, imageID, pastExp)
	validPastSigStr := base64.RawURLEncoding.EncodeToString(validPastSig)

	reqExpired := httptest.NewRequest(http.MethodGet, "/api/images/"+imageID+"?exp="+pastExpStr+"&sig="+validPastSigStr, nil)
	recExpired := httptest.NewRecorder()
	cExpired := e.NewContext(reqExpired, recExpired)
	cExpired.SetPathValues(echo.PathValues{{Name: "imageId", Value: imageID}})

	errExpired := handler(cExpired)
	if errExpired == nil {
		t.Fatal("expected error for expired exp, got nil")
	}
	if !errors.As(errExpired, &apiErr) {
		t.Fatalf("expected APIError for expired exp, got %T: %v", errExpired, errExpired)
	}
	if apiErr.Status != http.StatusForbidden || apiErr.Code != "image_forbidden" {
		t.Errorf("expected 403 image_forbidden, got %d %s", apiErr.Status, apiErr.Code)
	}
}
