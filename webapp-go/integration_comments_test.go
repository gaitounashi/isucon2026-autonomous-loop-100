package isureal

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// --- Helper types for decoding responses ---

type commentUserResp struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type commentResp struct {
	ID        string          `json:"id"`
	Author    commentUserResp `json:"author"`
	Body      string          `json:"body"`
	CreatedAt time.Time       `json:"createdAt"`
}

type commentListResp struct {
	Items   []commentResp `json:"items"`
	HasMore bool          `json:"hasMore"`
}

type commentRealMojiResp struct {
	ID           string          `json:"id"`
	Reactor      commentUserResp `json:"reactor"`
	ReactionType string          `json:"reactionType"`
	ImageURL     string          `json:"imageUrl"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type commentRealMojiListResp struct {
	Items   []commentRealMojiResp `json:"items"`
	HasMore bool                  `json:"hasMore"`
}

type commentErrorResp struct {
	Error string `json:"error"`
}

// --- Area-prefixed helper functions ---

func commentAssertStatus(t *testing.T, rec *httptest.ResponseRecorder, expectedStatus int) {
	t.Helper()
	if rec.Code != expectedStatus {
		t.Fatalf("expected status %d, got %d: %s", expectedStatus, rec.Code, rec.Body.String())
	}
}

func commentAssertError(t *testing.T, rec *httptest.ResponseRecorder, expectedStatus int, expectedErrorCode string) {
	t.Helper()
	commentAssertStatus(t, rec, expectedStatus)
	if expectedErrorCode != "" {
		errResp := decodeJSON[commentErrorResp](t, rec)
		if errResp.Error != expectedErrorCode {
			t.Fatalf("expected error code %q, got %q", expectedErrorCode, errResp.Error)
		}
	}
}

func commentCreatePostWithVisibility(t *testing.T, env *testEnv, u testUser, visibility string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("caption", "post with "+visibility); err != nil {
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
		t.Fatalf("createPostWithVisibility failed with status %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		ID string `json:"id"`
	}
	res = decodeJSON[struct {
		ID string `json:"id"`
	}](t, rec)
	if res.ID == "" {
		t.Fatalf("createPostWithVisibility returned empty post ID: %s", rec.Body.String())
	}
	return res.ID
}

func commentCreateFriendship(t *testing.T, env *testEnv, u1, u2 testUser) {
	t.Helper()
	low, high := u1.ID, u2.ID
	if low > high {
		low, high = high, low
	}
	_, err := env.DB.Exec(`INSERT INTO friendships (id, user_low_id, user_high_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, uuid.NewString(), low, high)
	if err != nil {
		t.Fatalf("failed to insert friendship: %v", err)
	}
}

func commentCreateBlock(t *testing.T, env *testEnv, blocker, blocked testUser) {
	t.Helper()
	_, err := env.DB.Exec(`INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, uuid.NewString(), blocker.ID, blocked.ID)
	if err != nil {
		t.Fatalf("failed to insert block: %v", err)
	}
}

func commentLargeJPEG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 513, 513))
	for y := 0; y < 513; y++ {
		for x := 0; x < 513; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func commentPostRealMoji(env *testEnv, postID, token, reactionType string, imageData []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if reactionType != "" {
		_ = writer.WriteField("reactionType", reactionType)
	}
	if imageData != nil {
		part, err := writer.CreateFormFile("image", "realmoji.jpg")
		if err == nil {
			_, _ = part.Write(imageData)
		}
	}
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/posts/"+postID+"/realmojis", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

// --- Integration Tests ---

// 1. Auth tests: 401 without token and with invalid token for all 6 endpoints.
func TestIntegrationComments_Auth(t *testing.T) {
	env := newTestEnv(t)

	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/posts/some-post-id/comments", map[string]string{"body": "test comment"}},
		{http.MethodGet, "/api/posts/some-post-id/comments", nil},
		{http.MethodDelete, "/api/comments/some-comment-id", nil},
		{http.MethodPost, "/api/posts/some-post-id/realmojis", nil},
		{http.MethodGet, "/api/posts/some-post-id/realmojis", nil},
		{http.MethodDelete, "/api/realmojis/some-realmoji-id", nil},
	}

	for _, ep := range endpoints {
		t.Run(fmt.Sprintf("%s %s missing token", ep.method, ep.path), func(t *testing.T) {
			rec := env.do(ep.method, ep.path, "", ep.body)
			commentAssertError(t, rec, http.StatusUnauthorized, "invalid_session")
		})

		t.Run(fmt.Sprintf("%s %s invalid token", ep.method, ep.path), func(t *testing.T) {
			rec := env.do(ep.method, ep.path, "invalid-token-abc", ep.body)
			commentAssertError(t, rec, http.StatusUnauthorized, "invalid_session")
		})
	}
}

// 2. Comment validation: empty body, oversized body, not-found post, valid boundaries.
func TestIntegrationComments_CreateCommentValidation(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("val_author")
	postID := env.createPost(author)

	commenter := env.registerUser("val_commenter")
	_ = env.createPost(commenter) // commenter must have posted to comment

	t.Run("empty body returns 400 invalid_interaction", func(t *testing.T) {
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": "",
		})
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_interaction")
	})

	t.Run("body exceeding 1000 runes returns 400 invalid_interaction", func(t *testing.T) {
		longBody := strings.Repeat("あ", 1001) // 1001 runes
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": longBody,
		})
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_interaction")
	})

	t.Run("commenting on non-existent post returns 403 interaction_forbidden", func(t *testing.T) {
		nonExistentID := uuid.NewString()
		rec := env.do(http.MethodPost, "/api/posts/"+nonExistentID+"/comments", commenter.Token, map[string]string{
			"body": "hello non-existent",
		})
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("minimum 1 rune body succeeds with 201", func(t *testing.T) {
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": "x",
		})
		commentAssertStatus(t, rec, http.StatusCreated)
		resp := decodeJSON[commentResp](t, rec)
		if resp.ID == "" {
			t.Fatalf("expected non-empty comment ID")
		}
		if resp.Body != "x" {
			t.Fatalf("expected body 'x', got %q", resp.Body)
		}
		if resp.Author.ID != commenter.ID {
			t.Fatalf("expected author ID %s, got %s", commenter.ID, resp.Author.ID)
		}
		if resp.Author.Username != commenter.Username {
			t.Fatalf("expected author username %s, got %s", commenter.Username, resp.Author.Username)
		}
	})

	t.Run("exact 1000 runes body succeeds with 201", func(t *testing.T) {
		exact1000 := strings.Repeat("字", 1000)
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": exact1000,
		})
		commentAssertStatus(t, rec, http.StatusCreated)
		resp := decodeJSON[commentResp](t, rec)
		if resp.Body != exact1000 {
			t.Fatalf("expected 1000-rune body")
		}
	})
}

// 3. Comment permissions: window active, user posted in window, blocks, visibility.
func TestIntegrationComments_CreateCommentPermissions(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("perm_author")
	postID := env.createPost(author)

	t.Run("user who has not posted in window gets 403 interaction_forbidden", func(t *testing.T) {
		lurker := env.registerUser("perm_lurker")
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", lurker.Token, map[string]string{
			"body": "lurker comment",
		})
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("author can comment on own post", func(t *testing.T) {
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", author.Token, map[string]string{
			"body": "author self comment",
		})
		commentAssertStatus(t, rec, http.StatusCreated)
		resp := decodeJSON[commentResp](t, rec)
		if resp.Author.ID != author.ID {
			t.Fatalf("expected author ID %s, got %s", author.ID, resp.Author.ID)
		}
	})

	t.Run("block relations prevent commenting in both directions", func(t *testing.T) {
		commenterA := env.registerUser("perm_blocked_a")
		_ = env.createPost(commenterA)
		commentCreateBlock(t, env, author, commenterA) // author blocked commenterA

		recA := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenterA.Token, map[string]string{
			"body": "should be blocked by author",
		})
		commentAssertError(t, recA, http.StatusForbidden, "interaction_forbidden")

		commenterB := env.registerUser("perm_blocker_b")
		_ = env.createPost(commenterB)
		commentCreateBlock(t, env, commenterB, author) // commenterB blocked author

		recB := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenterB.Token, map[string]string{
			"body": "should be blocked by self",
		})
		commentAssertError(t, recB, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("friends visibility allows friend and forbids non-friend", func(t *testing.T) {
		friendsAuthor := env.registerUser("perm_fa")
		friendsPostID := commentCreatePostWithVisibility(t, env, friendsAuthor, "friends")

		nonFriend := env.registerUser("perm_non_friend")
		_ = env.createPost(nonFriend)

		recNF := env.do(http.MethodPost, "/api/posts/"+friendsPostID+"/comments", nonFriend.Token, map[string]string{
			"body": "non-friend trying to comment",
		})
		commentAssertError(t, recNF, http.StatusForbidden, "interaction_forbidden")

		friend := env.registerUser("perm_friend")
		_ = env.createPost(friend)
		commentCreateFriendship(t, env, friendsAuthor, friend)

		recF := env.do(http.MethodPost, "/api/posts/"+friendsPostID+"/comments", friend.Token, map[string]string{
			"body": "friend commenting",
		})
		commentAssertStatus(t, recF, http.StatusCreated)
	})

	t.Run("friends_of_friends visibility allows direct friend but forbids mutual friend in canComment", func(t *testing.T) {
		fofAuthor := env.registerUser("perm_fofa")
		fofPostID := commentCreatePostWithVisibility(t, env, fofAuthor, "friends_of_friends")

		friend := env.registerUser("perm_fof_direct")
		_ = env.createPost(friend)
		commentCreateFriendship(t, env, fofAuthor, friend)

		// Direct friend succeeds
		recDirect := env.do(http.MethodPost, "/api/posts/"+fofPostID+"/comments", friend.Token, map[string]string{
			"body": "direct friend comment on fof post",
		})
		commentAssertStatus(t, recDirect, http.StatusCreated)

		// Mutual friend (friend of friend): fofAuthor <-> friend <-> mutualFriend
		mutualFriend := env.registerUser("perm_fof_mutual")
		_ = env.createPost(mutualFriend)
		commentCreateFriendship(t, env, friend, mutualFriend)

		recMutual := env.do(http.MethodPost, "/api/posts/"+fofPostID+"/comments", mutualFriend.Token, map[string]string{
			"body": "mutual friend comment on fof post",
		})
		// canComment currently only checks direct friendship even for friends_of_friends
		commentAssertError(t, recMutual, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("ended posting window forbids commenting even for author", func(t *testing.T) {
		// End the window
		_, err := env.DB.Exec(`UPDATE posting_windows SET ended_at = clock_timestamp() WHERE ended_at IS NULL`)
		if err != nil {
			t.Fatalf("failed to end posting window: %v", err)
		}

		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", author.Token, map[string]string{
			"body": "comment after window ended",
		})
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})
}

// 4. GET comments: ordering, pagination validation, >50 pagination, permissions, soft deleted.
func TestIntegrationComments_GetComments(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("get_author")
	postID := env.createPost(author)

	viewer := env.registerUser("get_viewer")
	_ = env.createPost(viewer)

	// Post 3 comments
	var commentIDs []string
	for i := 1; i <= 3; i++ {
		rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", viewer.Token, map[string]string{
			"body": fmt.Sprintf("comment number %d", i),
		})
		commentAssertStatus(t, rec, http.StatusCreated)
		resp := decodeJSON[commentResp](t, rec)
		commentIDs = append(commentIDs, resp.ID)
	}

	t.Run("happy path returns ordered comments", func(t *testing.T) {
		rec := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", viewer.Token, nil)
		commentAssertStatus(t, rec, http.StatusOK)
		list := decodeJSON[commentListResp](t, rec)
		if len(list.Items) != 3 {
			t.Fatalf("expected 3 comments, got %d", len(list.Items))
		}
		if list.HasMore {
			t.Fatalf("expected hasMore to be false")
		}
		for i, item := range list.Items {
			if item.ID != commentIDs[i] {
				t.Errorf("item[%d] ID = %s, want %s", i, item.ID, commentIDs[i])
			}
			if item.Body != fmt.Sprintf("comment number %d", i+1) {
				t.Errorf("item[%d] Body = %s", i, item.Body)
			}
			if item.Author.ID != viewer.ID {
				t.Errorf("item[%d] Author ID = %s, want %s", i, item.Author.ID, viewer.ID)
			}
		}
	})

	t.Run("invalid pagination query parameters return 400 invalid_pagination", func(t *testing.T) {
		// afterCreatedAt provided without afterId
		nowStr := url.QueryEscape(time.Now().UTC().Format(time.RFC3339Nano))
		rec1 := env.do(http.MethodGet, "/api/posts/"+postID+"/comments?afterCreatedAt="+nowStr, viewer.Token, nil)
		commentAssertError(t, rec1, http.StatusBadRequest, "invalid_pagination")

		// afterId provided without afterCreatedAt
		rec2 := env.do(http.MethodGet, "/api/posts/"+postID+"/comments?afterId="+commentIDs[0], viewer.Token, nil)
		commentAssertError(t, rec2, http.StatusBadRequest, "invalid_pagination")
	})

	t.Run("permissions to view comments", func(t *testing.T) {
		lurker := env.registerUser("get_lurker")
		recL := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", lurker.Token, nil)
		commentAssertError(t, recL, http.StatusForbidden, "interaction_forbidden")

		blockedUser := env.registerUser("get_blocked")
		_ = env.createPost(blockedUser)
		commentCreateBlock(t, env, author, blockedUser)
		recB := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", blockedUser.Token, nil)
		commentAssertError(t, recB, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("pagination with over 50 comments", func(t *testing.T) {
		pagAuthor := env.registerUser("get_pag_author")
		pagPostID := env.createPost(pagAuthor)

		baseTime := time.Now().UTC().Add(-1 * time.Hour)
		// Insert 52 comments directly via SQL
		for i := 0; i < 52; i++ {
			cID := uuid.NewString()
			cTime := baseTime.Add(time.Duration(i) * time.Second)
			_, err := env.DB.Exec(`
				INSERT INTO comments (id, post_id, author_id, body, created_at)
				VALUES ($1, $2, $3, $4, $5)
			`, cID, pagPostID, viewer.ID, fmt.Sprintf("bulk comment %d", i), cTime)
			if err != nil {
				t.Fatalf("failed to insert bulk comment: %v", err)
			}
		}

		// First page: should return 50 items and hasMore = true
		recPage1 := env.do(http.MethodGet, "/api/posts/"+pagPostID+"/comments", viewer.Token, nil)
		commentAssertStatus(t, recPage1, http.StatusOK)
		list1 := decodeJSON[commentListResp](t, recPage1)
		if len(list1.Items) != 50 {
			t.Fatalf("page 1 expected 50 items, got %d", len(list1.Items))
		}
		if !list1.HasMore {
			t.Fatalf("page 1 expected hasMore=true")
		}

		// Second page: query using afterCreatedAt & afterId of 50th item
		lastItem := list1.Items[49]
		page2URL := fmt.Sprintf("/api/posts/%s/comments?afterCreatedAt=%s&afterId=%s",
			pagPostID,
			url.QueryEscape(lastItem.CreatedAt.Format(time.RFC3339Nano)),
			url.QueryEscape(lastItem.ID),
		)
		recPage2 := env.do(http.MethodGet, page2URL, viewer.Token, nil)
		commentAssertStatus(t, recPage2, http.StatusOK)
		list2 := decodeJSON[commentListResp](t, recPage2)
		if len(list2.Items) != 2 {
			t.Fatalf("page 2 expected 2 items, got %d", len(list2.Items))
		}
		if list2.HasMore {
			t.Fatalf("page 2 expected hasMore=false")
		}
	})
}

// 5. DELETE comments: not found, own comment deletion, post author deletion, third party forbidden.
func TestIntegrationComments_DeleteComment(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("del_author")
	postID := env.createPost(author)

	commenter := env.registerUser("del_commenter")
	_ = env.createPost(commenter)

	thirdUser := env.registerUser("del_third")
	_ = env.createPost(thirdUser)

	t.Run("delete non-existent comment returns 404 interaction_not_found", func(t *testing.T) {
		rec := env.do(http.MethodDelete, "/api/comments/"+uuid.NewString(), commenter.Token, nil)
		commentAssertError(t, rec, http.StatusNotFound, "interaction_not_found")
	})

	t.Run("third party cannot delete another user's comment (403 interaction_forbidden)", func(t *testing.T) {
		recCreate := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": "comment to test third party delete",
		})
		commentAssertStatus(t, recCreate, http.StatusCreated)
		cResp := decodeJSON[commentResp](t, recCreate)

		recDel := env.do(http.MethodDelete, "/api/comments/"+cResp.ID, thirdUser.Token, nil)
		commentAssertError(t, recDel, http.StatusForbidden, "interaction_forbidden")

		// Verify comment still exists via GET
		recGet := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", commenter.Token, nil)
		list := decodeJSON[commentListResp](t, recGet)
		found := false
		for _, item := range list.Items {
			if item.ID == cResp.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected comment %s to still exist", cResp.ID)
		}
	})

	t.Run("comment author can delete own comment", func(t *testing.T) {
		recCreate := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": "comment to delete by author",
		})
		commentAssertStatus(t, recCreate, http.StatusCreated)
		cResp := decodeJSON[commentResp](t, recCreate)

		recDel := env.do(http.MethodDelete, "/api/comments/"+cResp.ID, commenter.Token, nil)
		commentAssertStatus(t, recDel, http.StatusNoContent)

		// Subsequent GET does not return it
		recGet := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", commenter.Token, nil)
		list := decodeJSON[commentListResp](t, recGet)
		for _, item := range list.Items {
			if item.ID == cResp.ID {
				t.Fatalf("comment %s should have been deleted", cResp.ID)
			}
		}

		// Subsequent DELETE returns 404 interaction_not_found
		recDelAgain := env.do(http.MethodDelete, "/api/comments/"+cResp.ID, commenter.Token, nil)
		commentAssertError(t, recDelAgain, http.StatusNotFound, "interaction_not_found")
	})

	t.Run("post author can delete others' comments on their post", func(t *testing.T) {
		recCreate := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", commenter.Token, map[string]string{
			"body": "comment to be removed by post owner",
		})
		commentAssertStatus(t, recCreate, http.StatusCreated)
		cResp := decodeJSON[commentResp](t, recCreate)

		// Post author deletes it
		recDel := env.do(http.MethodDelete, "/api/comments/"+cResp.ID, author.Token, nil)
		commentAssertStatus(t, recDel, http.StatusNoContent)

		// Subsequent GET does not return it
		recGet := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", author.Token, nil)
		list := decodeJSON[commentListResp](t, recGet)
		for _, item := range list.Items {
			if item.ID == cResp.ID {
				t.Fatalf("comment %s should have been deleted by post author", cResp.ID)
			}
		}
	})
}

// 6. RealMoji validation: missing image, invalid type, non-jpeg, oversized (>512x512), all 6 valid types.
func TestIntegrationComments_RealMojiValidation(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("rmval_author")
	postID := env.createPost(author)

	reactor := env.registerUser("rmval_reactor")
	_ = env.createPost(reactor)

	t.Run("missing image returns 400 invalid_realmoji", func(t *testing.T) {
		rec := commentPostRealMoji(env, postID, reactor.Token, "like", nil)
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_realmoji")
	})

	t.Run("invalid reactionType returns 400 invalid_realmoji", func(t *testing.T) {
		rec := commentPostRealMoji(env, postID, reactor.Token, "thumbsup", tinyJPEG())
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_realmoji")
	})

	t.Run("non-jpeg image returns 400 invalid_realmoji", func(t *testing.T) {
		rec := commentPostRealMoji(env, postID, reactor.Token, "like", tinyPNG())
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_realmoji")
	})

	t.Run("oversized image (>512x512) returns 400 invalid_realmoji", func(t *testing.T) {
		rec := commentPostRealMoji(env, postID, reactor.Token, "like", commentLargeJPEG())
		commentAssertError(t, rec, http.StatusBadRequest, "invalid_realmoji")
	})

	t.Run("all 6 allowed reaction types succeed with 201", func(t *testing.T) {
		validTypes := []string{"like", "smile", "surprise", "heart", "laugh", "instant"}
		for i, rt := range validTypes {
			// Register a distinct author for each post
			typeAuthor := env.registerUser(fmt.Sprintf("rmval_ta_%d", i))
			pID := env.createPost(typeAuthor)

			rec := commentPostRealMoji(env, pID, reactor.Token, rt, tinyJPEG())
			commentAssertStatus(t, rec, http.StatusCreated)
			resp := decodeJSON[commentRealMojiResp](t, rec)
			if resp.ReactionType != rt {
				t.Fatalf("expected reactionType %q, got %q", rt, resp.ReactionType)
			}
			if resp.Reactor.ID != reactor.ID {
				t.Fatalf("expected reactor ID %s, got %s", reactor.ID, resp.Reactor.ID)
			}
			if !strings.HasPrefix(resp.ImageURL, "/api/images/") {
				t.Fatalf("expected ImageURL to start with /api/images/, got %q", resp.ImageURL)
			}
		}
	})
}

// 7. RealMoji permissions: author reacting to own post forbidden, window, blocks, visibility.
func TestIntegrationComments_RealMojiPermissions(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("rmperm_author")
	postID := env.createPost(author)

	t.Run("author reacting to own post returns 403 interaction_forbidden", func(t *testing.T) {
		rec := commentPostRealMoji(env, postID, author.Token, "like", tinyJPEG())
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("reacting to non-existent post returns 403 interaction_forbidden", func(t *testing.T) {
		reactor := env.registerUser("rmperm_reactor1")
		_ = env.createPost(reactor)
		rec := commentPostRealMoji(env, uuid.NewString(), reactor.Token, "like", tinyJPEG())
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("reactor who has not posted in window returns 403 interaction_forbidden", func(t *testing.T) {
		lurker := env.registerUser("rmperm_lurker")
		rec := commentPostRealMoji(env, postID, lurker.Token, "like", tinyJPEG())
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("blocks prevent reacting in both directions", func(t *testing.T) {
		rA := env.registerUser("rmperm_blocked_a")
		_ = env.createPost(rA)
		commentCreateBlock(t, env, author, rA) // author blocked rA
		recA := commentPostRealMoji(env, postID, rA.Token, "like", tinyJPEG())
		commentAssertError(t, recA, http.StatusForbidden, "interaction_forbidden")

		rB := env.registerUser("rmperm_blocker_b")
		_ = env.createPost(rB)
		commentCreateBlock(t, env, rB, author) // rB blocked author
		recB := commentPostRealMoji(env, postID, rB.Token, "like", tinyJPEG())
		commentAssertError(t, recB, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("friends visibility allows friend and forbids non-friend", func(t *testing.T) {
		fa := env.registerUser("rmperm_fa")
		friendsPostID := commentCreatePostWithVisibility(t, env, fa, "friends")

		nonFriend := env.registerUser("rmperm_non_friend")
		_ = env.createPost(nonFriend)
		recNF := commentPostRealMoji(env, friendsPostID, nonFriend.Token, "like", tinyJPEG())
		commentAssertError(t, recNF, http.StatusForbidden, "interaction_forbidden")

		friend := env.registerUser("rmperm_friend")
		_ = env.createPost(friend)
		commentCreateFriendship(t, env, fa, friend)
		recF := commentPostRealMoji(env, friendsPostID, friend.Token, "like", tinyJPEG())
		commentAssertStatus(t, recF, http.StatusCreated)
	})

	t.Run("friends_of_friends visibility allows direct friend and mutual friend", func(t *testing.T) {
		fofa := env.registerUser("rmperm_fofa")
		fofPostID := commentCreatePostWithVisibility(t, env, fofa, "friends_of_friends")

		friend := env.registerUser("rmperm_fof_direct")
		_ = env.createPost(friend)
		commentCreateFriendship(t, env, fofa, friend)

		// Direct friend succeeds
		recDirect := commentPostRealMoji(env, fofPostID, friend.Token, "like", tinyJPEG())
		commentAssertStatus(t, recDirect, http.StatusCreated)

		// Mutual friend: fofa <-> friend <-> mutualFriend
		mutualFriend := env.registerUser("rmperm_fof_mutual")
		_ = env.createPost(mutualFriend)
		commentCreateFriendship(t, env, friend, mutualFriend)

		// In singlePostCanViewExpr, friends_of_friends includes mutual friends!
		recMutual := commentPostRealMoji(env, fofPostID, mutualFriend.Token, "heart", tinyJPEG())
		commentAssertStatus(t, recMutual, http.StatusCreated)

		// Unrelated user forbidden
		stranger := env.registerUser("rmperm_fof_stranger")
		_ = env.createPost(stranger)
		recStranger := commentPostRealMoji(env, fofPostID, stranger.Token, "smile", tinyJPEG())
		commentAssertError(t, recStranger, http.StatusForbidden, "interaction_forbidden")
	})

	t.Run("ended posting window forbids realmoji", func(t *testing.T) {
		reactor := env.registerUser("rmperm_w_reactor")
		_ = env.createPost(reactor)

		_, err := env.DB.Exec(`UPDATE posting_windows SET ended_at = clock_timestamp() WHERE ended_at IS NULL`)
		if err != nil {
			t.Fatalf("failed to end window: %v", err)
		}

		rec := commentPostRealMoji(env, postID, reactor.Token, "like", tinyJPEG())
		commentAssertError(t, rec, http.StatusForbidden, "interaction_forbidden")
	})
}

// 8. RealMoji replace semantics and GET realmojis: replaces old, ordering, author GET, pagination.
func TestIntegrationComments_RealMojiReplaceSemanticsAndGet(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("rmrep_author")
	postID := env.createPost(author)

	reactor1 := env.registerUser("rmrep_reactor1")
	_ = env.createPost(reactor1)

	reactor2 := env.registerUser("rmrep_reactor2")
	_ = env.createPost(reactor2)

	// Step 1: reactor1 reacts with "like"
	rec1 := commentPostRealMoji(env, postID, reactor1.Token, "like", tinyJPEG())
	commentAssertStatus(t, rec1, http.StatusCreated)
	rm1 := decodeJSON[commentRealMojiResp](t, rec1)

	// Check GET returns rm1
	recGet1 := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", reactor1.Token, nil)
	commentAssertStatus(t, recGet1, http.StatusOK)
	list1 := decodeJSON[commentRealMojiListResp](t, recGet1)
	if len(list1.Items) != 1 || list1.Items[0].ID != rm1.ID {
		t.Fatalf("expected 1 realmoji %s, got %v", rm1.ID, list1.Items)
	}

	// Step 2: reactor1 reacts again with "heart" on the same post -> should replace rm1
	rec2 := commentPostRealMoji(env, postID, reactor1.Token, "heart", tinyJPEG())
	commentAssertStatus(t, rec2, http.StatusCreated)
	rm2 := decodeJSON[commentRealMojiResp](t, rec2)
	if rm2.ID == rm1.ID {
		t.Fatalf("expected new realmoji ID on replace")
	}

	// Verify in DB that rm1 has replaced_at set, while rm2 has replaced_at IS NULL
	var rm1ReplacedAt, rm2ReplacedAt *time.Time
	err := env.DB.Get(&rm1ReplacedAt, `SELECT replaced_at FROM realmojis WHERE id = $1`, rm1.ID)
	if err != nil || rm1ReplacedAt == nil {
		t.Fatalf("expected rm1 replaced_at to be non-nil, err: %v", err)
	}
	err = env.DB.Get(&rm2ReplacedAt, `SELECT replaced_at FROM realmojis WHERE id = $1`, rm2.ID)
	if err != nil || rm2ReplacedAt != nil {
		t.Fatalf("expected rm2 replaced_at to be nil, got %v", rm2ReplacedAt)
	}

	// Check GET returns ONLY rm2 for reactor1
	recGet2 := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", reactor1.Token, nil)
	commentAssertStatus(t, recGet2, http.StatusOK)
	list2 := decodeJSON[commentRealMojiListResp](t, recGet2)
	if len(list2.Items) != 1 || list2.Items[0].ID != rm2.ID {
		t.Fatalf("expected only replaced realmoji %s, got %v", rm2.ID, list2.Items)
	}
	if list2.Items[0].ReactionType != "heart" {
		t.Fatalf("expected reactionType 'heart', got %q", list2.Items[0].ReactionType)
	}

	// Step 3: reactor2 reacts with "smile"
	rec3 := commentPostRealMoji(env, postID, reactor2.Token, "smile", tinyJPEG())
	commentAssertStatus(t, rec3, http.StatusCreated)
	rm3 := decodeJSON[commentRealMojiResp](t, rec3)

	// Step 4: GET returns 2 realmojis ordered by created_at DESC, id DESC
	recGet3 := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", reactor2.Token, nil)
	commentAssertStatus(t, recGet3, http.StatusOK)
	list3 := decodeJSON[commentRealMojiListResp](t, recGet3)
	if len(list3.Items) != 2 {
		t.Fatalf("expected 2 active realmojis, got %d", len(list3.Items))
	}
	if list3.Items[0].ID != rm3.ID || list3.Items[1].ID != rm2.ID {
		t.Fatalf("expected items in DESC order [%s, %s], got [%s, %s]",
			rm3.ID, rm2.ID, list3.Items[0].ID, list3.Items[1].ID)
	}

	// Post author can also call GET realmojis on their post
	recAuthorGet := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", author.Token, nil)
	commentAssertStatus(t, recAuthorGet, http.StatusOK)

	// Pagination validation
	nowStr := url.QueryEscape(time.Now().UTC().Format(time.RFC3339Nano))
	recBad1 := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis?beforeCreatedAt="+nowStr, author.Token, nil)
	commentAssertError(t, recBad1, http.StatusBadRequest, "invalid_pagination")

	recBad2 := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis?beforeId="+rm3.ID, author.Token, nil)
	commentAssertError(t, recBad2, http.StatusBadRequest, "invalid_pagination")

	// Pagination with >50 realmojis
	t.Run("pagination with over 50 realmojis", func(t *testing.T) {
		bulkAuthor := env.registerUser("rmrep_bulk_author")
		bulkPostID := env.createPost(bulkAuthor)

		var authorSecretKeyHash string
		if err := env.DB.Get(&authorSecretKeyHash, `SELECT secret_key_hash FROM users WHERE id = $1`, author.ID); err != nil {
			t.Fatalf("failed to query author secret_key_hash: %v", err)
		}

		baseTime := time.Now().UTC().Add(-1 * time.Hour)
		// Insert 52 users and realmojis
		for i := 0; i < 52; i++ {
			uID := uuid.NewString()
			_, err := env.DB.Exec(`
				INSERT INTO users (id, username, secret_key_hash, display_name, region, account_type, status)
				VALUES ($1, $2, $3, $4, 'asia', 'regular', 'active')
			`, uID, fmt.Sprintf("bulk_rm_user_%d", i), authorSecretKeyHash, fmt.Sprintf("Bulk User %d", i))
			if err != nil {
				t.Fatalf("failed to insert bulk user: %v", err)
			}

			rmID := uuid.NewString()
			rmTime := baseTime.Add(time.Duration(i) * time.Second)
			_, err = env.DB.Exec(`
				INSERT INTO realmojis (id, post_id, reactor_id, image_id, reaction_type, created_at)
				VALUES ($1, $2, $3, $4, 'like', $5)
			`, rmID, bulkPostID, uID, uuid.NewString(), rmTime)
			if err != nil {
				t.Fatalf("failed to insert bulk realmoji: %v", err)
			}
		}

		// First page: returns 50 items and hasMore = true
		recP1 := env.do(http.MethodGet, "/api/posts/"+bulkPostID+"/realmojis", author.Token, nil)
		commentAssertStatus(t, recP1, http.StatusOK)
		listP1 := decodeJSON[commentRealMojiListResp](t, recP1)
		if len(listP1.Items) != 50 {
			t.Fatalf("page 1 expected 50 items, got %d", len(listP1.Items))
		}
		if !listP1.HasMore {
			t.Fatalf("page 1 expected hasMore=true")
		}

		// Second page: query using beforeCreatedAt & beforeId of 50th item
		lastItem := listP1.Items[49]
		p2URL := fmt.Sprintf("/api/posts/%s/realmojis?beforeCreatedAt=%s&beforeId=%s",
			bulkPostID,
			url.QueryEscape(lastItem.CreatedAt.Format(time.RFC3339Nano)),
			url.QueryEscape(lastItem.ID),
		)
		recP2 := env.do(http.MethodGet, p2URL, author.Token, nil)
		commentAssertStatus(t, recP2, http.StatusOK)
		listP2 := decodeJSON[commentRealMojiListResp](t, recP2)
		if len(listP2.Items) != 2 {
			t.Fatalf("page 2 expected 2 items, got %d", len(listP2.Items))
		}
		if listP2.HasMore {
			t.Fatalf("page 2 expected hasMore=false")
		}
	})
}

// 9. DELETE realmoji: not found, reactor deletion, post author deletion, third party forbidden, deleting replaced realmoji.
func TestIntegrationComments_DeleteRealMoji(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("rmdel_author")
	postID := env.createPost(author)

	reactor := env.registerUser("rmdel_reactor")
	_ = env.createPost(reactor)

	thirdUser := env.registerUser("rmdel_third")
	_ = env.createPost(thirdUser)

	t.Run("delete non-existent realmoji returns 404 interaction_not_found", func(t *testing.T) {
		rec := env.do(http.MethodDelete, "/api/realmojis/"+uuid.NewString(), reactor.Token, nil)
		commentAssertError(t, rec, http.StatusNotFound, "interaction_not_found")
	})

	t.Run("third party cannot delete reactor's realmoji (403 interaction_forbidden)", func(t *testing.T) {
		recCreate := commentPostRealMoji(env, postID, reactor.Token, "like", tinyJPEG())
		commentAssertStatus(t, recCreate, http.StatusCreated)
		rm := decodeJSON[commentRealMojiResp](t, recCreate)

		recDel := env.do(http.MethodDelete, "/api/realmojis/"+rm.ID, thirdUser.Token, nil)
		commentAssertError(t, recDel, http.StatusForbidden, "interaction_forbidden")

		// Verify realmoji still exists
		recGet := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", reactor.Token, nil)
		list := decodeJSON[commentRealMojiListResp](t, recGet)
		found := false
		for _, item := range list.Items {
			if item.ID == rm.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected realmoji %s to still exist", rm.ID)
		}
	})

	t.Run("reactor can delete own realmoji", func(t *testing.T) {
		delAuthor2 := env.registerUser("rmdel_author2")
		pID := env.createPost(delAuthor2)
		recCreate := commentPostRealMoji(env, pID, reactor.Token, "smile", tinyJPEG())
		commentAssertStatus(t, recCreate, http.StatusCreated)
		rm := decodeJSON[commentRealMojiResp](t, recCreate)

		recDel := env.do(http.MethodDelete, "/api/realmojis/"+rm.ID, reactor.Token, nil)
		commentAssertStatus(t, recDel, http.StatusNoContent)

		// Subsequent GET does not return it
		recGet := env.do(http.MethodGet, "/api/posts/"+pID+"/realmojis", reactor.Token, nil)
		list := decodeJSON[commentRealMojiListResp](t, recGet)
		for _, item := range list.Items {
			if item.ID == rm.ID {
				t.Fatalf("realmoji %s should have been deleted", rm.ID)
			}
		}

		// Subsequent DELETE returns 404
		recDelAgain := env.do(http.MethodDelete, "/api/realmojis/"+rm.ID, reactor.Token, nil)
		commentAssertError(t, recDelAgain, http.StatusNotFound, "interaction_not_found")
	})

	t.Run("post author can delete reactor's realmoji", func(t *testing.T) {
		delAuthor3 := env.registerUser("rmdel_author3")
		pID := env.createPost(delAuthor3)
		recCreate := commentPostRealMoji(env, pID, reactor.Token, "surprise", tinyJPEG())
		commentAssertStatus(t, recCreate, http.StatusCreated)
		rm := decodeJSON[commentRealMojiResp](t, recCreate)

		recDel := env.do(http.MethodDelete, "/api/realmojis/"+rm.ID, delAuthor3.Token, nil)
		commentAssertStatus(t, recDel, http.StatusNoContent)

		// Subsequent GET does not return it
		recGet := env.do(http.MethodGet, "/api/posts/"+pID+"/realmojis", delAuthor3.Token, nil)
		list := decodeJSON[commentRealMojiListResp](t, recGet)
		for _, item := range list.Items {
			if item.ID == rm.ID {
				t.Fatalf("realmoji %s should have been deleted by post author", rm.ID)
			}
		}
	})

	t.Run("deleting a replaced realmoji succeeds and marks it deleted", func(t *testing.T) {
		delAuthor4 := env.registerUser("rmdel_author4")
		pID := env.createPost(delAuthor4)
		// Reactor reacts with "like"
		rec1 := commentPostRealMoji(env, pID, reactor.Token, "like", tinyJPEG())
		rm1 := decodeJSON[commentRealMojiResp](t, rec1)

		// Reactor replaces with "laugh"
		rec2 := commentPostRealMoji(env, pID, reactor.Token, "laugh", tinyJPEG())
		rm2 := decodeJSON[commentRealMojiResp](t, rec2)

		// Delete the replaced realmoji rm1
		recDel := env.do(http.MethodDelete, "/api/realmojis/"+rm1.ID, reactor.Token, nil)
		commentAssertStatus(t, recDel, http.StatusNoContent)

		// Active rm2 is still present in GET
		recGet := env.do(http.MethodGet, "/api/posts/"+pID+"/realmojis", reactor.Token, nil)
		list := decodeJSON[commentRealMojiListResp](t, recGet)
		if len(list.Items) != 1 || list.Items[0].ID != rm2.ID {
			t.Fatalf("expected rm2 %s to remain active, got %v", rm2.ID, list.Items)
		}
	})
}
