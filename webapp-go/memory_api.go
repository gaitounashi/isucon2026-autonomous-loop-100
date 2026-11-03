package isureal

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type memoryCacheKey struct {
	userID string
	period string
}

type memoryCacheEntry struct {
	response getMemoriesResponse
	cachedAt time.Time
}

var (
	memoryCacheMu  sync.RWMutex
	memoryCache    = make(map[memoryCacheKey]memoryCacheEntry)
	memoryCacheTTL = 60 * time.Second
	memoryCacheNow = time.Now
	// bumped on every invalidation so a load that raced with one is not cached
	memoryCacheGen uint64
)

func InvalidateMemoriesCacheForUser(userID string) {
	if userID == "" {
		return
	}
	memoryCacheMu.Lock()
	defer memoryCacheMu.Unlock()
	atomic.AddUint64(&memoryCacheGen, 1)
	for k := range memoryCache {
		if k.userID == userID {
			delete(memoryCache, k)
		}
	}
}

func InvalidateAllMemoriesCache() {
	memoryCacheMu.Lock()
	atomic.AddUint64(&memoryCacheGen, 1)
	memoryCache = make(map[memoryCacheKey]memoryCacheEntry)
	memoryCacheMu.Unlock()
}

type getMemoriesRequest struct {
	Month          string     `query:"month"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type memoryPostResponse struct {
	createPostResponse
	RealMojis []replaceRealMojiResponse `json:"realmojis"`
	Comments  []createCommentResponse   `json:"comments"`
}

type getMemoriesResponse struct {
	Items   []memoryPostResponse `json:"items"`
	HasMore bool                 `json:"hasMore"`
}

type getMemoriesByDateRequest struct {
	Date           string     `param:"date"`
	BeforePostedAt *time.Time `query:"beforePostedAt"`
	BeforeID       string     `query:"beforeId"`
}

type getMemoriesByDateResponse getMemoriesResponse

func getMemoriesHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.BeforePostedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		start, err := time.Parse("2006-01", input.Month)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}

		user := GetSession(c).User
		isFirstPage := input.BeforePostedAt == nil && input.BeforeID == ""
		now := memoryCacheNow()
		gen := atomic.LoadUint64(&memoryCacheGen)
		ck := memoryCacheKey{userID: user.ID, period: "month:" + input.Month}

		if isFirstPage {
			memoryCacheMu.RLock()
			cached, ok := memoryCache[ck]
			memoryCacheMu.RUnlock()
			if ok && now.Sub(cached.cachedAt) < memoryCacheTTL && !now.Before(cached.cachedAt) {
				return c.JSON(http.StatusOK, cached.response)
			}
		}

		response, err := listMemories(c.Request().Context(), h.DB, user, start, start.AddDate(0, 1, 0), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}

		if isFirstPage {
			memoryCacheMu.Lock()
			if atomic.LoadUint64(&memoryCacheGen) == gen {
				memoryCache[ck] = memoryCacheEntry{response: response, cachedAt: now}
			}
			memoryCacheMu.Unlock()
		}

		return c.JSON(http.StatusOK, response)
	}
}

func getMemoriesByDateHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getMemoriesByDateRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.BeforePostedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		start, err := time.Parse(time.DateOnly, input.Date)
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_memory_period")
		}

		user := GetSession(c).User
		isFirstPage := input.BeforePostedAt == nil && input.BeforeID == ""
		now := memoryCacheNow()
		gen := atomic.LoadUint64(&memoryCacheGen)
		ck := memoryCacheKey{userID: user.ID, period: "date:" + input.Date}

		if isFirstPage {
			memoryCacheMu.RLock()
			cached, ok := memoryCache[ck]
			memoryCacheMu.RUnlock()
			if ok && now.Sub(cached.cachedAt) < memoryCacheTTL && !now.Before(cached.cachedAt) {
				return c.JSON(http.StatusOK, getMemoriesByDateResponse(cached.response))
			}
		}

		response, err := listMemories(c.Request().Context(), h.DB, user, start, start.AddDate(0, 0, 1), input.BeforePostedAt, input.BeforeID)
		if err != nil {
			return err
		}

		if isFirstPage {
			memoryCacheMu.Lock()
			if atomic.LoadUint64(&memoryCacheGen) == gen {
				memoryCache[ck] = memoryCacheEntry{response: response, cachedAt: now}
			}
			memoryCacheMu.Unlock()
		}

		return c.JSON(http.StatusOK, getMemoriesByDateResponse(response))
	}
}

type memoryQueryRow struct {
	Ord             int        `db:"ord"`
	Kind            string     `db:"kind"`
	RN              int64      `db:"rn"`
	SubSeq          int64      `db:"sub_seq"`
	ID              string     `db:"id"`
	PostID          string     `db:"post_id"`
	AuthorID        string     `db:"author_id"`
	PostingWindowID string     `db:"posting_window_id"`
	ImageID         string     `db:"image_id"`
	Caption         string     `db:"caption"`
	Visibility      string     `db:"visibility"`
	LocationKind    string     `db:"location_kind"`
	Latitude        *float64   `db:"latitude"`
	Longitude       *float64   `db:"longitude"`
	Locality        *string    `db:"locality"`
	PostedAt        *time.Time `db:"posted_at"`
	OnTimeDeadline  *time.Time `db:"on_time_deadline"`
	WindowEndedAt   *time.Time `db:"window_ended_at"`
	ReactionType    string     `db:"reaction_type"`
	Body            string     `db:"body"`
	CreatedAt       time.Time  `db:"created_at"`
	User            User       `db:"u"`
}

func listMemories(ctx context.Context, db sqlx.QueryerContext, author User, start, end time.Time, beforeAt *time.Time, beforeID string) (getMemoriesResponse, error) {
	var response getMemoriesResponse
	if (beforeAt == nil) != (beforeID == "") {
		return response, NewAPIError(http.StatusBadRequest, "invalid_pagination")
	}

	var (
		cursorClause string
		args         []any
	)
	if beforeAt != nil {
		cursorClause = `AND (p.posted_at, p.id) < ($4, $5)`
		args = []any{author.ID, start, end, *beforeAt, beforeID}
	} else {
		args = []any{author.ID, start, end}
	}

	query := `
WITH page_posts AS MATERIALIZED (
    SELECT p.id, p.author_id, p.posting_window_id, p.image_id, p.caption, p.visibility,
           p.location_kind, p.latitude, p.longitude, p.locality, p.posted_at,
           w.on_time_deadline, w.ended_at AS window_ended_at,
           row_number() OVER (ORDER BY p.posted_at DESC, p.id DESC) AS rn
    FROM posts p
    JOIN posting_windows w ON w.id = p.posting_window_id
    WHERE p.deleted_at IS NULL
      AND p.author_id = $1
      AND w.local_date >= $2
      AND w.local_date < $3
      ` + cursorClause + `
    ORDER BY p.posted_at DESC, p.id DESC
    LIMIT 51
),
top_posts AS MATERIALIZED (
    SELECT * FROM page_posts WHERE rn <= 50
)
SELECT
    1 AS ord,
    'P' AS kind,
    p.rn,
    0::bigint AS sub_seq,
    p.id,
    p.id AS post_id,
    p.author_id,
    p.posting_window_id,
    p.image_id,
    p.caption,
    p.visibility,
    p.location_kind,
    p.latitude,
    p.longitude,
    p.locality,
    p.posted_at,
    p.on_time_deadline,
    p.window_ended_at,
    ''::text AS reaction_type,
    ''::text AS body,
    p.posted_at AS created_at,
    ''::varchar AS "u.id",
    ''::varchar AS "u.username",
    ''::text AS "u.secret_key_hash",
    ''::text AS "u.display_name",
    ''::text AS "u.bio",
    NULL::varchar AS "u.profile_image_id",
    ''::text AS "u.region",
    ''::text AS "u.account_type",
    ''::text AS "u.status"
FROM page_posts p
UNION ALL
SELECT
    2 AS ord,
    'R' AS kind,
    tp.rn,
    rm.seq AS sub_seq,
    rm.id,
    rm.post_id,
    rm.reactor_id AS author_id,
    ''::varchar AS posting_window_id,
    rm.image_id,
    ''::text AS caption,
    ''::text AS visibility,
    ''::text AS location_kind,
    NULL::numeric AS latitude,
    NULL::numeric AS longitude,
    NULL::text AS locality,
    NULL::timestamptz AS posted_at,
    NULL::timestamptz AS on_time_deadline,
    NULL::timestamptz AS window_ended_at,
    rm.reaction_type,
    ''::text AS body,
    rm.created_at,
    rm."u.id",
    rm."u.username",
    rm."u.secret_key_hash",
    rm."u.display_name",
    rm."u.bio",
    rm."u.profile_image_id",
    rm."u.region",
    rm."u.account_type",
    rm."u.status"
FROM top_posts tp
CROSS JOIN LATERAL (
    SELECT r.id, r.post_id, r.reactor_id, r.image_id, r.reaction_type, r.created_at,
           row_number() OVER (ORDER BY r.created_at DESC, r.id DESC) AS seq,
           ` + userColumnsAs("u", "u") + `
    FROM realmojis r
    JOIN users u ON u.id = r.reactor_id AND u.status = 'active'
    WHERE r.post_id = tp.id AND r.replaced_at IS NULL AND r.deleted_at IS NULL
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT 50
) rm
UNION ALL
SELECT
    3 AS ord,
    'C' AS kind,
    tp.rn,
    cm.seq AS sub_seq,
    cm.id,
    cm.post_id,
    cm.author_id,
    ''::varchar AS posting_window_id,
    ''::varchar AS image_id,
    ''::text AS caption,
    ''::text AS visibility,
    ''::text AS location_kind,
    NULL::numeric AS latitude,
    NULL::numeric AS longitude,
    NULL::text AS locality,
    NULL::timestamptz AS posted_at,
    NULL::timestamptz AS on_time_deadline,
    NULL::timestamptz AS window_ended_at,
    ''::text AS reaction_type,
    cm.body,
    cm.created_at,
    cm."u.id",
    cm."u.username",
    cm."u.secret_key_hash",
    cm."u.display_name",
    cm."u.bio",
    cm."u.profile_image_id",
    cm."u.region",
    cm."u.account_type",
    cm."u.status"
FROM top_posts tp
CROSS JOIN LATERAL (
    SELECT c.id, c.post_id, c.author_id, c.body, c.created_at,
           row_number() OVER (ORDER BY c.created_at ASC, c.id ASC) AS seq,
           ` + userColumnsAs("u", "u") + `
    FROM comments c
    JOIN users u ON u.id = c.author_id AND u.status = 'active'
    WHERE c.post_id = tp.id AND c.deleted_at IS NULL
    ORDER BY c.created_at ASC, c.id ASC
    LIMIT 50
) cm
ORDER BY ord, rn, sub_seq`

	var rows []memoryQueryRow
	if err := sqlx.SelectContext(ctx, db, &rows, query, args...); err != nil {
		return response, err
	}

	var (
		items        = make([]memoryPostResponse, 0, min(len(rows), 50))
		itemByPostID = make(map[string]*memoryPostResponse, 50)
		postCount    = 0
	)

	for _, row := range rows {
		switch row.Ord {
		case 1:
			postCount++
			if row.RN <= 50 {
				var postedAt, deadline time.Time
				if row.PostedAt != nil {
					postedAt = *row.PostedAt
				}
				if row.OnTimeDeadline != nil {
					deadline = *row.OnTimeDeadline
				}
				post := Post{
					ID:              row.ID,
					AuthorID:        row.AuthorID,
					PostingWindowID: row.PostingWindowID,
					ImageID:         row.ImageID,
					Caption:         row.Caption,
					Visibility:      row.Visibility,
					PostLocation: PostLocation{
						Kind:      row.LocationKind,
						Latitude:  row.Latitude,
						Longitude: row.Longitude,
						Locality:  row.Locality,
					},
					PostedAt:       postedAt,
					OnTimeDeadline: deadline,
					WindowEndedAt:  row.WindowEndedAt,
				}
				items = append(items, memoryPostResponse{
					createPostResponse: newPostResponse(post, author),
					RealMojis:          []replaceRealMojiResponse{},
					Comments:           []createCommentResponse{},
				})
			}
		case 2:
			if len(itemByPostID) == 0 && len(items) > 0 {
				for i := range items {
					itemByPostID[items[i].ID] = &items[i]
				}
			}
			item, ok := itemByPostID[row.PostID]
			if !ok {
				continue
			}
			reaction := RealMoji{
				ID:           row.ID,
				PostID:       row.PostID,
				ReactorID:    row.AuthorID,
				ImageID:      row.ImageID,
				ReactionType: row.ReactionType,
				CreatedAt:    row.CreatedAt,
			}
			if err := row.User.setImageKey(); err != nil {
				return response, err
			}
			item.RealMojis = append(item.RealMojis, newRealMojiResponse(reaction, row.User))
		case 3:
			if len(itemByPostID) == 0 && len(items) > 0 {
				for i := range items {
					itemByPostID[items[i].ID] = &items[i]
				}
			}
			item, ok := itemByPostID[row.PostID]
			if !ok {
				continue
			}
			comment := Comment{
				ID:        row.ID,
				PostID:    row.PostID,
				AuthorID:  row.AuthorID,
				Body:      row.Body,
				CreatedAt: row.CreatedAt,
			}
			if err := row.User.setImageKey(); err != nil {
				return response, err
			}
			item.Comments = append(item.Comments, newCommentResponse(comment, row.User))
		}
	}

	response.Items = items
	response.HasMore = postCount > 50
	return response, nil
}
