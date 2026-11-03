package isureal

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func doWithHeader(env *testEnv, method, path, token, headerKey, headerVal string, body []byte) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if headerKey != "" && headerVal != "" {
		req.Header.Set(headerKey, headerVal)
	}
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func TestPostingWindowConditionalRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	env := newTestEnv(t)
	user := env.registerUserInRegion("pw_cond_user", "asia")

	// 1. Initial request without If-None-Match: should return 200 OK with ETag
	rec1 := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec1.Code)
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header, got empty")
	}

	// 2. Request with matching If-None-Match: should return 304 Not Modified
	rec2 := doWithHeader(env, http.MethodGet, "/api/posting-window/current", user.Token, "If-None-Match", etag, nil)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("expected empty body for 304, got %d bytes", rec2.Body.Len())
	}

	// 3. Request with mismatched If-None-Match: should return 200 OK
	rec3 := doWithHeader(env, http.MethodGet, "/api/posting-window/current", user.Token, "If-None-Match", `"mismatched-etag"`, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec3.Code)
	}
}

func TestImageConditionalRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	env := newTestEnv(t)
	user := env.registerUser("img_cond_user")

	// Upload profile image to get a valid image ID and signed URL
	req := httptest.NewRequest(http.MethodPut, "/api/me/profile-image", bytes.NewReader(tinyJPEG()))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "image/jpeg")
	req.RemoteAddr = "192.0.2.1:12345"
	putRec := httptest.NewRecorder()
	env.E.ServeHTTP(putRec, req)
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 NoContent on profile image upload, got %d: %s", putRec.Code, putRec.Body.String())
	}

	// Get /api/me to obtain profileImageUrl
	meRec := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if meRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on /api/me, got %d", meRec.Code)
	}
	meRes := decodeJSON[getMeResponse](t, meRec)
	if meRes.ProfileImageURL == nil || *meRes.ProfileImageURL == "" {
		t.Fatal("expected profileImageUrl, got nil or empty")
	}

	imageURL := *meRes.ProfileImageURL
	// 1. Initial image request: should return 200 OK with ETag
	imgRec1 := env.do(http.MethodGet, imageURL, "", nil)
	if imgRec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on image GET, got %d: %s", imgRec1.Code, imgRec1.Body.String())
	}
	etag := imgRec1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header on image response, got empty")
	}

	// 2. Image request with matching If-None-Match: should return 304 Not Modified
	imgRec2 := doWithHeader(env, http.MethodGet, imageURL, "", "If-None-Match", etag, nil)
	if imgRec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", imgRec2.Code)
	}
	if imgRec2.Body.Len() != 0 {
		t.Fatalf("expected empty body for 304 image, got %d bytes", imgRec2.Body.Len())
	}

	// 3. Image request with unquoted matching ETag: should also return 304
	imgRec3 := doWithHeader(env, http.MethodGet, imageURL, "", "If-None-Match", strings.Trim(etag, "\""), nil)
	if imgRec3.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified for unquoted ETag, got %d", imgRec3.Code)
	}

	// 4. Image request with mismatched If-None-Match: should return 200 OK
	imgRec4 := doWithHeader(env, http.MethodGet, imageURL, "", "If-None-Match", `"wrong-image-id"`, nil)
	if imgRec4.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for mismatched If-None-Match, got %d", imgRec4.Code)
	}
}

func TestMyCurrentPostConditionalRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	env := newTestEnv(t)
	user := env.registerUserInRegion("mypost_cond_user", "asia")
	env.openPostingWindow()
	env.createPost(user)

	// 1. Initial request without If-None-Match: should return 200 OK with ETag
	rec1 := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec1.Code, rec1.Body.String())
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header on my current post, got empty")
	}

	// 2. Request with matching If-None-Match: should return 304 Not Modified
	rec2 := doWithHeader(env, http.MethodGet, "/api/posts/me/current", user.Token, "If-None-Match", etag, nil)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 Not Modified, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("expected empty body for 304, got %d bytes", rec2.Body.Len())
	}

	// 3. Request with mismatched If-None-Match: should return 200 OK
	rec3 := doWithHeader(env, http.MethodGet, "/api/posts/me/current", user.Token, "If-None-Match", `"wrong-post-id"`, nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec3.Code)
	}
}

func TestNotificationsZeroRTT(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	env := newTestEnv(t)
	user := env.registerUserInRegion("notif_zero_rtt", "asia")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)

	// Should successfully initiate event-stream and exit when ctx cancels
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /api/notifications, got %d", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", contentType)
	}
}
