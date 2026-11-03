package isureal

import (
	"net/http"
	"testing"
)

func TestSmokeRegisterAndGetMe(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("smoke_user_me")
	if user.Token == "" {
		t.Fatalf("expected non-empty token")
	}

	rec := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/me expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	type meResp struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Region      string `json:"region"`
	}
	me := decodeJSON[meResp](t, rec)

	if me.ID != user.ID {
		t.Fatalf("expected user ID %s, got %s", user.ID, me.ID)
	}
	if me.Username != "smoke_user_me" {
		t.Fatalf("expected username smoke_user_me, got %s", me.Username)
	}
	if me.Region != "asia" {
		t.Fatalf("expected region asia, got %s", me.Region)
	}
}

func TestSmokeUnauthorizedGetMe(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(http.MethodGet, "/api/me", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me without token expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	recBad := env.do(http.MethodGet, "/api/me", "invalid-token", nil)
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me with invalid token expected 401, got %d: %s", recBad.Code, recBad.Body.String())
	}
}

func TestSmokePostingWindowAndCreatePost(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("smoke_poster")
	postID := env.createPost(user)
	if postID == "" {
		t.Fatalf("expected non-empty post ID")
	}

	rec := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/posts/:id expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	type postResp struct {
		ID     string `json:"id"`
		Author struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"author"`
		Visibility string `json:"visibility"`
	}
	post := decodeJSON[postResp](t, rec)

	if post.ID != postID {
		t.Fatalf("expected post ID %s, got %s", postID, post.ID)
	}
	if post.Author.ID != user.ID {
		t.Fatalf("expected author ID %s, got %s", user.ID, post.Author.ID)
	}
	if post.Author.Username != "smoke_poster" {
		t.Fatalf("expected author username smoke_poster, got %s", post.Author.Username)
	}
}
