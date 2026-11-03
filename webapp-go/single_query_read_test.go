package isureal

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// --- unit tests (no DB) ---

func TestCoalescedUserColumns(t *testing.T) {
	got := coalescedUserColumns("x", "author")
	if !strings.Contains(got, `COALESCE(x."author.id", '') AS "author.id"`) {
		t.Fatalf("id must be coalesced: %s", got)
	}
	if !strings.Contains(got, `x."author.profile_image_id" AS "author.profile_image_id"`) ||
		strings.Contains(got, `COALESCE(x."author.profile_image_id"`) {
		t.Fatalf("profile_image_id is nullable and must stay NULL: %s", got)
	}
	if n := strings.Count(got, `AS "author.`); n != len(userColumns) {
		t.Fatalf("expected %d columns, got %d", len(userColumns), n)
	}
}

func TestViewerPostedTodayInlinedIntoPermissionSQL(t *testing.T) {
	posted := viewerPostedTodaySQL("$2")
	for name, q := range map[string]string{"canViewPostSQL": canViewPostSQL, "canCommentSQL": canCommentSQL} {
		if !strings.Contains(q, posted) {
			t.Fatalf("%s must inline the posted-today check for viewer $2", name)
		}
	}
}

func TestPostingWindowWarmupRow_NullsBecomeNil(t *testing.T) {
	r := postingWindowWarmupRow{UserID: "u"}
	if r.window() != nil || r.post() != nil {
		t.Fatalf("row without window/post must map to nil")
	}
}

func warmupTestRow(userID string) postingWindowWarmupRow {
	now := time.Now()
	wid, region, pid, img, caption, vis, kind := "w1", "asia", "p1", "img1", "c", "public", "none"
	return postingWindowWarmupRow{
		UserID: userID, WID: &wid, WRegion: &region, WLocalDate: &now, WStartsAt: &now, WOnTimeDeadline: &now,
		Active: 1, Deleted: 1,
		PID: &pid, PAuthorID: &userID, PPostingWindowID: &wid, PImageID: &img, PCaption: &caption,
		PVisibility: &vis, PLocationKind: &kind, PPostedAt: &now, POnTimeDeadline: &now,
	}
}

func TestApplyPostingWindowWarmup_WritesActualState(t *testing.T) {
	InvalidateAllPostingWindowCache()
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllPostingWindowCache()
	defer InvalidateAllMyCurrentPostCache()

	pwGen := atomic.LoadUint64(&postingWindowCacheGen)
	mpGen := atomic.LoadUint64(&myCurrentPostCacheGen)
	applyPostingWindowWarmup([]postingWindowWarmupRow{warmupTestRow("u-warm"), {UserID: "u-nowin"}}, time.Now(), pwGen, mpGen)

	postingWindowCacheMu.RLock()
	st, ok := postingWindowCacheByUserID["u-warm"]
	nowin, ok2 := postingWindowCacheByUserID["u-nowin"]
	postingWindowCacheMu.RUnlock()
	if !ok || st.window == nil || st.window.ID != "w1" || st.active != 1 || st.deleted != 1 {
		t.Fatalf("unexpected posting window state: %+v ok=%v", st, ok)
	}
	if !ok2 || nowin.window != nil {
		t.Fatalf("user without open window must be cached as nil window: %+v ok=%v", nowin, ok2)
	}

	myCurrentPostCacheMu.RLock()
	mp, ok := myCurrentPostCacheByUserID["u-warm"]
	myCurrentPostCacheMu.RUnlock()
	if !ok || mp.post == nil || mp.post.ID != "p1" {
		t.Fatalf("unexpected my current post: %+v ok=%v", mp, ok)
	}
}

func TestApplyPostingWindowWarmup_DiscardsAfterInvalidation(t *testing.T) {
	InvalidateAllPostingWindowCache()
	InvalidateAllMyCurrentPostCache()
	defer InvalidateAllPostingWindowCache()
	defer InvalidateAllMyCurrentPostCache()

	pwGen := atomic.LoadUint64(&postingWindowCacheGen)
	mpGen := atomic.LoadUint64(&myCurrentPostCacheGen)
	// a post/delete landed while the warmup query was in flight
	InvalidatePostingWindowCacheForUser("someone")
	InvalidateMyCurrentPostCacheForUser("someone")
	applyPostingWindowWarmup([]postingWindowWarmupRow{warmupTestRow("u-stale")}, time.Now(), pwGen, mpGen)

	postingWindowCacheMu.RLock()
	_, ok := postingWindowCacheByUserID["u-stale"]
	postingWindowCacheMu.RUnlock()
	myCurrentPostCacheMu.RLock()
	_, ok2 := myCurrentPostCacheByUserID["u-stale"]
	myCurrentPostCacheMu.RUnlock()
	if ok || ok2 {
		t.Fatalf("warmup that raced with an invalidation must not be stored")
	}
}

func TestHandleResetNotify_OnlyOnInitializePayload(t *testing.T) {
	orig := resetWarmup
	defer func() { resetWarmup = orig }()
	called := make(chan struct{}, 4)
	resetWarmup = func(context.Context, *sqlx.DB) { called <- struct{}{} }

	h := HandleResetNotify(nil)
	h("") // LISTEN reconnect replay
	h("other")
	select {
	case <-called:
		t.Fatalf("warmup must not run for reconnect replay / unknown payload")
	case <-time.After(50 * time.Millisecond):
	}

	h(resetNotifyPayload)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatalf("warmup must run on reset notification")
	}
}

// --- integration tests (ISUREAL_TEST_DB=1) ---

func TestIntegrationSingleQuery_CommentsAndRealMojis(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	author := env.registerUser("sq_author")
	viewer := env.registerUser("sq_viewer")
	postID := env.createPost(author)

	// viewer has not posted today: forbidden, without reading rows
	if rec := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", viewer.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("comments before posting: want 403, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", viewer.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("realmojis before posting: want 403, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get post before posting: want 404, got %d %s", rec.Code, rec.Body.String())
	}

	env.createPost(viewer)

	// allowed but empty: the sentinel row must yield 200 with no items
	rec := env.do(http.MethodGet, "/api/posts/"+postID+"/comments", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty comments: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeJSON[commentListResp](t, rec); len(got.Items) != 0 || got.HasMore {
		t.Fatalf("empty comments: unexpected %+v", got)
	}
	rec = env.do(http.MethodGet, "/api/posts/"+postID+"/realmojis", viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty realmojis: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeJSON[commentRealMojiListResp](t, rec); len(got.Items) != 0 || got.HasMore {
		t.Fatalf("empty realmojis: unexpected %+v", got)
	}
	if rec := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("get post after posting: want 200, got %d %s", rec.Code, rec.Body.String())
	}

	for _, body := range []string{"first", "second"} {
		if rec := env.do(http.MethodPost, "/api/posts/"+postID+"/comments", viewer.Token, map[string]any{"body": body}); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("create comment: got %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = env.do(http.MethodGet, "/api/posts/"+postID+"/comments", viewer.Token, nil)
	got := decodeJSON[commentListResp](t, rec)
	if rec.Code != http.StatusOK || len(got.Items) != 2 || got.Items[0].Body != "first" || got.Items[1].Body != "second" {
		t.Fatalf("comments: want [first second], got %d %+v", rec.Code, got)
	}
	if got.Items[0].Author.ID != viewer.ID || got.Items[0].Author.Username != viewer.Username {
		t.Fatalf("comment author not mapped: %+v", got.Items[0].Author)
	}

	// cursor past the last comment: allowed, empty
	last := got.Items[1]
	path := "/api/posts/" + postID + "/comments?afterCreatedAt=" + strings.ReplaceAll(last.CreatedAt.Format(time.RFC3339Nano), "+", "%2B") + "&afterId=" + last.ID
	rec = env.do(http.MethodGet, path, viewer.Token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("comments after cursor: want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if after := decodeJSON[commentListResp](t, rec); len(after.Items) != 0 {
		t.Fatalf("comments after cursor: want empty, got %+v", after)
	}
}

func TestIntegrationSingleQuery_FeedRequiresPostedToday(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	author := env.registerUser("sqf_author")
	viewer := env.registerUser("sqf_viewer")
	postID := env.createPost(author)

	type feedResp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	for _, feed := range []string{"public", "friends", "following", "friends-of-friends"} {
		rec := env.do(http.MethodGet, "/api/feeds/"+feed, viewer.Token, nil)
		if rec.Code != http.StatusOK || len(decodeJSON[feedResp](t, rec).Items) != 0 {
			t.Fatalf("%s feed before posting: want 200 empty, got %d %s", feed, rec.Code, rec.Body.String())
		}
	}

	env.createPost(viewer)
	rec := env.do(http.MethodGet, "/api/feeds/public", viewer.Token, nil)
	items := decodeJSON[feedResp](t, rec).Items
	if rec.Code != http.StatusOK || len(items) != 1 || items[0].ID != postID {
		t.Fatalf("public feed after posting: want [%s], got %d %s", postID, rec.Code, rec.Body.String())
	}
	// no friends: still 200 and empty (friend IDs are now resolved inside the feed query)
	rec = env.do(http.MethodGet, "/api/feeds/friends", viewer.Token, nil)
	if rec.Code != http.StatusOK || len(decodeJSON[feedResp](t, rec).Items) != 0 {
		t.Fatalf("friends feed without friends: want 200 empty, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationWarmupPostingWindowCache_ReflectsExistingPosts(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	poster := env.registerUser("sqw_poster")
	idle := env.registerUser("sqw_idle")
	postID := env.createPost(poster)

	InvalidateAllPostingWindowCache()
	InvalidateAllMyCurrentPostCache()
	if err := WarmupPostingWindowCache(context.Background(), env.DB); err != nil {
		t.Fatalf("warmup: %v", err)
	}

	postingWindowCacheMu.RLock()
	ps, ok1 := postingWindowCacheByUserID[poster.ID]
	is, ok2 := postingWindowCacheByUserID[idle.ID]
	postingWindowCacheMu.RUnlock()
	if !ok1 || ps.window == nil || ps.active != 1 {
		t.Fatalf("poster must be warmed as posted: %+v ok=%v", ps, ok1)
	}
	if !ok2 || is.window == nil || is.active != 0 {
		t.Fatalf("idle user must be warmed as not posted: %+v ok=%v", is, ok2)
	}

	myCurrentPostCacheMu.RLock()
	pm, ok1 := myCurrentPostCacheByUserID[poster.ID]
	im, ok2 := myCurrentPostCacheByUserID[idle.ID]
	myCurrentPostCacheMu.RUnlock()
	if !ok1 || pm.post == nil || pm.post.ID != postID {
		t.Fatalf("poster's current post must be warmed: %+v ok=%v", pm, ok1)
	}
	if !ok2 || im.post != nil {
		t.Fatalf("idle user's current post must be warmed as none: %+v ok=%v", im, ok2)
	}

	// served from the warmed cache, and identical to the DB path
	rec := env.do(http.MethodGet, "/api/posts/me/current", poster.Token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), postID) {
		t.Fatalf("my current post from warm cache: got %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodGet, "/api/posts/me/current", idle.Token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("idle my current post from warm cache: want 404, got %d", rec.Code)
	}
}
