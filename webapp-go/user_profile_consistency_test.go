package isureal

import (
	"net/http"
	"testing"
)

// GET /api/users/:userId must reflect writes made right before it, even when the
// target was fetched earlier (no per-server response caching).
func TestUserProfile_ReflectsWritesAfterPreviousFetch(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("profile_cons_viewer")
	target := env.registerUser("profile_cons_target")

	getTarget := func() *getUserResponse {
		t.Helper()
		rec := env.do(http.MethodGet, "/api/users/"+target.ID, viewer.Token, nil)
		if rec.Code == http.StatusNotFound {
			return nil
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 or 404, got %d: %s", rec.Code, rec.Body.String())
		}
		res := decodeJSON[getUserResponse](t, rec)
		return &res
	}

	if got := getTarget(); got == nil {
		t.Fatalf("expected target to be visible before any block")
	}

	// 1. Display name change is visible immediately.
	if rec := env.do(http.MethodPatch, "/api/me", target.Token, map[string]string{"displayName": "Renamed Target"}); rec.Code != http.StatusOK {
		t.Fatalf("profile update failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := getTarget(); got == nil || got.DisplayName != "Renamed Target" {
		t.Fatalf("expected renamed display name, got %+v", got)
	}

	// 2. Target blocks viewer -> 404 right away.
	if rec := env.do(http.MethodPut, "/api/blocks/"+viewer.ID, target.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("block failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := getTarget(); got != nil {
		t.Fatalf("expected 404 after target blocked viewer, got %+v", got)
	}

	// 3. Unblock -> visible again right away.
	if rec := env.do(http.MethodDelete, "/api/blocks/"+viewer.ID, target.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unblock failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := getTarget(); got == nil {
		t.Fatalf("expected target to be visible again after unblock")
	}
}
