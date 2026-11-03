package isureal

import (
	"net/http"
	"testing"
)

// 026-unlogged-active-images.sql: run images go to the UNLOGGED active_image_objects table,
// are served from it, and are removed from it when the image is deleted.
func TestActiveImageObjects_UnloggedRedirectServeAndDelete(t *testing.T) {
	env := newTestEnv(t)

	var persistence string
	if err := env.DB.Get(&persistence, `SELECT relpersistence::text FROM pg_class WHERE relname = 'active_image_objects'`); err != nil {
		t.Fatalf("active_image_objects not found: %v", err)
	}
	if persistence != "u" {
		t.Fatalf("expected active_image_objects to be UNLOGGED, got relpersistence=%q", persistence)
	}

	user := env.registerUser("active_img_user")
	profileImageID := func() string {
		t.Helper()
		var id *string
		if err := env.DB.Get(&id, `SELECT profile_image_id FROM users WHERE id = $1`, user.ID); err != nil || id == nil {
			t.Fatalf("profile_image_id not set: %v", err)
		}
		return *id
	}
	countIn := func(table, id string) int {
		t.Helper()
		var n int
		if err := env.DB.Get(&n, `SELECT count(*) FROM `+table+` WHERE id = $1`, id); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}

	if rec := mediaPutProfileImage(env, user.Token, tinyJPEG()); rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for PUT profile image, got %d: %s", rec.Code, rec.Body.String())
	}
	firstID := profileImageID()
	if countIn("active_image_objects", firstID) != 1 || countIn("image_objects", firstID) != 0 {
		t.Fatalf("expected image %s only in active_image_objects", firstID)
	}

	// Served through the active-first read query (bypass the local file cache).
	var data []byte
	if err := env.DB.Get(&data, `
		(SELECT data FROM active_image_objects WHERE id = $1)
		UNION ALL
		(SELECT data FROM image_objects WHERE id = $1 AND created_at = now())
		LIMIT 1
	`, firstID); err != nil || len(data) == 0 {
		t.Fatalf("expected image data from active_image_objects: %v", err)
	}
	me := decodeJSON[struct {
		ProfileImageURL *string `json:"profileImageUrl"`
	}](t, env.do(http.MethodGet, "/api/me", user.Token, nil))
	if me.ProfileImageURL == nil {
		t.Fatalf("expected profileImageUrl")
	}
	if rec := env.do(http.MethodGet, *me.ProfileImageURL, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for profile image, got %d: %s", rec.Code, rec.Body.String())
	}

	// Replacing the profile image deletes the old image (image_meta DELETE -> active row DELETE).
	if rec := mediaPutProfileImage(env, user.Token, tinyJPEG()); rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for second PUT profile image, got %d: %s", rec.Code, rec.Body.String())
	}
	secondID := profileImageID()
	if secondID == firstID {
		t.Fatalf("expected a new profile image id")
	}
	if countIn("active_image_objects", firstID) != 0 {
		t.Fatalf("expected old image %s to be deleted from active_image_objects", firstID)
	}
	if countIn("active_image_objects", secondID) != 1 {
		t.Fatalf("expected new image %s in active_image_objects", secondID)
	}
}
