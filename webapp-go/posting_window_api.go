package isureal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type currentPostingWindowState struct {
	window   *PostingWindow
	active   int
	deleted  int
	cachedAt time.Time
}

var (
	postingWindowCacheMu       sync.RWMutex
	postingWindowCacheByUserID = make(map[string]currentPostingWindowState)
	postingWindowCacheTTL      = 60 * time.Second
	postingWindowCacheNow      = time.Now
	// bumped on every invalidation so a load that raced with one is not cached
	postingWindowCacheGen uint64
)

func InvalidatePostingWindowCacheForUser(userID string) {
	if userID == "" {
		return
	}
	postingWindowCacheMu.Lock()
	atomic.AddUint64(&postingWindowCacheGen, 1)
	delete(postingWindowCacheByUserID, userID)
	postingWindowCacheMu.Unlock()
}

func InvalidateAllPostingWindowCache() {
	postingWindowCacheMu.Lock()
	atomic.AddUint64(&postingWindowCacheGen, 1)
	postingWindowCacheByUserID = make(map[string]currentPostingWindowState)
	postingWindowCacheMu.Unlock()
}

// WarmupPostingWindowCache loads every active user's current posting-window state and current
// post in one query (actual counts, not assumed). Entries are written only if no invalidation
// happened while the query was in flight, so a concurrent post/delete is never overwritten.
func WarmupPostingWindowCache(ctx context.Context, db *sqlx.DB) error {
	now := postingWindowCacheNow()
	pwGen := atomic.LoadUint64(&postingWindowCacheGen)
	mpGen := atomic.LoadUint64(&myCurrentPostCacheGen)

	var rows []postingWindowWarmupRow
	if err := db.SelectContext(ctx, &rows, postingWindowWarmupQuery); err != nil {
		return err
	}
	applyPostingWindowWarmup(rows, now, pwGen, mpGen)
	return nil
}

const postingWindowWarmupQuery = `
	SELECT u.id AS user_id,
	       cw.id AS w_id, cw.region AS w_region, cw.local_date AS w_local_date, cw.starts_at AS w_starts_at,
	       cw.on_time_deadline AS w_on_time_deadline, cw.ended_at AS w_ended_at,
	       COALESCE(s.active, 0) AS active, COALESCE(s.deleted, 0) AS deleted,
	       mp.id AS p_id, mp.author_id AS p_author_id, mp.posting_window_id AS p_posting_window_id,
	       mp.image_id AS p_image_id, mp.caption AS p_caption, mp.visibility AS p_visibility,
	       mp.location_kind AS p_location_kind, mp.latitude AS p_latitude, mp.longitude AS p_longitude,
	       mp.locality AS p_locality, mp.posted_at AS p_posted_at,
	       mp.on_time_deadline AS p_on_time_deadline, mp.window_ended_at AS p_window_ended_at
	FROM users u
	LEFT JOIN posting_windows cw ON cw.region = u.region AND cw.ended_at IS NULL
	LEFT JOIN LATERAL (
		SELECT count(*) FILTER (WHERE p.deleted_at IS NULL) AS active,
		       count(*) FILTER (WHERE p.deleted_at IS NOT NULL) AS deleted
		FROM posting_windows pw
		JOIN posts p ON p.posting_window_id = pw.id AND p.author_id = u.id
		WHERE pw.region IN ('asia', 'europe', 'america') AND pw.local_date = cw.local_date
	) s ON true
	LEFT JOIN LATERAL (
		SELECT ` + postColumns + `
		FROM posts p
		JOIN posting_windows w ON w.id = p.posting_window_id
		WHERE p.author_id = u.id AND p.deleted_at IS NULL AND w.local_date = cw.local_date
		LIMIT 1
	) mp ON true
	WHERE u.status = 'active'
`

type postingWindowWarmupRow struct {
	UserID           string     `db:"user_id"`
	WID              *string    `db:"w_id"`
	WRegion          *string    `db:"w_region"`
	WLocalDate       *time.Time `db:"w_local_date"`
	WStartsAt        *time.Time `db:"w_starts_at"`
	WOnTimeDeadline  *time.Time `db:"w_on_time_deadline"`
	WEndedAt         *time.Time `db:"w_ended_at"`
	Active           int        `db:"active"`
	Deleted          int        `db:"deleted"`
	PID              *string    `db:"p_id"`
	PAuthorID        *string    `db:"p_author_id"`
	PPostingWindowID *string    `db:"p_posting_window_id"`
	PImageID         *string    `db:"p_image_id"`
	PCaption         *string    `db:"p_caption"`
	PVisibility      *string    `db:"p_visibility"`
	PLocationKind    *string    `db:"p_location_kind"`
	PLatitude        *float64   `db:"p_latitude"`
	PLongitude       *float64   `db:"p_longitude"`
	PLocality        *string    `db:"p_locality"`
	PPostedAt        *time.Time `db:"p_posted_at"`
	POnTimeDeadline  *time.Time `db:"p_on_time_deadline"`
	PWindowEndedAt   *time.Time `db:"p_window_ended_at"`
}

func (r postingWindowWarmupRow) window() *PostingWindow {
	if r.WID == nil || r.WRegion == nil || r.WLocalDate == nil || r.WStartsAt == nil || r.WOnTimeDeadline == nil {
		return nil
	}
	return &PostingWindow{
		ID: *r.WID, Region: *r.WRegion, LocalDate: *r.WLocalDate,
		StartsAt: *r.WStartsAt, OnTimeDeadline: *r.WOnTimeDeadline, EndedAt: r.WEndedAt,
	}
}

func (r postingWindowWarmupRow) post() *Post {
	if r.PID == nil || r.PAuthorID == nil || r.PPostingWindowID == nil || r.PImageID == nil || r.PCaption == nil ||
		r.PVisibility == nil || r.PLocationKind == nil || r.PPostedAt == nil || r.POnTimeDeadline == nil {
		return nil
	}
	return &Post{
		ID: *r.PID, AuthorID: *r.PAuthorID, PostingWindowID: *r.PPostingWindowID, ImageID: *r.PImageID,
		Caption: *r.PCaption, Visibility: *r.PVisibility,
		PostLocation: PostLocation{Kind: *r.PLocationKind, Latitude: r.PLatitude, Longitude: r.PLongitude, Locality: r.PLocality},
		PostedAt:     *r.PPostedAt, OnTimeDeadline: *r.POnTimeDeadline, WindowEndedAt: r.PWindowEndedAt,
	}
}

// applyPostingWindowWarmup stores warmup rows unless the matching cache was invalidated after
// pwGen/mpGen were read (the rows may then predate that change).
func applyPostingWindowWarmup(rows []postingWindowWarmupRow, now time.Time, pwGen, mpGen uint64) {
	postingWindowCacheMu.Lock()
	if atomic.LoadUint64(&postingWindowCacheGen) == pwGen {
		for _, r := range rows {
			postingWindowCacheByUserID[r.UserID] = currentPostingWindowState{
				window: r.window(), active: r.Active, deleted: r.Deleted, cachedAt: now,
			}
		}
	}
	postingWindowCacheMu.Unlock()

	myCurrentPostCacheMu.Lock()
	if atomic.LoadUint64(&myCurrentPostCacheGen) == mpGen {
		for _, r := range rows {
			myCurrentPostCacheByUserID[r.UserID] = myCurrentPostCacheEntry{post: r.post(), cachedAt: now}
		}
	}
	myCurrentPostCacheMu.Unlock()
}

type currentPostingWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

type getCurrentPostingWindowResponse struct {
	Window        *currentPostingWindowResponse `json:"window"`
	HasPosted     bool                          `json:"hasPosted"`
	CanPost       bool                          `json:"canPost"`
	DeletionCount int                           `json:"deletionCount"`
}

func sendCurrentPostingWindowResponse(c *echo.Context, w *PostingWindow, active, deleted int, now time.Time) error {
	var etag string
	if w == nil {
		etag = `"none"`
	} else {
		etag = fmt.Sprintf("\"%s-%d-%d\"", w.ID, active, deleted)
	}
	c.Response().Header().Set("ETag", etag)

	if reqETag := c.Request().Header.Get("If-None-Match"); reqETag != "" {
		if reqETag == etag || strings.Trim(reqETag, "\"") == strings.Trim(etag, "\"") {
			return c.NoContent(http.StatusNotModified)
		}
	}

	if w == nil {
		return c.JSON(http.StatusOK, getCurrentPostingWindowResponse{})
	}
	return c.JSON(http.StatusOK, getCurrentPostingWindowResponse{
		Window: &currentPostingWindowResponse{
			ID: w.ID, Region: w.Region, LocalDate: w.LocalDate.Format(time.DateOnly),
			StartsAt: w.StartsAt, OnTimeDeadline: w.OnTimeDeadline,
		},
		HasPosted:     active > 0,
		CanPost:       active == 0 && deleted < 2 && !now.Before(w.StartsAt),
		DeletionCount: deleted,
	})
}

func getCurrentPostingWindowHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		userID := GetSession(c).UserID
		now := postingWindowCacheNow()
		gen := atomic.LoadUint64(&postingWindowCacheGen)

		postingWindowCacheMu.RLock()
		cached, ok := postingWindowCacheByUserID[userID]
		postingWindowCacheMu.RUnlock()

		if ok && now.Sub(cached.cachedAt) < postingWindowCacheTTL && !now.Before(cached.cachedAt) {
			return sendCurrentPostingWindowResponse(c, cached.window, cached.active, cached.deleted, now)
		}

		var state struct {
			PostingWindow
			Active  int `db:"active"`
			Deleted int `db:"deleted"`
		}
		err := h.DB.GetContext(c.Request().Context(), &state, `
   SELECT w.id, w.region, w.local_date, w.starts_at, w.on_time_deadline, w.ended_at,
          count(p.id) FILTER (WHERE p.deleted_at IS NULL) AS active,
          count(p.id) FILTER (WHERE p.deleted_at IS NOT NULL) AS deleted
   FROM users u JOIN posting_windows w ON w.region = u.region AND w.ended_at IS NULL
   LEFT JOIN posting_windows posted_window ON posted_window.region IN ('asia', 'europe', 'america') AND posted_window.local_date = w.local_date
   LEFT JOIN posts p ON p.author_id = u.id AND p.posting_window_id = posted_window.id
   WHERE u.id = $1
   GROUP BY w.id
  `, userID)

		if errors.Is(err, sql.ErrNoRows) {
			postingWindowCacheMu.Lock()
			if atomic.LoadUint64(&postingWindowCacheGen) == gen {
				postingWindowCacheByUserID[userID] = currentPostingWindowState{
					window:   nil,
					cachedAt: now,
				}
			}
			postingWindowCacheMu.Unlock()
			return sendCurrentPostingWindowResponse(c, nil, 0, 0, now)
		}
		if err != nil {
			return err
		}

		postingWindowCacheMu.Lock()
		if atomic.LoadUint64(&postingWindowCacheGen) == gen {
			postingWindowCacheByUserID[userID] = currentPostingWindowState{
				window:   &state.PostingWindow,
				active:   state.Active,
				deleted:  state.Deleted,
				cachedAt: now,
			}
		}
		postingWindowCacheMu.Unlock()

		return sendCurrentPostingWindowResponse(c, &state.PostingWindow, state.Active, state.Deleted, now)
	}
}
