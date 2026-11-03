package isureal

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

var (
	feedCacheMu          sync.RWMutex
	precomputedFeedCache = make(map[string][]byte) // cacheKey -> pre-serialized JSON
)

func InvalidateAllFeedCache() {
	feedCacheMu.Lock()
	precomputedFeedCache = make(map[string][]byte)
	feedCacheMu.Unlock()
}

func getCachedFeedJSON(cacheKey string) ([]byte, bool) {
	feedCacheMu.RLock()
	defer feedCacheMu.RUnlock()
	b, ok := precomputedFeedCache[cacheKey]
	return b, ok
}

func setCachedFeedJSON(cacheKey string, data []byte) {
	feedCacheMu.Lock()
	defer feedCacheMu.Unlock()
	precomputedFeedCache[cacheKey] = data
}

type getFeedRequest struct {
	Feed           string     `param:"feed"`
	BeforeScore    *int64     `query:"beforeScore"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type feedItemResponse struct {
	createPostResponse
	Score *int64 `json:"score,omitempty"`
}

type getFeedResponse struct {
	Items   []feedItemResponse `json:"items"`
	HasMore bool               `json:"hasMore"`
}

const viewerNotBlockedAuthorExpr = `NOT EXISTS (
	SELECT 1 FROM block_relations b
	WHERE (b.blocker_id = $1 AND b.blocked_id = p.author_id)
	   OR (b.blocker_id = p.author_id AND b.blocked_id = $1)
)`

func getFeedHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getFeedRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		switch input.Feed {
		case "friends", "friends-of-friends", "public", "following":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if (input.BeforePostedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ranked := input.Feed == "friends-of-friends"
		if ranked && (input.BeforeScore == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}

		ctx := c.Request().Context()
		session := GetSession(c)

		// 最新フィード取得時のインメモリ事前シリアライズ JSON 高速パス (DB問い合わせゼロ化)
		cacheKey := ""
		if input.BeforePostedAt == nil && input.BeforeScore == nil {
			cacheKey = session.UserID + ":" + input.Feed
			if cachedJSON, ok := getCachedFeedJSON(cacheKey); ok {
				return c.Blob(http.StatusOK, "application/json", cachedJSON)
			}
		}

		if !ranked {
			cursorClause := ""
			args := []any{session.UserID}
			if input.BeforePostedAt != nil {
				cursorClause = "AND (p.posted_at, p.id) < ($2, $3)"
				args = append(args, *input.BeforePostedAt, input.BeforeID)
			}

			var feedCondition string
			switch input.Feed {
			case "public":
				feedCondition = `AND p.visibility = 'public'`
			case "friends":
				// ARRAY(...) is an InitPlan evaluated once, so this keeps the = ANY(array) index plan
				// without a separate round trip for the friend IDs.
				feedCondition = `
					AND p.visibility IN ('public', 'friends', 'friends_of_friends')
					AND p.author_id = ANY(ARRAY(
						SELECT friend_id FROM friend_edges WHERE user_id = $1
					))`
			case "following":
				feedCondition = `
					AND p.author_id IN (SELECT fl.followee_id FROM follows fl WHERE fl.follower_id = $1)
					AND (
						p.visibility = 'public'
						OR (
							p.visibility IN ('friends', 'friends_of_friends')
							AND EXISTS (
								SELECT 1 FROM friendships f
								WHERE f.user_low_id = LEAST($1, p.author_id)
								  AND f.user_high_id = GREATEST($1, p.author_id)
							)
						)
						OR (
							p.visibility = 'friends_of_friends'
							AND EXISTS (
								SELECT 1 FROM friend_edges mf
									JOIN friend_edges f2 ON f2.user_id = mf.friend_id AND f2.friend_id = p.author_id
									WHERE mf.user_id = $1
							)
						)
					)`
			}

			posts, err := selectPostsWithAuthor(ctx, h.DB, `
				AND p.author_id <> $1
				AND w.ended_at IS NULL
				AND `+viewerPostedTodaySQL("$1")+`
				`+cursorClause+`
				AND `+viewerNotBlockedAuthorExpr+`
				`+feedCondition+`
				ORDER BY p.posted_at DESC, p.id DESC
				LIMIT 51
			`, args...)
			if err != nil {
				return err
			}
			items := make([]feedItemResponse, 0, min(len(posts), 50))
			for i := 0; i < len(posts) && i < 50; i++ {
				items = append(items, feedItemResponse{createPostResponse: newPostResponse(posts[i].Post, posts[i].Author)})
			}
			feedResp := getFeedResponse{Items: items, HasMore: len(posts) > 50}
			if cacheKey != "" {
				if b, err := json.Marshal(feedResp); err == nil {
					setCachedFeedJSON(cacheKey, b)
				}
			}
			return c.JSON(http.StatusOK, feedResp)
		}

		// Ranked feed: friends-of-friends
		if GlobalGraph.IsReady() {
			fofAuthors := GlobalGraph.ComputeFoFAuthors(session.UserID)
			if len(fofAuthors) == 0 {
				return c.JSON(http.StatusOK, getFeedResponse{Items: []feedItemResponse{}, HasMore: false})
			}
		}

		var (
			rankedCursorWhere string
			args              []any
		)
		if input.BeforeScore != nil {
			rankedCursorWhere = "WHERE (score < $2 OR (score = $2 AND (posted_at, id) < ($3, $4)))"
			args = []any{session.UserID, *input.BeforeScore, *input.BeforePostedAt, input.BeforeID}
		} else {
			args = []any{session.UserID}
		}

		query := `
WITH my_friends AS MATERIALIZED (
    SELECT friend_id AS fid FROM friend_edges WHERE user_id = $1
),
my_blocked AS MATERIALIZED (
    SELECT blocked_id AS uid FROM block_relations WHERE blocker_id = $1
    UNION ALL
    SELECT blocker_id AS uid FROM block_relations WHERE blocked_id = $1
),
mutuals AS MATERIALIZED (
    SELECT e.uid, count(*)::bigint AS cnt
    FROM (
        SELECT fe.friend_id AS uid
        FROM my_friends mf JOIN friend_edges fe ON fe.user_id = mf.fid
        WHERE fe.friend_id <> $1
    ) e
    GROUP BY e.uid
),
fof_authors AS MATERIALIZED (
    SELECT m.uid, m.cnt AS mutual_cnt
    FROM mutuals m
    WHERE NOT EXISTS (SELECT 1 FROM my_friends mf WHERE mf.fid = m.uid)
      AND NOT EXISTS (SELECT 1 FROM my_blocked mb WHERE mb.uid = m.uid)
),
candidates AS MATERIALIZED (
    SELECT ` + postColumns + `, ` + userColumnsAs("u", "author") + `, fa.mutual_cnt
    FROM fof_authors fa
    JOIN users u ON u.id = fa.uid AND u.status = 'active'
    JOIN posts p ON p.author_id = fa.uid AND p.deleted_at IS NULL
                AND p.visibility IN ('public', 'friends_of_friends')
    JOIN posting_windows w ON w.id = p.posting_window_id AND w.ended_at IS NULL
    WHERE ` + viewerPostedTodaySQL("$1") + `
),
cand_authors AS MATERIALIZED (
    SELECT DISTINCT author_id FROM candidates
),
author_conns AS MATERIALIZED (
    SELECT ca.author_id,
           LEAST(COALESCE(SUM(m.cnt), 0)::bigint, 30) AS conn_score
    FROM cand_authors ca
    LEFT JOIN (
        SELECT fe.user_id AS author_id, fe.friend_id AS neighbor_id
        FROM cand_authors c1 JOIN friend_edges fe ON fe.user_id = c1.author_id
    ) nb ON nb.author_id = ca.author_id
    LEFT JOIN mutuals m ON m.uid = nb.neighbor_id
    GROUP BY ca.author_id
),
post_engagements AS MATERIALIZED (
    SELECT g.post_id, count(*)::bigint AS eng_cnt
    FROM (
        SELECT r.post_id, r.reactor_id AS user_id
        FROM candidates cp
        JOIN realmojis r ON r.post_id = cp.id AND r.replaced_at IS NULL AND r.deleted_at IS NULL
        JOIN my_friends mf ON mf.fid = r.reactor_id
        JOIN users u ON u.id = r.reactor_id AND u.status = 'active'
        UNION
        SELECT c.post_id, c.author_id AS user_id
        FROM candidates cp
        JOIN comments c ON c.post_id = cp.id AND c.deleted_at IS NULL
        JOIN my_friends mf ON mf.fid = c.author_id
        JOIN users u ON u.id = c.author_id AND u.status = 'active'
    ) g
    GROUP BY g.post_id
),
scored AS (
    SELECT c.id, c.author_id, c.posting_window_id, c.image_id, c.caption, c.visibility,
           c.location_kind, c.latitude, c.longitude, c.locality, c.posted_at,
           c.on_time_deadline, c.window_ended_at,
           c."author.id", c."author.username", c."author.secret_key_hash",
           c."author.display_name", c."author.bio", c."author.profile_image_id",
           c."author.region", c."author.account_type", c."author.status",
           (10 * COALESCE(pe.eng_cnt, 0) + 3 * c.mutual_cnt + COALESCE(ac.conn_score, 0))::bigint AS score
    FROM candidates c
    LEFT JOIN post_engagements pe ON pe.post_id = c.id
    LEFT JOIN author_conns ac ON ac.author_id = c.author_id
)
SELECT *
FROM scored
` + rankedCursorWhere + `
ORDER BY score DESC, posted_at DESC, id DESC
LIMIT 51`

		var rows []struct {
			PostWithAuthor
			Score int64 `db:"score"`
		}
		if err := h.DB.SelectContext(ctx, &rows, query, args...); err != nil {
			return err
		}

		items := make([]feedItemResponse, 0, min(len(rows), 50))
		for i := 0; i < len(rows) && i < 50; i++ {
			if err := rows[i].Author.setImageKey(); err != nil {
				return err
			}
			s := rows[i].Score
			items = append(items, feedItemResponse{
				createPostResponse: newPostResponse(rows[i].Post, rows[i].Author),
				Score:              &s,
			})
		}
		feedResp := getFeedResponse{Items: items, HasMore: len(rows) > 50}
		if cacheKey != "" {
			if b, err := json.Marshal(feedResp); err == nil {
				setCachedFeedJSON(cacheKey, b)
			}
		}
		return c.JSON(http.StatusOK, feedResp)
	}
}

func rankedBefore(score int64, post Post, beforeScore int64, beforePostedAt time.Time, beforeID string) bool {
	if score != beforeScore {
		return score < beforeScore
	}
	t1 := post.PostedAt.UnixNano()
	t2 := beforePostedAt.UnixNano()
	if t1 != t2 {
		return t1 < t2
	}
	return post.ID < beforeID
}
