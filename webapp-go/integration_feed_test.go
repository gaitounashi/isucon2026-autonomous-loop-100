package isureal

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Helper functions for integration_feed_test.go

func feedOpenWindow(t *testing.T, env *testEnv, localDate string) {
	t.Helper()
	token := env.adminToken()
	rec := env.do(http.MethodPost, "/api/admin/posting-window", token, map[string]any{
		"region":    "asia",
		"localDate": localDate,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedOpenWindow failed for %s (%d): %s", localDate, rec.Code, rec.Body.String())
	}
	time.Sleep(10 * time.Millisecond)
}

func feedCreatePost(t *testing.T, env *testEnv, u testUser, visibility, caption string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("caption", caption); err != nil {
		t.Fatalf("failed to write caption: %v", err)
	}
	if err := writer.WriteField("visibility", visibility); err != nil {
		t.Fatalf("failed to write visibility: %v", err)
	}
	if err := writer.WriteField("location_kind", "none"); err != nil {
		t.Fatalf("failed to write location_kind: %v", err)
	}
	part, err := writer.CreateFormFile("image", "image.jpg")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(tinyJPEG()); err != nil {
		t.Fatalf("failed to write image data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/posts", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedCreatePost failed (%d): %s", rec.Code, rec.Body.String())
	}
	var res struct {
		ID string `json:"id"`
	}
	res = decodeJSON[struct {
		ID string `json:"id"`
	}](t, rec)
	return res.ID
}

func feedAddFriendship(t *testing.T, db *sqlx.DB, u1, u2 testUser) {
	t.Helper()
	low, high := u1.ID, u2.ID
	if low > high {
		low, high = high, low
	}
	_, err := db.Exec(`INSERT INTO friendships (id, user_low_id, user_high_id) VALUES ($1, $2, $3)`, uuid.NewString(), low, high)
	if err != nil {
		t.Fatalf("failed to insert friendship: %v", err)
	}
}

func feedAddFollow(t *testing.T, db *sqlx.DB, follower, followee testUser) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO follows (id, follower_id, followee_id) VALUES ($1, $2, $3)`, uuid.NewString(), follower.ID, followee.ID)
	if err != nil {
		t.Fatalf("failed to insert follow: %v", err)
	}
}

func feedAddBlock(t *testing.T, db *sqlx.DB, blocker, blocked testUser) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($1, $2, $3)`, uuid.NewString(), blocker.ID, blocked.ID)
	if err != nil {
		t.Fatalf("failed to insert block: %v", err)
	}
}

func feedAddComment(t *testing.T, env *testEnv, u testUser, postID, body string) {
	t.Helper()
	rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", u.Token, map[string]string{
		"body": body,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedAddComment failed (%d): %s", rec.Code, rec.Body.String())
	}
}

func feedAddRealMoji(t *testing.T, env *testEnv, u testUser, postID, reactionType string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("reactionType", reactionType); err != nil {
		t.Fatalf("failed to write reactionType: %v", err)
	}
	part, err := writer.CreateFormFile("image", "realmoji.jpg")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(tinyJPEG()); err != nil {
		t.Fatalf("failed to write image data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/posts/"+postID+"/realmojis", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedAddRealMoji failed (%d): %s", rec.Code, rec.Body.String())
	}
}

// 1. Auth and Region Middleware tests
func TestIntegrationFeed_AuthAndRegionMiddleware(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	// Missing token -> 401 invalid_session
	rec := env.do(http.MethodGet, "/api/feeds/public", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error 'invalid_session', got %q", errResp.Error)
	}

	// Invalid token -> 401 invalid_session
	rec = env.do(http.MethodGet, "/api/feeds/public", "bad-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error 'invalid_session', got %q", errResp.Error)
	}

	// Memories without token -> 401 invalid_session
	rec = env.do(http.MethodGet, "/api/memories?month=2026-10", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error 'invalid_session', got %q", errResp.Error)
	}

	// Memories by date without token -> 401 invalid_session
	rec = env.do(http.MethodGet, "/api/memories/2026-10-31", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error 'invalid_session', got %q", errResp.Error)
	}

	// Export without token -> 401 invalid_session
	rec = env.do(http.MethodGet, "/api/me/export", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error 'invalid_session', got %q", errResp.Error)
	}

	// Region mismatch: user registered in europe accessing asia server -> 403 region_mismatch
	europeUser := env.registerUserInRegion("euro_user", "europe")
	rec = env.do(http.MethodGet, "/api/feeds/public", europeUser.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "region_mismatch" {
		t.Fatalf("expected error 'region_mismatch', got %q", errResp.Error)
	}

	rec = env.do(http.MethodGet, "/api/memories?month=2026-10", europeUser.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = env.do(http.MethodGet, "/api/memories/2026-10-31", europeUser.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = env.do(http.MethodGet, "/api/me/export", europeUser.Token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 2. Feed validation errors: invalid feed name and invalid pagination
func TestIntegrationFeed_ValidationAndPaginationErrors(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")
	user := env.registerUser("val_user")
	_ = env.createPost(user)

	// Invalid feed name -> 400 invalid_post
	for _, invalidFeed := range []string{"unknown", "stories", "global", "trending"} {
		rec := env.do(http.MethodGet, "/api/feeds/"+invalidFeed, user.Token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for feed %s, got %d: %s", invalidFeed, rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[APIErrorResponse](t, rec)
		if errResp.Error != "invalid_post" {
			t.Fatalf("expected error 'invalid_post', got %q", errResp.Error)
		}
	}

	// Unranked pagination: beforePostedAt provided without beforeId -> 400 invalid_pagination
	rec := env.do(http.MethodGet, "/api/feeds/public?beforePostedAt=2026-10-31T00:00:00Z", user.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected error 'invalid_pagination', got %q", errResp.Error)
	}

	// Unranked pagination: beforeId provided without beforePostedAt -> 400 invalid_pagination
	rec = env.do(http.MethodGet, "/api/feeds/public?beforeId=some-uuid", user.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected error 'invalid_pagination', got %q", errResp.Error)
	}

	// Ranked pagination (friends-of-friends):
	// beforeScore provided without beforeId/beforePostedAt -> 400 invalid_pagination
	rec = env.do(http.MethodGet, "/api/feeds/friends-of-friends?beforeScore=10", user.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected error 'invalid_pagination', got %q", errResp.Error)
	}

	// beforeId and beforePostedAt provided without beforeScore for ranked feed -> 400 invalid_pagination
	rec = env.do(http.MethodGet, "/api/feeds/friends-of-friends?beforeId=abc&beforePostedAt=2026-10-31T00:00:00Z", user.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected error 'invalid_pagination', got %q", errResp.Error)
	}
}

// 3. Must post today gating for all feeds
func TestIntegrationFeed_MustPostTodayGating(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_gate")
	poster := env.registerUser("poster_gate")
	posterPostID := feedCreatePost(t, env, poster, "public", "poster's public post")

	// Viewer has NOT posted today -> all feeds return 200 with empty items and hasMore: false
	feedKinds := []string{"public", "friends", "friends-of-friends", "following"}
	for _, feed := range feedKinds {
		rec := env.do(http.MethodGet, "/api/feeds/"+feed, viewer.Token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s before posting, got %d: %s", feed, rec.Code, rec.Body.String())
		}
		resp := decodeJSON[getFeedResponse](t, rec)
		if len(resp.Items) != 0 {
			t.Fatalf("expected 0 items before posting today in %s feed, got %d", feed, len(resp.Items))
		}
		if resp.HasMore {
			t.Fatalf("expected hasMore to be false")
		}
	}

	// Now viewer posts today
	_ = env.createPost(viewer)

	// Now public feed should reveal poster's post!
	rec := env.do(http.MethodGet, "/api/feeds/public", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for public feed after posting, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getFeedResponse](t, rec)
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item in public feed after posting today, got %d", len(resp.Items))
	}
	if resp.Items[0].ID != posterPostID {
		t.Fatalf("expected post ID %s, got %s", posterPostID, resp.Items[0].ID)
	}
}

// 4. Public feed: visibility, self-exclusion, and blocking
func TestIntegrationFeed_PublicFeed_VisibilityAndBlocking(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_pub")
	_ = feedCreatePost(t, env, viewer, "public", "viewer self post")

	userPub := env.registerUser("pub_user")
	pubPostID := feedCreatePost(t, env, userPub, "public", "public post from userPub")

	userFriendsOnly := env.registerUser("friends_only_user")
	_ = feedCreatePost(t, env, userFriendsOnly, "friends", "friends only post")

	userBlockedByViewer := env.registerUser("blocked_by_viewer")
	_ = feedCreatePost(t, env, userBlockedByViewer, "public", "post from user blocked by viewer")
	feedAddBlock(t, env.DB, viewer, userBlockedByViewer)

	userBlockerOfViewer := env.registerUser("blocker_of_viewer")
	_ = feedCreatePost(t, env, userBlockerOfViewer, "public", "post from user who blocked viewer")
	feedAddBlock(t, env.DB, userBlockerOfViewer, viewer)

	rec := env.do(http.MethodGet, "/api/feeds/public", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getFeedResponse](t, rec)

	// Only pubPostID should appear. Self, non-public, and blocked users must be excluded.
	if len(resp.Items) != 1 {
		t.Fatalf("expected exactly 1 item in public feed, got %d", len(resp.Items))
	}
	if resp.Items[0].ID != pubPostID {
		t.Fatalf("expected post ID %s, got %s", pubPostID, resp.Items[0].ID)
	}
	if resp.Items[0].Author.ID != userPub.ID {
		t.Fatalf("expected author ID %s, got %s", userPub.ID, resp.Items[0].Author.ID)
	}
}

// 5. Friends feed: visibility (public, friends, friends_of_friends), non-friends, self-exclusion, blocking
func TestIntegrationFeed_FriendsFeed_VisibilityAndBlocking(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_fr")
	_ = feedCreatePost(t, env, viewer, "friends", "viewer own post")

	friend1 := env.registerUser("friend_1")
	f1PostID := feedCreatePost(t, env, friend1, "friends", "friend1 friends post")
	feedAddFriendship(t, env.DB, viewer, friend1)

	friend2 := env.registerUser("friend_2")
	f2PostID := feedCreatePost(t, env, friend2, "public", "friend2 public post")
	feedAddFriendship(t, env.DB, viewer, friend2)

	friend3 := env.registerUser("friend_3")
	f3PostID := feedCreatePost(t, env, friend3, "friends_of_friends", "friend3 fof post")
	feedAddFriendship(t, env.DB, viewer, friend3)

	friendBlocked := env.registerUser("friend_blocked")
	_ = feedCreatePost(t, env, friendBlocked, "friends", "blocked friend post")
	feedAddFriendship(t, env.DB, viewer, friendBlocked)
	feedAddBlock(t, env.DB, viewer, friendBlocked)

	stranger := env.registerUser("stranger_user")
	_ = feedCreatePost(t, env, stranger, "public", "stranger public post")

	rec := env.do(http.MethodGet, "/api/feeds/friends", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getFeedResponse](t, rec)

	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items in friends feed, got %d", len(resp.Items))
	}

	foundIDs := make(map[string]bool)
	for _, item := range resp.Items {
		foundIDs[item.ID] = true
	}
	if !foundIDs[f1PostID] || !foundIDs[f2PostID] || !foundIDs[f3PostID] {
		t.Fatalf("expected posts %s, %s, %s; got %v", f1PostID, f2PostID, f3PostID, foundIDs)
	}
}

// 6. Following feed: public, friends (only if mutual friends), fof (if mutual friend exists), blocked
func TestIntegrationFeed_FollowingFeed_VisibilityAndRelationships(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_fl")
	_ = feedCreatePost(t, env, viewer, "public", "viewer own post")

	// Followee 1: Followed, Public post -> Visible
	followee1 := env.registerUser("followee_1")
	f1PostID := feedCreatePost(t, env, followee1, "public", "followee1 public post")
	feedAddFollow(t, env.DB, viewer, followee1)

	// Followee 2: Followed, Friends post, but NOT friends with viewer -> Hidden
	followee2 := env.registerUser("followee_2")
	_ = feedCreatePost(t, env, followee2, "friends", "followee2 friends post")
	feedAddFollow(t, env.DB, viewer, followee2)

	// Followee 3: Followed, Friends post, AND friends with viewer -> Visible
	followee3 := env.registerUser("followee_3")
	f3PostID := feedCreatePost(t, env, followee3, "friends", "followee3 friends post")
	feedAddFollow(t, env.DB, viewer, followee3)
	feedAddFriendship(t, env.DB, viewer, followee3)

	// Followee 4: Followed, Friends of Friends post, share mutual friend M -> Visible
	followee4 := env.registerUser("followee_4")
	mutualM := env.registerUser("mutual_m")
	f4PostID := feedCreatePost(t, env, followee4, "friends_of_friends", "followee4 fof post")
	feedAddFollow(t, env.DB, viewer, followee4)
	feedAddFriendship(t, env.DB, viewer, mutualM)
	feedAddFriendship(t, env.DB, mutualM, followee4)

	// Followee 5: Followed, Public post, but Blocked -> Hidden
	followee5 := env.registerUser("followee_5")
	_ = feedCreatePost(t, env, followee5, "public", "followee5 public post blocked")
	feedAddFollow(t, env.DB, viewer, followee5)
	feedAddBlock(t, env.DB, viewer, followee5)

	// Non-followee: Public post -> Hidden
	nonFollowee := env.registerUser("non_followee")
	_ = feedCreatePost(t, env, nonFollowee, "public", "non followee post")

	rec := env.do(http.MethodGet, "/api/feeds/following", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getFeedResponse](t, rec)

	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items in following feed, got %d", len(resp.Items))
	}
	foundIDs := make(map[string]bool)
	for _, item := range resp.Items {
		foundIDs[item.ID] = true
	}
	if !foundIDs[f1PostID] || !foundIDs[f3PostID] || !foundIDs[f4PostID] {
		t.Fatalf("expected posts %s, %s, %s in following feed; got %v", f1PostID, f3PostID, f4PostID, foundIDs)
	}
}

// 7. Friends-of-friends feed: ranking, exclusion of direct friends, visibility restriction, and reactions
func TestIntegrationFeed_FriendsOfFriendsFeed_RankingAndScoring(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_fof")
	_ = feedCreatePost(t, env, viewer, "public", "viewer post")

	// Direct friend F
	friendF := env.registerUser("friend_f")
	_ = feedCreatePost(t, env, friendF, "friends_of_friends", "direct friend post")
	feedAddFriendship(t, env.DB, viewer, friendF)

	// FoF 1: friend of F, not direct friend of viewer. Visibility: friends_of_friends
	fof1 := env.registerUser("fof_user_1")
	fof1PostID := feedCreatePost(t, env, fof1, "friends_of_friends", "fof1 post")
	feedAddFriendship(t, env.DB, friendF, fof1)

	// FoF 2: friend of F, not direct friend of viewer. Visibility: public
	fof2 := env.registerUser("fof_user_2")
	fof2PostID := feedCreatePost(t, env, fof2, "public", "fof2 post")
	feedAddFriendship(t, env.DB, friendF, fof2)

	// FoF 3: friend of F, but Visibility: friends -> Excluded from FoF feed!
	fof3 := env.registerUser("fof_user_3")
	_ = feedCreatePost(t, env, fof3, "friends", "fof3 friends only post")
	feedAddFriendship(t, env.DB, friendF, fof3)

	// FoF 4: friend of F, but blocked by viewer -> Excluded
	fof4 := env.registerUser("fof_user_4")
	_ = feedCreatePost(t, env, fof4, "friends_of_friends", "fof4 post blocked")
	feedAddFriendship(t, env.DB, friendF, fof4)
	feedAddBlock(t, env.DB, viewer, fof4)

	// Add engagement by Friend F to FoF 1's post (1 comment, 1 realmoji)
	// Scores: 10 * friendEngagements + 3 * mutualFriends + min(connections, 30)
	// Friend F is a friend of viewer, so Friend F engaging adds +10 per engagement!
	feedAddComment(t, env, friendF, fof1PostID, "awesome post!")
	feedAddRealMoji(t, env, friendF, fof1PostID, "heart")

	rec := env.do(http.MethodGet, "/api/feeds/friends-of-friends", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getFeedResponse](t, rec)

	// Only FoF1 and FoF2 should appear (direct friend F, friends-only fof3, and blocked fof4 excluded)
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items in FoF feed, got %d", len(resp.Items))
	}
	// FoF 1 should be ranked #1 because of friend engagement
	if resp.Items[0].ID != fof1PostID {
		t.Fatalf("expected top ranked post to be %s, got %s", fof1PostID, resp.Items[0].ID)
	}
	if resp.Items[1].ID != fof2PostID {
		t.Fatalf("expected second ranked post to be %s, got %s", fof2PostID, resp.Items[1].ID)
	}

	// Score field must be present
	if resp.Items[0].Score == nil || resp.Items[1].Score == nil {
		t.Fatalf("expected score to be non-nil in FoF feed items")
	}
	if *resp.Items[0].Score <= *resp.Items[1].Score {
		t.Fatalf("expected score for fof1 (%d) to be > fof2 (%d)", *resp.Items[0].Score, *resp.Items[1].Score)
	}
}

// 8. Ordering and cursor pagination for unranked feeds
func TestIntegrationFeed_OrderingAndPagination_Unranked(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_page")
	_ = feedCreatePost(t, env, viewer, "public", "viewer post")

	user1 := env.registerUser("u1_page")
	p1 := feedCreatePost(t, env, user1, "public", "post 1")
	time.Sleep(20 * time.Millisecond)

	user2 := env.registerUser("u2_page")
	p2 := feedCreatePost(t, env, user2, "public", "post 2")
	time.Sleep(20 * time.Millisecond)

	user3 := env.registerUser("u3_page")
	p3 := feedCreatePost(t, env, user3, "public", "post 3")

	// Page 1: all posts in descending order: p3, p2, p1
	rec := env.do(http.MethodGet, "/api/feeds/public", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page1 := decodeJSON[getFeedResponse](t, rec)
	if len(page1.Items) != 3 {
		t.Fatalf("expected 3 items in page 1, got %d", len(page1.Items))
	}
	if page1.Items[0].ID != p3 || page1.Items[1].ID != p2 || page1.Items[2].ID != p1 {
		t.Fatalf("expected order [p3, p2, p1], got [%s, %s, %s]", page1.Items[0].ID, page1.Items[1].ID, page1.Items[2].ID)
	}

	// Cursor paginate after p3: should return [p2, p1]
	p3PostedAt := page1.Items[0].PostedAt.Format(time.RFC3339Nano)
	page2URL := fmt.Sprintf("/api/feeds/public?beforePostedAt=%s&beforeId=%s", url.QueryEscape(p3PostedAt), url.QueryEscape(p3))
	rec = env.do(http.MethodGet, page2URL, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page2 := decodeJSON[getFeedResponse](t, rec)
	if len(page2.Items) != 2 {
		t.Fatalf("expected 2 items in page 2, got %d", len(page2.Items))
	}
	if page2.Items[0].ID != p2 || page2.Items[1].ID != p1 {
		t.Fatalf("expected [p2, p1], got [%s, %s]", page2.Items[0].ID, page2.Items[1].ID)
	}

	// Cursor paginate after p2: should return [p1]
	p2PostedAt := page2.Items[0].PostedAt.Format(time.RFC3339Nano)
	page3URL := fmt.Sprintf("/api/feeds/public?beforePostedAt=%s&beforeId=%s", url.QueryEscape(p2PostedAt), url.QueryEscape(p2))
	rec = env.do(http.MethodGet, page3URL, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page3 := decodeJSON[getFeedResponse](t, rec)
	if len(page3.Items) != 1 || page3.Items[0].ID != p1 {
		t.Fatalf("expected [p1], got %v", page3.Items)
	}

	// Cursor paginate after p1: should return empty []
	p1PostedAt := page3.Items[0].PostedAt.Format(time.RFC3339Nano)
	page4URL := fmt.Sprintf("/api/feeds/public?beforePostedAt=%s&beforeId=%s", url.QueryEscape(p1PostedAt), url.QueryEscape(p1))
	rec = env.do(http.MethodGet, page4URL, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page4 := decodeJSON[getFeedResponse](t, rec)
	if len(page4.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(page4.Items))
	}
	if page4.HasMore {
		t.Fatalf("expected hasMore=false")
	}
}

// 9. Ordering and cursor pagination for ranked feeds
func TestIntegrationFeed_OrderingAndPagination_Ranked(t *testing.T) {
	env := newTestEnv(t)
	feedOpenWindow(t, env, "2026-10-31")

	viewer := env.registerUser("viewer_rf")
	_ = feedCreatePost(t, env, viewer, "public", "viewer post")

	friend := env.registerUser("friend_rf")
	_ = feedCreatePost(t, env, friend, "friends", "friend post")
	feedAddFriendship(t, env.DB, viewer, friend)

	fof1 := env.registerUser("fof1_rf")
	p1 := feedCreatePost(t, env, fof1, "friends_of_friends", "post 1")
	feedAddFriendship(t, env.DB, friend, fof1)

	fof2 := env.registerUser("fof2_rf")
	p2 := feedCreatePost(t, env, fof2, "friends_of_friends", "post 2")
	feedAddFriendship(t, env.DB, friend, fof2)

	// Friend interacts twice with p1 so p1 score is strictly higher than p2
	feedAddComment(t, env, friend, p1, "reaction 1")
	feedAddRealMoji(t, env, friend, p1, "smile")

	rec := env.do(http.MethodGet, "/api/feeds/friends-of-friends", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page1 := decodeJSON[getFeedResponse](t, rec)
	if len(page1.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(page1.Items))
	}
	if page1.Items[0].ID != p1 || page1.Items[1].ID != p2 {
		t.Fatalf("expected [p1, p2], got [%s, %s]", page1.Items[0].ID, page1.Items[1].ID)
	}

	// Paginate after p1 using beforeScore, beforePostedAt, beforeId
	p1Score := *page1.Items[0].Score
	p1PostedAt := page1.Items[0].PostedAt.Format(time.RFC3339Nano)
	page2URL := fmt.Sprintf("/api/feeds/friends-of-friends?beforeScore=%d&beforePostedAt=%s&beforeId=%s",
		p1Score, url.QueryEscape(p1PostedAt), url.QueryEscape(p1))
	rec = env.do(http.MethodGet, page2URL, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page2 := decodeJSON[getFeedResponse](t, rec)
	if len(page2.Items) != 1 || page2.Items[0].ID != p2 {
		t.Fatalf("expected [p2], got %v", page2.Items)
	}

	// Paginate after p2
	p2Score := *page2.Items[0].Score
	p2PostedAt := page2.Items[0].PostedAt.Format(time.RFC3339Nano)
	page3URL := fmt.Sprintf("/api/feeds/friends-of-friends?beforeScore=%d&beforePostedAt=%s&beforeId=%s",
		p2Score, url.QueryEscape(p2PostedAt), url.QueryEscape(p2))
	rec = env.do(http.MethodGet, page3URL, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page3 := decodeJSON[getFeedResponse](t, rec)
	if len(page3.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(page3.Items))
	}
}

// 10. Memories API: monthly memories with interactions, user isolation, and pagination
func TestIntegrationFeed_Memories_Monthly(t *testing.T) {
	env := newTestEnv(t)

	userA := env.registerUser("user_mem_a")
	userB := env.registerUser("user_mem_b")

	// Window 1: 2026-10-29
	feedOpenWindow(t, env, "2026-10-29")
	p1 := feedCreatePost(t, env, userA, "public", "post on 29th")
	_ = feedCreatePost(t, env, userB, "public", "user B post on 29th")

	// Window 2: 2026-10-30
	feedOpenWindow(t, env, "2026-10-30")
	p2 := feedCreatePost(t, env, userA, "friends", "post on 30th")

	// Window 3: 2026-10-31
	feedOpenWindow(t, env, "2026-10-31")
	p3 := feedCreatePost(t, env, userA, "public", "post on 31st")
	_ = feedCreatePost(t, env, userB, "public", "user B post on 31st")

	// Add interactions to p3
	feedAddFriendship(t, env.DB, userA, userB)
	feedAddComment(t, env, userB, p3, "first memory comment")
	feedAddRealMoji(t, env, userB, p3, "laugh")

	// Window 4: next month 2026-11-01
	feedOpenWindow(t, env, "2026-11-01")
	_ = feedCreatePost(t, env, userA, "public", "november post")

	// GET /api/memories?month=2026-10
	rec := env.do(http.MethodGet, "/api/memories?month=2026-10", userA.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getMemoriesResponse](t, rec)

	// Must contain exactly 3 posts (p3, p2, p1 descending). User B's post and Nov post excluded.
	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items in October memories, got %d", len(resp.Items))
	}
	if resp.Items[0].ID != p3 || resp.Items[1].ID != p2 || resp.Items[2].ID != p1 {
		t.Fatalf("expected [p3, p2, p1], got [%s, %s, %s]", resp.Items[0].ID, resp.Items[1].ID, resp.Items[2].ID)
	}

	// Verify interactions on p3
	if len(resp.Items[0].Comments) != 1 {
		t.Fatalf("expected 1 comment on p3, got %d", len(resp.Items[0].Comments))
	}
	if resp.Items[0].Comments[0].Body != "first memory comment" {
		t.Fatalf("expected comment body 'first memory comment', got %s", resp.Items[0].Comments[0].Body)
	}
	if len(resp.Items[0].RealMojis) != 1 {
		t.Fatalf("expected 1 realmoji on p3, got %d", len(resp.Items[0].RealMojis))
	}
	if resp.Items[0].RealMojis[0].ReactionType != "laugh" {
		t.Fatalf("expected realmoji reaction 'laugh', got %s", resp.Items[0].RealMojis[0].ReactionType)
	}

	// Verify cursor pagination on monthly memories
	p3PostedAt := resp.Items[0].PostedAt.Format(time.RFC3339Nano)
	page2URL := fmt.Sprintf("/api/memories?month=2026-10&beforePostedAt=%s&beforeId=%s", url.QueryEscape(p3PostedAt), url.QueryEscape(p3))
	rec = env.do(http.MethodGet, page2URL, userA.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	page2 := decodeJSON[getMemoriesResponse](t, rec)
	if len(page2.Items) != 2 || page2.Items[0].ID != p2 || page2.Items[1].ID != p1 {
		t.Fatalf("expected [p2, p1], got %v", page2.Items)
	}

	// Validation errors for monthly memories
	// Missing month -> 400 invalid_memory_period
	rec = env.do(http.MethodGet, "/api/memories", userA.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing month, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_memory_period" {
		t.Fatalf("expected 'invalid_memory_period', got %q", errResp.Error)
	}

	// Invalid month format -> 400 invalid_memory_period
	for _, badMonth := range []string{"2026-13", "not-a-month", "2026", "2026-10-31"} {
		rec = env.do(http.MethodGet, "/api/memories?month="+badMonth, userA.Token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bad month %s, got %d: %s", badMonth, rec.Code, rec.Body.String())
		}
		errResp = decodeJSON[APIErrorResponse](t, rec)
		if errResp.Error != "invalid_memory_period" {
			t.Fatalf("expected 'invalid_memory_period', got %q", errResp.Error)
		}
	}

	// Invalid pagination on memories -> 400 invalid_pagination
	rec = env.do(http.MethodGet, "/api/memories?month=2026-10&beforeId=xyz", userA.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp = decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected 'invalid_pagination', got %q", errResp.Error)
	}
}

// 11. Memories API: daily memories, invalid date, and soft deletion reflection
func TestIntegrationFeed_Memories_DailyAndDeletion(t *testing.T) {
	env := newTestEnv(t)

	user := env.registerUser("user_mem_daily")

	// Window 1: 2026-10-30
	feedOpenWindow(t, env, "2026-10-30")
	p1 := feedCreatePost(t, env, user, "public", "post on 30th")

	// Window 2: 2026-10-31
	feedOpenWindow(t, env, "2026-10-31")
	p2 := feedCreatePost(t, env, user, "public", "post on 31st")

	// GET /api/memories/2026-10-30 -> returns only p1
	rec := env.do(http.MethodGet, "/api/memories/2026-10-30", user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[getMemoriesResponse](t, rec)
	if len(resp.Items) != 1 || resp.Items[0].ID != p1 {
		t.Fatalf("expected [p1], got %v", resp.Items)
	}

	// GET /api/memories/2026-10-31 -> returns only p2
	rec = env.do(http.MethodGet, "/api/memories/2026-10-31", user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp = decodeJSON[getMemoriesResponse](t, rec)
	if len(resp.Items) != 1 || resp.Items[0].ID != p2 {
		t.Fatalf("expected [p2], got %v", resp.Items)
	}

	// GET /api/memories/2026-10-25 (no posts) -> returns empty []
	rec = env.do(http.MethodGet, "/api/memories/2026-10-25", user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp = decodeJSON[getMemoriesResponse](t, rec)
	if len(resp.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(resp.Items))
	}

	// Soft delete p2: DELETE /api/posts/:postId -> 204
	delRec := env.do(http.MethodDelete, "/api/posts/"+p2, user.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Subsequent GET /api/memories/2026-10-31 must now return empty []
	rec = env.do(http.MethodGet, "/api/memories/2026-10-31", user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	resp = decodeJSON[getMemoriesResponse](t, rec)
	if len(resp.Items) != 0 {
		t.Fatalf("expected 0 items after deletion, got %d", len(resp.Items))
	}

	// Invalid date formats -> 400 invalid_memory_period
	for _, badDate := range []string{"invalid", "2026-13-01", "2026-02-31", "2026-10"} {
		rec = env.do(http.MethodGet, "/api/memories/"+badDate, user.Token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for bad date %s, got %d: %s", badDate, rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[APIErrorResponse](t, rec)
		if errResp.Error != "invalid_memory_period" {
			t.Fatalf("expected 'invalid_memory_period', got %q", errResp.Error)
		}
	}

	// Invalid pagination for daily memories
	rec = env.do(http.MethodGet, "/api/memories/2026-10-30?beforePostedAt=2026-10-30T00:00:00Z", user.Token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[APIErrorResponse](t, rec)
	if errResp.Error != "invalid_pagination" {
		t.Fatalf("expected 'invalid_pagination', got %q", errResp.Error)
	}
}

// 12. Export API: ZIP format, posts.json contents, image files, user isolation, and deletion
func TestIntegrationFeed_ExportPosts_FormatAndContents(t *testing.T) {
	env := newTestEnv(t)

	userA := env.registerUser("user_exp_a")
	userB := env.registerUser("user_exp_b")

	// User with 0 posts exports empty zip
	recEmpty := env.do(http.MethodGet, "/api/me/export", userA.Token, nil)
	if recEmpty.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recEmpty.Code, recEmpty.Body.String())
	}
	if ct := recEmpty.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("expected application/zip, got %s", ct)
	}
	if cd := recEmpty.Header().Get("Content-Disposition"); cd != `attachment; filename="isureal-posts.zip"` {
		t.Fatalf("expected attachment header, got %s", cd)
	}
	zipReaderEmpty, err := zip.NewReader(bytes.NewReader(recEmpty.Body.Bytes()), int64(recEmpty.Body.Len()))
	if err != nil {
		t.Fatalf("failed to parse empty export zip: %v", err)
	}
	var foundManifest bool
	for _, f := range zipReaderEmpty.File {
		if f.Name == "posts.json" {
			foundManifest = true
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("failed to open posts.json: %v", err)
			}
			var items []exportPostResponse
			if err := json.NewDecoder(rc).Decode(&items); err != nil {
				t.Fatalf("failed to decode posts.json: %v", err)
			}
			rc.Close()
			if len(items) != 0 {
				t.Fatalf("expected 0 items in empty export, got %d", len(items))
			}
		}
	}
	if !foundManifest {
		t.Fatalf("posts.json not found in empty export zip")
	}

	// Now create posts
	feedOpenWindow(t, env, "2026-10-30")
	p1 := feedCreatePost(t, env, userA, "public", "post 1 for export")
	_ = feedCreatePost(t, env, userB, "public", "user B post should NOT be exported")

	feedOpenWindow(t, env, "2026-10-31")
	p2 := feedCreatePost(t, env, userA, "friends", "post 2 for export")

	// User A exports posts
	rec := env.do(http.MethodGet, "/api/me/export", userA.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("expected application/zip, got %s", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="isureal-posts.zip"` {
		t.Fatalf("expected attachment header, got %s", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("expected Cache-Control: no-store, got %s", cc)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}

	filesByName := make(map[string]*zip.File)
	for _, f := range zipReader.File {
		filesByName[f.Name] = f
	}

	manifestFile, ok := filesByName["posts.json"]
	if !ok {
		t.Fatalf("missing posts.json in export zip")
	}

	rc, err := manifestFile.Open()
	if err != nil {
		t.Fatalf("failed to open posts.json: %v", err)
	}
	var exportedItems []exportPostResponse
	if err := json.NewDecoder(rc).Decode(&exportedItems); err != nil {
		t.Fatalf("failed to decode posts.json: %v", err)
	}
	rc.Close()

	// Must contain exactly 2 posts in chronological order: p1, p2
	if len(exportedItems) != 2 {
		t.Fatalf("expected 2 exported items, got %d", len(exportedItems))
	}
	if exportedItems[0].ID != p1 || exportedItems[1].ID != p2 {
		t.Fatalf("expected [p1, p2], got [%s, %s]", exportedItems[0].ID, exportedItems[1].ID)
	}
	if exportedItems[0].Author.ID != userA.ID || exportedItems[1].Author.ID != userA.ID {
		t.Fatalf("expected user A as author for all exported posts")
	}
	if exportedItems[0].Caption != "post 1 for export" || exportedItems[1].Caption != "post 2 for export" {
		t.Fatalf("unexpected captions in exported posts")
	}

	// Verify images exist in zip and are valid JPEGs
	for _, item := range exportedItems {
		imgFile, ok := filesByName[item.ImagePath]
		if !ok {
			t.Fatalf("missing image file %s in export zip", item.ImagePath)
		}
		imgRC, err := imgFile.Open()
		if err != nil {
			t.Fatalf("failed to open %s: %v", item.ImagePath, err)
		}
		imgBytes, err := io.ReadAll(imgRC)
		imgRC.Close()
		if err != nil {
			t.Fatalf("failed to read %s: %v", item.ImagePath, err)
		}
		if len(imgBytes) == 0 {
			t.Fatalf("image file %s is empty", item.ImagePath)
		}
		_, err = jpeg.Decode(bytes.NewReader(imgBytes))
		if err != nil {
			t.Fatalf("image file %s is not valid JPEG: %v", item.ImagePath, err)
		}
	}

	// Soft delete p2 and re-export: state change visible
	delRec := env.do(http.MethodDelete, "/api/posts/"+p2, userA.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("failed to delete post p2: %d", delRec.Code)
	}

	recAfterDel := env.do(http.MethodGet, "/api/me/export", userA.Token, nil)
	if recAfterDel.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recAfterDel.Code, recAfterDel.Body.String())
	}
	zipReaderAfterDel, err := zip.NewReader(bytes.NewReader(recAfterDel.Body.Bytes()), int64(recAfterDel.Body.Len()))
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}
	manifestFile2 := zipReaderAfterDel.File[len(zipReaderAfterDel.File)-1]
	rc2, err := manifestFile2.Open()
	if err != nil {
		t.Fatalf("failed to open posts.json: %v", err)
	}
	var exportedAfterDel []exportPostResponse
	_ = json.NewDecoder(rc2).Decode(&exportedAfterDel)
	rc2.Close()

	if len(exportedAfterDel) != 1 || exportedAfterDel[0].ID != p1 {
		t.Fatalf("expected only [p1] after deleting p2, got %v", exportedAfterDel)
	}
}
