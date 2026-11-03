package isureal

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// Concurrent comments on the same author's post must all succeed: the post author's users row
// is locked FOR SHARE, so commenters do not serialize on (or deadlock against) each other.
func TestIntegrationConcurrency_CommentsOnSameAuthor(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("conc_author")
	postID := env.createPost(author)

	const commenters = 8
	users := make([]testUser, commenters)
	for i := range users {
		users[i] = env.registerUser(fmt.Sprintf("conc_commenter_%d", i))
		_ = env.createPost(users[i])
	}

	codes := make([]int, commenters)
	bodies := make([]string, commenters)
	var wg sync.WaitGroup
	for i, u := range users {
		wg.Add(1)
		go func(i int, u testUser) {
			defer wg.Done()
			rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", u.Token, map[string]string{
				"body": fmt.Sprintf("concurrent comment %d", i),
			})
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i, u)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusCreated {
			t.Fatalf("commenter %d: expected 201, got %d: %s", i, code, bodies[i])
		}
	}

	var count int
	if err := env.DB.Get(&count, `SELECT count(*) FROM comments WHERE post_id = $1 AND deleted_at IS NULL`, postID); err != nil {
		t.Fatalf("failed to count comments: %v", err)
	}
	if count != commenters {
		t.Fatalf("expected %d comments, got %d", commenters, count)
	}
}

// Logout runs as a single statement; the old token must stop working while other sessions stay valid.
func TestIntegrationConcurrency_LogoutThenReuseToken(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("conc_logout_user")
	other := env.registerUser("conc_logout_other")

	if rec := env.do(http.MethodPost, "/api/auth/logout", user.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodGet, "/api/me", user.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me after logout expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodPost, "/api/auth/logout", user.Token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("second logout expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodGet, "/api/me", other.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("other user's session expected to stay valid, got %d: %s", rec.Code, rec.Body.String())
	}
}
