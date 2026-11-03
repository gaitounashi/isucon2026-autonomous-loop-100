package isureal

import (
	"net/http"
	"testing"
	"time"
)

func seedImageMeta(t *testing.T, imageID, ownerID string) {
	t.Helper()
	setCachedImageMeta(imageID, cachedImageMeta{
		OwnerUserID: ownerID,
		SigningKey:  []byte("key_" + ownerID),
		MediaType:   "image/jpeg",
		CreatedAt:   time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}, imageMetaCacheGeneration())
	if _, ok := getCachedImageMeta(imageID); !ok {
		t.Fatalf("seed failed for %s", imageID)
	}
}

func TestImageMetaCache_DefaultTTLIsLong(t *testing.T) {
	if imageMetaCacheTTL < 30*time.Second {
		t.Fatalf("expected long default TTL backed by NOTIFY invalidation, got %v", imageMetaCacheTTL)
	}
}

func TestInvalidateImageMeta_RemovesOnlyTarget(t *testing.T) {
	InvalidateAllImageMeta()
	defer InvalidateAllImageMeta()

	seedImageMeta(t, "img_a1", "owner_a")
	seedImageMeta(t, "img_a2", "owner_a")
	seedImageMeta(t, "img_b1", "owner_b")

	InvalidateImageMeta("img_a1")

	if _, ok := getCachedImageMeta("img_a1"); ok {
		t.Fatalf("img_a1 should be invalidated")
	}
	if _, ok := getCachedImageMeta("img_a2"); !ok {
		t.Fatalf("img_a2 should remain cached")
	}
	if _, ok := getCachedImageMeta("img_b1"); !ok {
		t.Fatalf("img_b1 should remain cached")
	}

	// The per-user index must stay consistent: invalidating owner_a drops img_a2.
	InvalidateImageMetaForUser("owner_a")
	if _, ok := getCachedImageMeta("img_a2"); ok {
		t.Fatalf("img_a2 should be invalidated with its owner")
	}
	if _, ok := getCachedImageMeta("img_b1"); !ok {
		t.Fatalf("img_b1 should remain cached")
	}

	// Unknown / empty IDs are no-ops.
	InvalidateImageMeta("")
	InvalidateImageMeta("img_unknown")
	if _, ok := getCachedImageMeta("img_b1"); !ok {
		t.Fatalf("img_b1 should remain cached")
	}
}

func TestInvalidateImageMeta_BlocksInflightLoad(t *testing.T) {
	InvalidateAllImageMeta()
	defer InvalidateAllImageMeta()

	// A load that started before the invalidation must not repopulate the cache.
	gen := imageMetaCacheGeneration()
	InvalidateImageMeta("img_inflight")
	setCachedImageMeta("img_inflight", cachedImageMeta{
		OwnerUserID: "owner_x",
		SigningKey:  []byte("k"),
		MediaType:   "image/jpeg",
	}, gen)
	if _, ok := getCachedImageMeta("img_inflight"); ok {
		t.Fatalf("stale in-flight load should not be cached after invalidation")
	}
}

func TestHandleImageMetaNotify(t *testing.T) {
	InvalidateAllImageMeta()
	defer InvalidateAllImageMeta()

	seedImageMeta(t, "img_p", "author")
	seedImageMeta(t, "img_r1", "reactor1")
	seedImageMeta(t, "img_r2", "reactor2")
	seedImageMeta(t, "img_keep", "other")

	HandleImageMetaNotify("img_p,img_r1,img_r2")
	for _, id := range []string{"img_p", "img_r1", "img_r2"} {
		if _, ok := getCachedImageMeta(id); ok {
			t.Fatalf("%s should be invalidated by list payload", id)
		}
	}
	if _, ok := getCachedImageMeta("img_keep"); !ok {
		t.Fatalf("img_keep should remain cached")
	}

	HandleImageMetaNotify("*")
	if _, ok := getCachedImageMeta("img_keep"); ok {
		t.Fatalf("'*' payload should clear everything")
	}

	seedImageMeta(t, "img_empty", "other")
	HandleImageMetaNotify("")
	if _, ok := getCachedImageMeta("img_empty"); ok {
		t.Fatalf("empty payload should clear everything")
	}
}

// Same-server writes must invalidate synchronously (NOTIFY only arrives after commit).
// Runs against the real handlers when ISUREAL_TEST_DB=1.
func TestImageMeta_ReplacedProfileImageIsForbiddenImmediately(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("img_meta_cache_profile")

	putProfile := func() string {
		t.Helper()
		if rec := mediaPutProfileImage(env, user.Token, tinyJPEG()); rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for PUT profile image, got %d: %s", rec.Code, rec.Body.String())
		}
		me := decodeJSON[struct {
			ProfileImageURL *string `json:"profileImageUrl"`
		}](t, env.do(http.MethodGet, "/api/me", user.Token, nil))
		if me.ProfileImageURL == nil || *me.ProfileImageURL == "" {
			t.Fatalf("expected profileImageUrl")
		}
		return *me.ProfileImageURL
	}

	oldURL := putProfile()
	if rec := env.do(http.MethodGet, oldURL, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for current profile image (warms cache), got %d", rec.Code)
	}
	_ = putProfile()
	if rec := env.do(http.MethodGet, oldURL, "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for replaced profile image, got %d", rec.Code)
	}
}
