package isureal

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// friend_edges (030-friend-edges.sql) must mirror friendships in both directions.

func friendEdgesOf(t *testing.T, env *testEnv, userID string) []string {
	t.Helper()
	var ids []string
	if err := env.DB.Select(&ids, `SELECT friend_id FROM friend_edges WHERE user_id = $1 ORDER BY friend_id`, userID); err != nil {
		t.Fatalf("select friend_edges: %v", err)
	}
	return ids
}

func assertEdgesMatchFriendships(t *testing.T, env *testEnv) {
	t.Helper()
	var mismatches int
	if err := env.DB.Get(&mismatches, `
		SELECT count(*) FROM (
			(SELECT user_id, friend_id FROM friend_edges
			 EXCEPT
			 SELECT user_low_id, user_high_id FROM friendships
			 EXCEPT
			 SELECT user_high_id, user_low_id FROM friendships)
			UNION ALL
			(SELECT user_low_id, user_high_id FROM friendships
			 UNION ALL
			 SELECT user_high_id, user_low_id FROM friendships
			 EXCEPT
			 SELECT user_id, friend_id FROM friend_edges)
		) d`); err != nil {
		t.Fatalf("compare friend_edges: %v", err)
	}
	if mismatches != 0 {
		t.Fatalf("friend_edges out of sync with friendships: %d rows differ", mismatches)
	}
}

func TestFriendEdges_FollowFriendshipLifecycle(t *testing.T) {
	env := newTestEnv(t)
	a := env.registerUser("fe_a")
	b := env.registerUser("fe_b")
	c := env.registerUser("fe_c")

	// accept via API (stored functions) and a direct INSERT both maintain the edges
	rec := env.do(http.MethodPost, "/api/friend-requests", a.Token, map[string]string{"recipientId": b.ID})
	if rec.Code != http.StatusCreated {
		t.Fatalf("friend request: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodPost, "/api/friend-requests", b.Token, map[string]string{"recipientId": a.ID}); rec.Code != http.StatusCreated {
		t.Fatalf("mutual friend request: %d %s", rec.Code, rec.Body.String())
	}
	feedAddFriendship(t, env.DB, a, c)

	wantA := []string{b.ID, c.ID}
	sort.Strings(wantA)
	if got := friendEdgesOf(t, env, a.ID); strings.Join(got, ",") != strings.Join(wantA, ",") {
		t.Fatalf("edges of a: want %v, got %v", wantA, got)
	}
	if got := friendEdgesOf(t, env, b.ID); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("edges of b: want [%s], got %v", a.ID, got)
	}
	assertEdgesMatchFriendships(t, env)

	// unfriend removes both directions
	if rec := env.do(http.MethodDelete, "/api/friendships/"+b.ID, a.Token, nil); rec.Code >= 300 {
		t.Fatalf("delete friendship: %d %s", rec.Code, rec.Body.String())
	}
	if got := friendEdgesOf(t, env, b.ID); len(got) != 0 {
		t.Fatalf("edges of b after unfriend: want none, got %v", got)
	}
	if got := friendEdgesOf(t, env, a.ID); len(got) != 1 || got[0] != c.ID {
		t.Fatalf("edges of a after unfriend: want [%s], got %v", c.ID, got)
	}
	assertEdgesMatchFriendships(t, env)

	// block removes the friendship (and therefore the edges)
	if rec := env.do(http.MethodPut, "/api/blocks/"+c.ID, a.Token, nil); rec.Code >= 300 {
		t.Fatalf("block: %d %s", rec.Code, rec.Body.String())
	}
	assertEdgesMatchFriendships(t, env)

	// updated_at-only updates keep the edges
	feedAddFriendship(t, env.DB, b, c)
	if _, err := env.DB.Exec(`UPDATE friendships SET updated_at = clock_timestamp()`); err != nil {
		t.Fatalf("touch friendships: %v", err)
	}
	assertEdgesMatchFriendships(t, env)

	// TRUNCATE friendships clears the edges
	if _, err := env.DB.Exec(`TRUNCATE friendships`); err != nil {
		t.Fatalf("truncate friendships: %v", err)
	}
	var n int
	if err := env.DB.Get(&n, `SELECT count(*) FROM friend_edges`); err != nil || n != 0 {
		t.Fatalf("friend_edges after truncate: want 0, got %d (%v)", n, err)
	}
}

func TestFriendEdges_FriendsOfFriendsFeedUsesEdges(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	viewer := env.registerUser("fe_fof_viewer")
	mid := env.registerUser("fe_fof_mid")
	far := env.registerUser("fe_fof_far")
	feedAddFriendship(t, env.DB, viewer, mid)
	feedAddFriendship(t, env.DB, mid, far)
	farPost := env.createPost(far)
	env.createPost(viewer)

	type feedResp struct {
		Items []struct {
			ID    string `json:"id"`
			Score *int64 `json:"score"`
		} `json:"items"`
	}
	rec := env.do(http.MethodGet, "/api/feeds/friends-of-friends", viewer.Token, nil)
	got := decodeJSON[feedResp](t, rec)
	if rec.Code != http.StatusOK || len(got.Items) != 1 || got.Items[0].ID != farPost {
		t.Fatalf("fof feed: want [%s], got %d %s", farPost, rec.Code, rec.Body.String())
	}
	// 1 mutual friend (3 points) + far's neighbour mid has mutual count 0 → score 3
	if got.Items[0].Score == nil || *got.Items[0].Score != 3 {
		t.Fatalf("fof score: want 3, got %v", got.Items[0].Score)
	}
	if rec := env.do(http.MethodGet, "/api/posts/"+farPost, viewer.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("public post by friend-of-friend must be visible: %d", rec.Code)
	}

	// once mid unfriends far, far is no longer a friend-of-friend
	if _, err := env.DB.Exec(`DELETE FROM friendships WHERE user_low_id = LEAST($1, $2) AND user_high_id = GREATEST($1, $2)`, mid.ID, far.ID); err != nil {
		t.Fatalf("delete friendship: %v", err)
	}
	rec = env.do(http.MethodGet, "/api/feeds/friends-of-friends", viewer.Token, nil)
	if got := decodeJSON[feedResp](t, rec); rec.Code != http.StatusOK || len(got.Items) != 0 {
		t.Fatalf("fof feed after unfriend: want empty, got %d %s", rec.Code, rec.Body.String())
	}
}
