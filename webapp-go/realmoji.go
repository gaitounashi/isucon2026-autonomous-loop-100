package isureal

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
)

type RealMoji struct {
	ID           string     `db:"id"`
	PostID       string     `db:"post_id"`
	ReactorID    string     `db:"reactor_id"`
	ImageID      string     `db:"image_id"`
	ReactionType string     `db:"reaction_type"`
	CreatedAt    time.Time  `db:"created_at"`
	ReplacedAt   *time.Time `db:"replaced_at"`
	DeletedAt    *time.Time `db:"deleted_at"`
}

func getRealMoji(ctx context.Context, db sqlx.QueryerContext, id string) (RealMoji, error) {
	var reaction RealMoji
	err := sqlx.GetContext(ctx, db, &reaction, `SELECT id, post_id, reactor_id, image_id, reaction_type, created_at, replaced_at, deleted_at FROM realmojis WHERE id=$1`, id)
	return reaction, err
}

type RealMojiWithReactor struct {
	RealMoji
	Reactor User `db:"reactor"`
}

func selectCurrentRealMojis(ctx context.Context, db sqlx.QueryerContext, conditions string, args ...any) ([]RealMojiWithReactor, error) {
	var reactions []RealMojiWithReactor
	if err := sqlx.SelectContext(ctx, db, &reactions, `
  SELECT r.id, r.post_id, r.reactor_id, r.image_id, r.reaction_type, r.created_at, r.replaced_at, r.deleted_at,
         `+userColumnsAs("u", "reactor")+`
  FROM realmojis r JOIN users u ON u.id = r.reactor_id AND u.status = 'active'
  WHERE r.replaced_at IS NULL AND r.deleted_at IS NULL `+conditions, args...); err != nil {
		return nil, err
	}
	for i := range reactions {
		if err := reactions[i].Reactor.setImageKey(); err != nil {
			return nil, err
		}
	}
	return reactions, nil
}

// selectRealMojisIfAllowed checks that the viewer can view the post and loads up to 51 current
// realmojis (descending, before the cursor) in a single round trip.
func selectRealMojisIfAllowed(ctx context.Context, db sqlx.QueryerContext, viewerID, postID string, beforeCreatedAt *time.Time, beforeID string) (bool, []RealMojiWithReactor, error) {
	var rows []struct {
		Allowed bool `db:"allowed"`
		HasRow  bool `db:"has_row"`
		RealMojiWithReactor
	}
	if err := sqlx.SelectContext(ctx, db, &rows, `
		SELECT a.allowed, x.id IS NOT NULL AS has_row,
		       COALESCE(x.id, '') AS id, COALESCE(x.post_id, '') AS post_id, COALESCE(x.reactor_id, '') AS reactor_id,
		       COALESCE(x.image_id, '') AS image_id, COALESCE(x.reaction_type, '') AS reaction_type,
		       COALESCE(x.created_at, 'epoch'::timestamptz) AS created_at, x.replaced_at, x.deleted_at,
		       `+coalescedUserColumns("x", "reactor")+`
		FROM (SELECT `+canViewPostSQL+` AS allowed) a
		LEFT JOIN LATERAL (
			SELECT r.id, r.post_id, r.reactor_id, r.image_id, r.reaction_type, r.created_at, r.replaced_at, r.deleted_at,
			       `+userColumnsAs("u", "reactor")+`
			FROM realmojis r JOIN users u ON u.id = r.reactor_id AND u.status = 'active'
			WHERE a.allowed AND r.replaced_at IS NULL AND r.deleted_at IS NULL AND r.post_id = $1
			  AND ($3::timestamptz IS NULL OR (r.created_at, r.id) < ($3, $4))
			ORDER BY r.created_at DESC, r.id DESC
			LIMIT 51
		) x ON true
		ORDER BY x.created_at DESC, x.id DESC
	`, postID, viewerID, beforeCreatedAt, beforeID); err != nil {
		return false, nil, err
	}
	if len(rows) == 0 || !rows[0].Allowed {
		return false, nil, nil
	}
	reactions := make([]RealMojiWithReactor, 0, len(rows))
	for i := range rows {
		if !rows[i].HasRow {
			continue
		}
		if err := rows[i].Reactor.setImageKey(); err != nil {
			return false, nil, err
		}
		reactions = append(reactions, rows[i].RealMojiWithReactor)
	}
	return true, reactions, nil
}
