package isureal

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type Comment struct {
	ID        string     `db:"id"`
	PostID    string     `db:"post_id"`
	AuthorID  string     `db:"author_id"`
	Body      string     `db:"body"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func getComment(ctx context.Context, db sqlx.QueryerContext, id string) (Comment, error) {
	var comment Comment
	err := sqlx.GetContext(ctx, db, &comment, `SELECT id,post_id,author_id,body,created_at,deleted_at FROM comments WHERE id=$1`, id)
	return comment, err
}

type CommentWithAuthor struct {
	Comment
	Author User `db:"author"`
}

func selectCommentsWithAuthor(ctx context.Context, db sqlx.QueryerContext, conditions string, args ...any) ([]CommentWithAuthor, error) {
	var comments []CommentWithAuthor
	if err := sqlx.SelectContext(ctx, db, &comments, `
  SELECT c.id, c.post_id, c.author_id, c.body, c.created_at, c.deleted_at, `+userColumnsAs("u", "author")+`
  FROM comments c JOIN users u ON u.id = c.author_id AND u.status = 'active'
  WHERE c.deleted_at IS NULL `+conditions, args...); err != nil {
		return nil, err
	}
	for i := range comments {
		if err := comments[i].Author.setImageKey(); err != nil {
			return nil, err
		}
	}
	return comments, nil
}

// canCommentSQL is a scalar boolean: viewer $2 has posted today and may interact with post $1.
var canCommentSQL = `(
	` + viewerPostedTodaySQL("$2") + `
	AND EXISTS (
		SELECT 1
		FROM posts p
		JOIN posting_windows w ON w.id = p.posting_window_id
		JOIN users u ON u.id = p.author_id AND u.status = 'active'
		JOIN users v ON v.id = $2 AND v.status = 'active'
		WHERE p.id = $1
		  AND p.deleted_at IS NULL
		  AND w.ended_at IS NULL
		  AND (
			  p.author_id = v.id
			  OR (
				  NOT EXISTS (
					  SELECT 1 FROM block_relations b
					  WHERE (b.blocker_id = v.id AND b.blocked_id = p.author_id)
					     OR (b.blocker_id = p.author_id AND b.blocked_id = v.id)
				  )
				  AND (
					  p.visibility = 'public'
					  OR (
						  p.visibility IN ('friends', 'friends_of_friends')
						  AND EXISTS (
							  SELECT 1 FROM friendships f
							  WHERE f.user_low_id = LEAST(v.id, p.author_id)
							    AND f.user_high_id = GREATEST(v.id, p.author_id)
						  )
					  )
				  )
			  )
		  )
	)
)`

// selectCommentsIfAllowed checks the viewer's permission and loads up to 51 comments of the
// post (ascending, after the cursor) in a single round trip. When not allowed, no comments are read.
func selectCommentsIfAllowed(ctx context.Context, db sqlx.QueryerContext, viewerID, postID string, afterCreatedAt *time.Time, afterID string) (bool, []CommentWithAuthor, error) {
	var rows []struct {
		Allowed bool `db:"allowed"`
		HasRow  bool `db:"has_row"`
		CommentWithAuthor
	}
	if err := sqlx.SelectContext(ctx, db, &rows, `
		SELECT a.allowed, x.id IS NOT NULL AS has_row,
		       COALESCE(x.id, '') AS id, COALESCE(x.post_id, '') AS post_id, COALESCE(x.author_id, '') AS author_id,
		       COALESCE(x.body, '') AS body, COALESCE(x.created_at, 'epoch'::timestamptz) AS created_at, x.deleted_at,
		       `+coalescedUserColumns("x", "author")+`
		FROM (SELECT `+canCommentSQL+` AS allowed) a
		LEFT JOIN LATERAL (
			SELECT c.id, c.post_id, c.author_id, c.body, c.created_at, c.deleted_at, `+userColumnsAs("u", "author")+`
			FROM comments c JOIN users u ON u.id = c.author_id AND u.status = 'active'
			WHERE a.allowed AND c.deleted_at IS NULL AND c.post_id = $1
			  AND ($3::timestamptz IS NULL OR (c.created_at, c.id) > ($3, $4))
			ORDER BY c.created_at, c.id
			LIMIT 51
		) x ON true
		ORDER BY x.created_at, x.id
	`, postID, viewerID, afterCreatedAt, afterID); err != nil {
		return false, nil, err
	}
	if len(rows) == 0 || !rows[0].Allowed {
		return false, nil, nil
	}
	comments := make([]CommentWithAuthor, 0, len(rows))
	for i := range rows {
		if !rows[i].HasRow {
			continue
		}
		if err := rows[i].Author.setImageKey(); err != nil {
			return false, nil, err
		}
		comments = append(comments, rows[i].CommentWithAuthor)
	}
	return true, comments, nil
}
