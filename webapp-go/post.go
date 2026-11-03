package isureal

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
)

type PostLocation struct {
	Kind      string   `json:"kind" db:"location_kind"`
	Latitude  *float64 `json:"latitude,omitempty" db:"latitude"`
	Longitude *float64 `json:"longitude,omitempty" db:"longitude"`
	Locality  *string  `json:"locality,omitempty" db:"locality"`
}

func (location PostLocation) valid() bool {
	switch location.Kind {
	case "none":
		return location.Latitude == nil && location.Longitude == nil && location.Locality == nil
	case "exact":
		return location.Latitude != nil && location.Longitude != nil && location.Locality == nil &&
			*location.Latitude >= -90 && *location.Latitude <= 90 && *location.Longitude >= -180 && *location.Longitude <= 180
	case "approximate":
		return location.Latitude == nil && location.Longitude == nil && location.Locality != nil && validUserText(*location.Locality, 1, 100)
	default:
		return false
	}
}

type Post struct {
	ID              string `db:"id"`
	AuthorID        string `db:"author_id"`
	PostingWindowID string `db:"posting_window_id"`
	ImageID         string `db:"image_id"`
	Caption         string `db:"caption"`
	Visibility      string `db:"visibility"`
	PostLocation
	PostedAt       time.Time  `db:"posted_at"`
	OnTimeDeadline time.Time  `db:"on_time_deadline"`
	WindowEndedAt  *time.Time `db:"window_ended_at"`
}

const postColumns = `p.id, p.author_id, p.posting_window_id, p.image_id, p.caption, p.visibility,
  p.location_kind, p.latitude, p.longitude, p.locality, p.posted_at,
  w.on_time_deadline, w.ended_at AS window_ended_at`

func getPost(ctx context.Context, db sqlx.QueryerContext, postID string) (Post, error) {
	var post Post
	err := sqlx.GetContext(ctx, db, &post, `
  SELECT `+postColumns+`
  FROM posts p JOIN posting_windows w ON w.id = p.posting_window_id
  WHERE p.id = $1 AND p.deleted_at IS NULL
 `, postID)
	return post, err
}

type PostWithAuthor struct {
	Post
	Author User `db:"author"`
}

func selectPostsWithAuthor(ctx context.Context, db sqlx.QueryerContext, conditions string, args ...any) ([]PostWithAuthor, error) {
	posts := make([]PostWithAuthor, 0, 128)
	if err := sqlx.SelectContext(ctx, db, &posts, `
  SELECT `+postColumns+`, `+userColumnsAs("u", "author")+`
  FROM posts p
  JOIN posting_windows w ON w.id = p.posting_window_id
  JOIN users u ON u.id = p.author_id AND u.status = 'active'
  WHERE p.deleted_at IS NULL `+conditions, args...); err != nil {
		return nil, err
	}
	for i := range posts {
		if err := posts[i].Author.setImageKey(); err != nil {
			return nil, err
		}
	}
	return posts, nil
}

func getPostWithAuthor(ctx context.Context, db sqlx.QueryerContext, postID string) (PostWithAuthor, error) {
	posts, err := selectPostsWithAuthor(ctx, db, `AND p.id = $1`, postID)
	if err != nil {
		return PostWithAuthor{}, err
	}
	if len(posts) == 0 {
		return PostWithAuthor{}, sql.ErrNoRows
	}
	return posts[0], nil
}

// viewerPostedTodaySQL is true when the viewer (param) has an active post in any region's
// posting window whose local_date matches the viewer's region's current window. It is
// inlined into read queries so the check costs no extra DB round trip.
func viewerPostedTodaySQL(param string) string {
	return `EXISTS (
	SELECT 1 FROM users v
	JOIN posting_windows cw ON cw.region = v.region AND cw.ended_at IS NULL
	JOIN posting_windows pw ON pw.region IN ('asia', 'europe', 'america') AND pw.local_date = cw.local_date
	JOIN posts mp ON mp.author_id = v.id AND mp.posting_window_id = pw.id AND mp.deleted_at IS NULL
	WHERE v.id = ` + param + ` AND v.status = 'active'
)`
}

// singlePostCanViewExpr evaluates whether viewer $2 may see post p (joined with window w).
// The viewer's own posts are always visible; others require the viewer to have posted today.
var singlePostCanViewExpr = `(
	EXISTS (SELECT 1 FROM users v WHERE v.id = $2 AND v.status = 'active')
	AND (
		p.author_id = $2
		OR (
			w.ended_at IS NULL
			AND ` + viewerPostedTodaySQL("$2") + `
			AND NOT EXISTS (
				SELECT 1 FROM block_relations b
				WHERE (b.blocker_id = $2 AND b.blocked_id = p.author_id)
				   OR (b.blocker_id = p.author_id AND b.blocked_id = $2)
			)
			AND (
				p.visibility = 'public'
				OR (
					p.visibility IN ('friends', 'friends_of_friends')
					AND EXISTS (
						SELECT 1 FROM friendships f
						WHERE f.user_low_id = LEAST($2, p.author_id)
						  AND f.user_high_id = GREATEST($2, p.author_id)
					)
				)
				OR (
					p.visibility = 'friends_of_friends'
					AND EXISTS (
						SELECT 1 FROM friend_edges mf
							JOIN friend_edges f2 ON f2.user_id = mf.friend_id AND f2.friend_id = p.author_id
							WHERE mf.user_id = $2
					)
				)
			)
		)
	)
)`

// canViewPostSQL is a scalar boolean: viewer $2 may view post $1 (false if the post is gone).
var canViewPostSQL = `COALESCE((
	SELECT ` + singlePostCanViewExpr + `
	FROM posts p
	JOIN posting_windows w ON w.id = p.posting_window_id
	JOIN users u ON u.id = p.author_id AND u.status = 'active'
	WHERE p.id = $1 AND p.deleted_at IS NULL
), false)`

func getPostWithAuthorIfVisible(ctx context.Context, db sqlx.QueryerContext, viewerID, postID string) (PostWithAuthor, bool, error) {
	var row struct {
		PostWithAuthor
		CanView bool `db:"can_view"`
	}
	err := sqlx.GetContext(ctx, db, &row, `
		SELECT `+postColumns+`, `+userColumnsAs("u", "author")+`, `+singlePostCanViewExpr+` AS can_view
		FROM posts p
		JOIN posting_windows w ON w.id = p.posting_window_id
		JOIN users u ON u.id = p.author_id AND u.status = 'active'
		WHERE p.id = $1 AND p.deleted_at IS NULL
	`, postID, viewerID)
	if err != nil {
		return PostWithAuthor{}, false, err
	}
	if !row.CanView {
		return PostWithAuthor{}, false, nil
	}
	if err := row.Author.setImageKey(); err != nil {
		return PostWithAuthor{}, false, err
	}
	return row.PostWithAuthor, true, nil
}

type viewerContext struct {
	User
	HasPostedToday   bool
	Friends          map[string]bool
	FriendsOfFriends map[string]bool
	MutualFriends    map[string]int
	Blocked          map[string]bool
}

func loadViewerContext(ctx context.Context, db sqlx.QueryerContext, viewerID string) (viewerContext, error) {
	user, err := getUser(ctx, db, viewerID)
	if err != nil {
		return viewerContext{}, err
	}
	return loadViewerContextForUser(ctx, db, user)
}

func loadViewerContextForUser(ctx context.Context, db sqlx.QueryerContext, user User) (viewerContext, error) {
	viewer := viewerContext{
		User:             user,
		Friends:          map[string]bool{},
		FriendsOfFriends: map[string]bool{},
		MutualFriends:    map[string]int{},
		Blocked:          map[string]bool{},
	}

	var rows []struct {
		Kind string `db:"kind"`
		A    string `db:"a"`
		B    string `db:"b"`
	}
	if err := sqlx.SelectContext(ctx, db, &rows, `
		WITH my_friends AS (
			SELECT friend_id AS fid FROM friend_edges WHERE user_id = $1
		)
		SELECT 'P' AS kind,
		       CASE WHEN EXISTS (
		           SELECT 1 FROM posts p
		           JOIN posting_windows posted ON posted.id = p.posting_window_id
		           JOIN posting_windows current ON current.region = $2 AND current.ended_at IS NULL
		                                       AND current.local_date = posted.local_date
		           WHERE p.author_id = $1 AND p.deleted_at IS NULL
		       ) THEN '1' ELSE '0' END AS a,
		       '' AS b
		UNION ALL
		SELECT 'B' AS kind,
		       CASE WHEN blocker_id = $1 THEN blocked_id ELSE blocker_id END AS a,
		       '' AS b
		FROM block_relations
		WHERE blocker_id = $1 OR blocked_id = $1
		UNION ALL
		SELECT 'F' AS kind, fid AS a, '' AS b
		FROM my_friends
		UNION ALL
		SELECT 'E' AS kind, e.user_low_id AS a, e.user_high_id AS b
		FROM (
			SELECT f1.user_low_id, f1.user_high_id
			FROM my_friends mf JOIN friendships f1 ON f1.user_low_id = mf.fid
			UNION
			SELECT f2.user_low_id, f2.user_high_id
			FROM my_friends mf JOIN friendships f2 ON f2.user_high_id = mf.fid
		) e
	`, user.ID, user.Region); err != nil {
		return viewer, err
	}

	type edge struct{ low, high string }
	var edges []edge
	for _, r := range rows {
		switch r.Kind {
		case "P":
			viewer.HasPostedToday = r.A == "1"
		case "B":
			viewer.Blocked[r.A] = true
		case "F":
			viewer.Friends[r.A] = true
		case "E":
			edges = append(edges, edge{low: r.A, high: r.B})
		}
	}
	for _, e := range edges {
		if viewer.Friends[e.low] {
			viewer.FriendsOfFriends[e.high] = true
			if e.high != viewer.ID {
				viewer.MutualFriends[e.high]++
			}
		}
		if viewer.Friends[e.high] {
			viewer.FriendsOfFriends[e.low] = true
			if e.low != viewer.ID {
				viewer.MutualFriends[e.low]++
			}
		}
	}
	return viewer, nil
}

func (viewer viewerContext) canView(post Post) bool {
	if post.AuthorID == viewer.ID {
		return true
	}
	if post.WindowEndedAt != nil || !viewer.HasPostedToday || viewer.Blocked[post.AuthorID] {
		return false
	}
	switch post.Visibility {
	case "public":
		return true
	case "friends":
		return viewer.Friends[post.AuthorID]
	case "friends_of_friends":
		return viewer.Friends[post.AuthorID] || viewer.FriendsOfFriends[post.AuthorID]
	default:
		return false
	}
}

func getFriendIDs(ctx context.Context, db sqlx.QueryerContext, userID string) ([]string, error) {
	var ids []string
	if err := sqlx.SelectContext(ctx, db, &ids, `
		SELECT friend_id FROM friend_edges WHERE user_id = $1
	`, userID); err != nil {
		return nil, err
	}
	return ids, nil
}

func getBlockedUserIDs(ctx context.Context, db sqlx.QueryerContext, userID string) (map[string]bool, error) {
	var ids []string
	if err := sqlx.SelectContext(ctx, db, &ids, `
		SELECT blocked_id FROM block_relations WHERE blocker_id = $1
		UNION ALL
		SELECT blocker_id FROM block_relations WHERE blocked_id = $1
	`, userID); err != nil {
		return nil, err
	}
	blocked := make(map[string]bool, len(ids))
	for _, id := range ids {
		blocked[id] = true
	}
	return blocked, nil
}

// dynamic tuning cycle 11: pipeline optimization pass
