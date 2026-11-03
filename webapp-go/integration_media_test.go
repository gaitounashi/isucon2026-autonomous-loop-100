package isureal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mediaSyncRecorder wraps httptest.ResponseRecorder to safely observe header flush.
type mediaSyncRecorder struct {
	*httptest.ResponseRecorder
	flushedCh chan struct{}
	once      sync.Once
}

func newMediaSyncRecorder() *mediaSyncRecorder {
	return &mediaSyncRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushedCh:        make(chan struct{}),
	}
}

func (r *mediaSyncRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.once.Do(func() {
		close(r.flushedCh)
	})
}

func mediaDoWithContext(env *testEnv, ctx context.Context, method, path, token string, body any) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	var contentType string

	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
		case string:
			bodyReader = strings.NewReader(v)
		case io.Reader:
			bodyReader = v
		default:
			data, err := json.Marshal(body)
			if err != nil {
				env.T.Fatalf("failed to marshal request body: %v", err)
			}
			bodyReader = bytes.NewReader(data)
			contentType = "application/json"
		}
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func mediaPutProfileImage(env *testEnv, token string, jpegData []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/me/profile-image", bytes.NewReader(jpegData))
	req.Header.Set("Content-Type", "image/jpeg")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func mediaCreateRealMoji(env *testEnv, user testUser, postID, reactionType string, imgBytes []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("reactionType", reactionType); err != nil {
		env.T.Fatalf("failed to write reactionType: %v", err)
	}
	part, err := writer.CreateFormFile("image", "reaction.jpg")
	if err != nil {
		env.T.Fatalf("failed to create realmoji form file: %v", err)
	}
	if _, err := part.Write(imgBytes); err != nil {
		env.T.Fatalf("failed to write realmoji image bytes: %v", err)
	}
	if err := writer.Close(); err != nil {
		env.T.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/posts/"+postID+"/realmojis", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if user.Token != "" {
		req.Header.Set("Authorization", "Bearer "+user.Token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func mediaParseSSENotification(raw string) (getNotificationsResponse, error) {
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var notif getNotificationsResponse
			if err := json.Unmarshal([]byte(payload), &notif); err != nil {
				return getNotificationsResponse{}, err
			}
			return notif, nil
		}
	}
	return getNotificationsResponse{}, errors.New("no SSE data found in response")
}

func TestIntegrationMedia_GetImage_PostImage_Valid(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("media_img_poster")
	postID := env.createPost(user)

	recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	if recPost.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET post, got %d: %s", recPost.Code, recPost.Body.String())
	}

	type postResp struct {
		ID       string `json:"id"`
		ImageURL string `json:"imageUrl"`
	}
	post := decodeJSON[postResp](t, recPost)
	if post.ImageURL == "" {
		t.Fatalf("expected non-empty post ImageURL")
	}

	// 1. GET without token (public image endpoint)
	recImg := env.do(http.MethodGet, post.ImageURL, "", nil)
	if recImg.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid signed post image, got %d: %s", recImg.Code, recImg.Body.String())
	}
	if ct := recImg.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %s", ct)
	}
	if cc := recImg.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %s", cc)
	}
	if !bytes.Equal(recImg.Body.Bytes(), tinyJPEG()) {
		t.Fatalf("image body does not match uploaded tinyJPEG bytes")
	}

	// 2. GET with token
	recImgWithToken := env.do(http.MethodGet, post.ImageURL, user.Token, nil)
	if recImgWithToken.Code != http.StatusOK {
		t.Fatalf("expected 200 for signed post image with token, got %d: %s", recImgWithToken.Code, recImgWithToken.Body.String())
	}
	if !bytes.Equal(recImgWithToken.Body.Bytes(), tinyJPEG()) {
		t.Fatalf("image body with token does not match uploaded tinyJPEG bytes")
	}
}

func TestIntegrationMedia_GetImage_ProfileImage_Valid(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("media_prof_user")

	putRec := mediaPutProfileImage(env, user.Token, tinyJPEG())
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for PUT profile image, got %d: %s", putRec.Code, putRec.Body.String())
	}

	meRec := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if meRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/me, got %d: %s", meRec.Code, meRec.Body.String())
	}

	type meResp struct {
		ProfileImageURL *string `json:"profileImageUrl"`
	}
	me := decodeJSON[meResp](t, meRec)
	if me.ProfileImageURL == nil || *me.ProfileImageURL == "" {
		t.Fatalf("expected non-empty profileImageUrl in /api/me response")
	}

	imgRec := env.do(http.MethodGet, *me.ProfileImageURL, "", nil)
	if imgRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for signed profile image URL, got %d: %s", imgRec.Code, imgRec.Body.String())
	}
	if ct := imgRec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %s", ct)
	}
	if cc := imgRec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %s", cc)
	}
	if !bytes.Equal(imgRec.Body.Bytes(), tinyJPEG()) {
		t.Fatalf("profile image body does not match uploaded tinyJPEG bytes")
	}
}

func TestIntegrationMedia_GetImage_TamperedSignature(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("media_tamper_user")
	postID := env.createPost(user)

	recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	var post struct {
		ImageURL string `json:"imageUrl"`
	}
	post = decodeJSON[struct {
		ImageURL string `json:"imageUrl"`
	}](t, recPost)

	u, err := url.Parse(post.ImageURL)
	if err != nil {
		t.Fatalf("failed to parse imageUrl: %v", err)
	}
	q := u.Query()
	origSig := q.Get("sig")
	if origSig == "" {
		t.Fatalf("expected sig in query params")
	}

	// Tamper signature by changing the last character
	tamperedSig := origSig[:len(origSig)-1]
	if origSig[len(origSig)-1] == 'a' {
		tamperedSig += "b"
	} else {
		tamperedSig += "a"
	}
	q.Set("sig", tamperedSig)
	u.RawQuery = q.Encode()

	rec := env.do(http.MethodGet, u.String(), "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for tampered signature, got %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control no-store on error, got %s", cc)
	}

	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "image_forbidden" {
		t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
	}
}

func TestIntegrationMedia_GetImage_ExpiredSignature(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("media_expired_user")
	postID := env.createPost(user)

	recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	var post struct {
		ImageURL string `json:"imageUrl"`
	}
	post = decodeJSON[struct {
		ImageURL string `json:"imageUrl"`
	}](t, recPost)

	u, err := url.Parse(post.ImageURL)
	if err != nil {
		t.Fatalf("failed to parse imageUrl: %v", err)
	}
	q := u.Query()
	// Set expiration in the past
	pastExp := time.Now().Unix() - 3600
	q.Set("exp", strconv.FormatInt(pastExp, 10))
	u.RawQuery = q.Encode()

	rec := env.do(http.MethodGet, u.String(), "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for expired signature, got %d: %s", rec.Code, rec.Body.String())
	}

	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "image_forbidden" {
		t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
	}
}

func TestIntegrationMedia_GetImage_MalformedQueryParams(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("media_malformed_user")
	postID := env.createPost(user)

	recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	var post struct {
		ImageURL string `json:"imageUrl"`
	}
	post = decodeJSON[struct {
		ImageURL string `json:"imageUrl"`
	}](t, recPost)

	u, err := url.Parse(post.ImageURL)
	if err != nil {
		t.Fatalf("failed to parse imageUrl: %v", err)
	}
	basePath := u.Path
	origExp := u.Query().Get("exp")
	origSig := u.Query().Get("sig")

	cases := []struct {
		name     string
		rawQuery string
	}{
		{"missing_exp", "sig=" + origSig},
		{"missing_sig", "exp=" + origExp},
		{"duplicate_exp", fmt.Sprintf("exp=%s&exp=%s&sig=%s", origExp, origExp, origSig)},
		{"duplicate_sig", fmt.Sprintf("exp=%s&sig=%s&sig=%s", origExp, origSig, origSig)},
		{"non_numeric_exp", "exp=notanumber&sig=" + origSig},
		{"leading_zero_exp", "exp=0" + origExp + "&sig=" + origSig},
		{"invalid_base64_sig", "exp=" + origExp + "&sig=!!invalid@base64!!"},
		{"short_sig", "exp=" + origExp + "&sig=YQ"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := basePath + "?" + tc.rawQuery
			rec := env.do(http.MethodGet, path, "", nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 Forbidden for %s, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
			errResp := decodeJSON[APIErrorResponse](t, rec)
			if errResp.Error != "image_forbidden" {
				t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
			}
		})
	}
}

func TestIntegrationMedia_GetImage_UnknownImage(t *testing.T) {
	env := newTestEnv(t)

	// An unknown image ID with syntactically valid future exp and 32-byte sig.
	unknownID := uuid.NewString()
	exp := strconv.FormatInt(time.Now().Unix()+3600, 10)
	dummySig := base64.RawURLEncoding.EncodeToString(make([]byte, 32))

	path := fmt.Sprintf("/api/images/%s?exp=%s&sig=%s", unknownID, exp, dummySig)
	rec := env.do(http.MethodGet, path, "", nil)

	// Handler returns 403 Forbidden ("image_forbidden") when image_meta row is not found.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for unknown image, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "image_forbidden" {
		t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
	}
}

func TestIntegrationMedia_GetImage_DeletedPost(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	// Subtest 1: User has a profile image set.
	// u.profile_image_id is non-null, so (u.profile_image_id = m.id) evaluates to false.
	// When post is deleted, is_alive is false and returns 403 Forbidden.
	t.Run("with_profile_image_set", func(t *testing.T) {
		user := env.registerUser("media_del_with_prof")
		putRec := mediaPutProfileImage(env, user.Token, tinyJPEG())
		if putRec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for PUT profile image, got %d: %s", putRec.Code, putRec.Body.String())
		}

		postID := env.createPost(user)
		recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
		var post struct {
			ImageURL string `json:"imageUrl"`
		}
		post = decodeJSON[struct {
			ImageURL string `json:"imageUrl"`
		}](t, recPost)

		// Verify image is accessible before deletion
		recBefore := env.do(http.MethodGet, post.ImageURL, "", nil)
		if recBefore.Code != http.StatusOK {
			t.Fatalf("expected 200 before post deletion, got %d", recBefore.Code)
		}

		// Delete post
		delRec := env.do(http.MethodDelete, "/api/posts/"+postID, user.Token, nil)
		if delRec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for DELETE post, got %d: %s", delRec.Code, delRec.Body.String())
		}

		// Subsequent GET returns 403 Forbidden
		recAfter := env.do(http.MethodGet, post.ImageURL, "", nil)
		if recAfter.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for image of deleted post, got %d: %s", recAfter.Code, recAfter.Body.String())
		}
		errResp := decodeJSON[APIErrorResponse](t, recAfter)
		if errResp.Error != "image_forbidden" {
			t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
		}
	})

	// Subtest 2: User has NO profile image set (u.profile_image_id IS NULL).
	// The initial implementation returns 403 image_forbidden here as well.
	t.Run("without_profile_image", func(t *testing.T) {
		user := env.registerUser("media_del_no_prof")
		postID := env.createPost(user)

		recPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
		var post struct {
			ImageURL string `json:"imageUrl"`
		}
		post = decodeJSON[struct {
			ImageURL string `json:"imageUrl"`
		}](t, recPost)

		delRec := env.do(http.MethodDelete, "/api/posts/"+postID, user.Token, nil)
		if delRec.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for DELETE post, got %d: %s", delRec.Code, delRec.Body.String())
		}

		recAfter := env.do(http.MethodGet, post.ImageURL, "", nil)
		if recAfter.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for image of deleted post, got %d: %s", recAfter.Code, recAfter.Body.String())
		}
		if got := decodeJSON[map[string]string](t, recAfter)["error"]; got != "image_forbidden" {
			t.Fatalf("expected error image_forbidden, got %q", got)
		}
	})
}

func TestIntegrationMedia_GetImage_DeletedProfileImage(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("media_del_prof_user")

	putRec := mediaPutProfileImage(env, user.Token, tinyJPEG())
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for PUT profile image, got %d: %s", putRec.Code, putRec.Body.String())
	}

	meRec := env.do(http.MethodGet, "/api/me", user.Token, nil)
	var me struct {
		ProfileImageURL *string `json:"profileImageUrl"`
	}
	me = decodeJSON[struct {
		ProfileImageURL *string `json:"profileImageUrl"`
	}](t, meRec)
	if me.ProfileImageURL == nil {
		t.Fatalf("expected profileImageUrl to be present")
	}

	// Verify image is accessible
	recBefore := env.do(http.MethodGet, *me.ProfileImageURL, "", nil)
	if recBefore.Code != http.StatusOK {
		t.Fatalf("expected 200 before profile image deletion, got %d", recBefore.Code)
	}

	// Delete profile image
	delRec := env.do(http.MethodDelete, "/api/me/profile-image", user.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for DELETE profile-image, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Subsequent GET to the old profile image URL should now be rejected
	recAfter := env.do(http.MethodGet, *me.ProfileImageURL, "", nil)
	if recAfter.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for deleted profile image, got %d: %s", recAfter.Code, recAfter.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, recAfter)
	if errResp.Error != "image_forbidden" {
		t.Fatalf("expected error code 'image_forbidden', got '%s'", errResp.Error)
	}
}

func TestIntegrationMedia_Notifications_AuthAndRegion(t *testing.T) {
	env := newTestEnv(t)

	// 1. GET without token -> 401
	recNoAuth := env.do(http.MethodGet, "/api/notifications", "", nil)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth token, got %d: %s", recNoAuth.Code, recNoAuth.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, recNoAuth)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected 'invalid_session', got '%s'", errResp.Error)
	}

	// 2. GET with invalid token -> 401
	recBadToken := env.do(http.MethodGet, "/api/notifications", "bad-token-12345", nil)
	if recBadToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with invalid token, got %d: %s", recBadToken.Code, recBadToken.Body.String())
	}

	// 3. User in different region ("america") -> 403 region_mismatch
	americaUser := env.registerUserInRegion("media_am_notif_user", "america")
	recRegion := env.do(http.MethodGet, "/api/notifications", americaUser.Token, nil)
	if recRegion.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for region mismatch, got %d: %s", recRegion.Code, recRegion.Body.String())
	}
	errRegion := decodeJSON[APIErrorResponse](t, recRegion)
	if errRegion.Error != "region_mismatch" {
		t.Fatalf("expected 'region_mismatch', got '%s'", errRegion.Error)
	}
}

func TestIntegrationMedia_Notifications_PostingWindowDeliveryAndShape(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("media_sse_user")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.RemoteAddr = "192.0.2.1:12345"

	rec := newMediaSyncRecorder()
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		env.E.ServeHTTP(rec, req)
	}()

	// Wait for SSE initial headers to flush (connection established)
	select {
	case <-rec.flushedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SSE connection to establish")
	}

	// Small pause so subscriber finishes registering in hub
	time.Sleep(30 * time.Millisecond)

	// Admin opens posting window in "asia"
	env.openPostingWindow()

	// Wait for handler to receive notification, write SSE event, and return
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for notification event delivery")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for notifications SSE, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}

	body := rec.Body.String()
	notif, err := mediaParseSSENotification(body)
	if err != nil {
		t.Fatalf("failed to parse SSE notification: %v, raw body: %q", err, body)
	}

	if notif.ID == "" {
		t.Fatalf("expected non-empty notification ID")
	}
	if notif.PostingWindowID == "" {
		t.Fatalf("expected non-empty postingWindowId")
	}
	if notif.Region != "asia" {
		t.Fatalf("expected notification region 'asia', got '%s'", notif.Region)
	}
	if notif.CreatedAt.IsZero() || time.Since(notif.CreatedAt) > 10*time.Second {
		t.Fatalf("expected reasonable createdAt timestamp, got %v", notif.CreatedAt)
	}
}

func TestIntegrationMedia_Notifications_EventsThatDoNotCreateNotifications(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user1 := env.registerUser("media_notif_target")
	user2 := env.registerUser("media_notif_actor")

	// Both create a post in current window so they are eligible to view & comment
	postID1 := env.createPost(user1)
	_ = env.createPost(user2)

	// Count existing notifications (created during openPostingWindow)
	var initialCount int
	if err := env.DB.Get(&initialCount, `SELECT count(*) FROM notifications WHERE user_id = $1`, user1.ID); err != nil {
		t.Fatalf("failed to count notifications: %v", err)
	}

	// user1 connects to /api/notifications with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+user1.Token)
	req.RemoteAddr = "192.0.2.1:12345"

	rec := newMediaSyncRecorder()
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		env.E.ServeHTTP(rec, req)
	}()

	select {
	case <-rec.flushedCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SSE connection to establish")
	}

	time.Sleep(20 * time.Millisecond)

	// 1. Friend request from user2 to user1
	frRec := env.do(http.MethodPost, "/api/friend-requests", user2.Token, map[string]string{
		"recipientId": user1.ID,
	})
	if frRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for friend request, got %d: %s", frRec.Code, frRec.Body.String())
	}

	// 2. Comment from user2 on user1's post
	cmRec := env.do(http.MethodPost, "/api/posts/"+postID1+"/comments", user2.Token, map[string]string{
		"body": "awesome post!",
	})
	if cmRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for comment, got %d: %s", cmRec.Code, cmRec.Body.String())
	}

	// 3. RealMoji reaction from user2 on user1's post
	rmRec := mediaCreateRealMoji(env, user2, postID1, "like", tinyJPEG())
	if rmRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for realmoji, got %d: %s", rmRec.Code, rmRec.Body.String())
	}

	// Wait for context timeout
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for SSE request to finish after context cancel")
	}

	// Verify no notification event was sent over the SSE connection
	body := rec.Body.String()
	if strings.Contains(body, "data:") {
		t.Fatalf("expected no SSE notification data for comments/realmojis/friend-requests, got: %s", body)
	}

	// Verify in DB that no new notification records were added for user1
	var finalCount int
	if err := env.DB.Get(&finalCount, `SELECT count(*) FROM notifications WHERE user_id = $1`, user1.ID); err != nil {
		t.Fatalf("failed to count notifications: %v", err)
	}
	if finalCount != initialCount {
		t.Fatalf("notifications table rows changed from %d to %d; expected no new notifications", initialCount, finalCount)
	}
}

func TestIntegrationMedia_Notifications_ContextCancellation(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("media_cancel_user")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rec := mediaDoWithContext(env, ctx, http.MethodGet, "/api/notifications", user.Token, nil)

	// Should exit cleanly without error
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK header before cancellation, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %s", ct)
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("expected no event data, got %s", rec.Body.String())
	}
}

func TestIntegrationMedia_Notifications_PreExistingPostingWindowNotDelivered(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("media_preexist_user")

	// Open posting window BEFORE subscriber connects
	env.openPostingWindow()

	// Connect with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	rec := mediaDoWithContext(env, ctx, http.MethodGet, "/api/notifications", user.Token, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	// Past notification is not delivered to late subscriber
	if strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("expected pre-existing posting window notification NOT to be delivered, got: %s", rec.Body.String())
	}
}
