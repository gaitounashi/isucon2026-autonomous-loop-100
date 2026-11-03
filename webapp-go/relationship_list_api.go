package isureal

import (
	"context"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v5"
)

type relationshipsCacheKey struct {
	userID string
	kind   string
}

type relationshipsCacheEntry struct {
	response getRelationshipsResponse
	cachedAt time.Time
}

var (
	relationshipsCacheMu  sync.RWMutex
	relationshipsCache    = make(map[relationshipsCacheKey]relationshipsCacheEntry)
	relationshipsCacheTTL = 60 * time.Second
	relationshipsCacheNow = time.Now
	// bumped on every invalidation so a load that raced with one is not cached
	relationshipsCacheGen uint64
)

func relationshipsCacheEnabled() bool {
	return os.Getenv("ISUREAL_TEST_DB") != "1"
}

func InvalidateRelationshipsCacheForUser(userID string) {
	if userID == "" {
		return
	}
	relationshipsCacheMu.Lock()
	defer relationshipsCacheMu.Unlock()
	atomic.AddUint64(&relationshipsCacheGen, 1)
	for k := range relationshipsCache {
		if k.userID == userID {
			delete(relationshipsCache, k)
		}
	}
}

func InvalidateAllRelationshipsCache() {
	relationshipsCacheMu.Lock()
	atomic.AddUint64(&relationshipsCacheGen, 1)
	relationshipsCache = make(map[relationshipsCacheKey]relationshipsCacheEntry)
	relationshipsCacheMu.Unlock()
}

type getRelationshipsRequest struct {
	Kind            string     `param:"kind"`
	BeforeUpdatedAt *time.Time `query:"beforeUpdatedAt"`
	BeforeID        string     `query:"beforeId"`
}

type relationshipUserResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type relationshipResponse struct {
	RelationID string                   `json:"relationId"`
	User       relationshipUserResponse `json:"user"`
	Status     string                   `json:"status"`
	UpdatedAt  time.Time                `json:"updatedAt"`
}

type getRelationshipsResponse struct {
	Items   []relationshipResponse `json:"items"`
	HasMore bool                   `json:"hasMore"`
}

func getRelationshipsHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getRelationshipsRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.BeforeUpdatedAt == nil) != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}

		session := GetSession(c)
		userID := session.UserID
		isFirstPage := input.BeforeUpdatedAt == nil && input.BeforeID == ""
		now := relationshipsCacheNow()
		gen := atomic.LoadUint64(&relationshipsCacheGen)

		if isFirstPage && relationshipsCacheEnabled() {
			ck := relationshipsCacheKey{userID: userID, kind: input.Kind}
			relationshipsCacheMu.RLock()
			cached, ok := relationshipsCache[ck]
			relationshipsCacheMu.RUnlock()
			if ok && now.Sub(cached.cachedAt) < relationshipsCacheTTL && !now.Before(cached.cachedAt) {
				return c.JSON(http.StatusOK, cached.response)
			}
		}

		cursorClause := ""
		if input.BeforeUpdatedAt != nil {
			cursorClause = " AND (updated_at, id) < ($2, $3)"
		}
		var query string
		switch input.Kind {
		case "friends":
			query = `
				(SELECT id, user_high_id AS other_id, updated_at
				 FROM friendships
				 WHERE user_low_id = $1` + cursorClause + `
				 ORDER BY updated_at DESC, id DESC)
				UNION ALL
				(SELECT id, user_low_id AS other_id, updated_at
				 FROM friendships
				 WHERE user_high_id = $1` + cursorClause + `
				 ORDER BY updated_at DESC, id DESC)`
		case "following":
			query = `
				SELECT id, followee_id AS other_id, updated_at
				FROM follows
				WHERE follower_id = $1` + cursorClause + `
				ORDER BY updated_at DESC, id DESC`
		case "followers":
			query = `
				SELECT id, follower_id AS other_id, updated_at
				FROM follows
				WHERE followee_id = $1` + cursorClause + `
				ORDER BY updated_at DESC, id DESC`
		case "incoming-requests":
			query = `
				SELECT id, requester_id AS other_id, updated_at
				FROM friend_requests
				WHERE recipient_id = $1 AND status = 'pending'` + cursorClause + `
				ORDER BY updated_at DESC, id DESC`
		case "outgoing-requests":
			query = `
				SELECT id, recipient_id AS other_id, updated_at
				FROM friend_requests
				WHERE requester_id = $1 AND status = 'pending'` + cursorClause + `
				ORDER BY updated_at DESC, id DESC`
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		ctx := c.Request().Context()
		actor := GetSession(c).UserID
		args := []any{actor}
		if input.BeforeUpdatedAt != nil {
			args = append(args, *input.BeforeUpdatedAt, input.BeforeID)
		}
		var records []struct {
			ID        string    `db:"id"`
			UpdatedAt time.Time `db:"updated_at"`
			User      User      `db:"user"`
		}
		if err := h.DB.SelectContext(ctx, &records, `
			SELECT relations.id, relations.updated_at, `+userColumnsAs("u", "user")+`
			FROM (`+query+`) relations
			JOIN users u ON u.id = relations.other_id AND u.status = 'active'
			ORDER BY relations.updated_at DESC, relations.id DESC
			LIMIT 51
		`, args...); err != nil {
			return err
		}
		items := make([]relationshipResponse, 0, min(len(records), 50))
		for i := 0; i < len(records) && i < 50; i++ {
			record := records[i]
			user := record.User
			if err := user.setImageKey(); err != nil {
				return err
			}
			if user.AccountType != "certified_public" && input.Kind != "friends" {
				user.Bio = ""
			}
			status := input.Kind
			if input.Kind == "incoming-requests" || input.Kind == "outgoing-requests" {
				status = "pending"
			}
			items = append(items, relationshipResponse{
				RelationID: record.ID, Status: status, UpdatedAt: record.UpdatedAt,
				User: newRelationshipUserResponse(user),
			})
		}
		response := getRelationshipsResponse{Items: items, HasMore: len(records) > 50}
		if isFirstPage && relationshipsCacheEnabled() {
			ck := relationshipsCacheKey{userID: userID, kind: input.Kind}
			relationshipsCacheMu.Lock()
			if atomic.LoadUint64(&relationshipsCacheGen) == gen {
				relationshipsCache[ck] = relationshipsCacheEntry{
					response: response,
					cachedAt: now,
				}
			}
			relationshipsCacheMu.Unlock()
		}
		return c.JSON(http.StatusOK, response)
	}
}

func WarmupRelationshipsCache(ctx context.Context, db *sqlx.DB) error {
	if !relationshipsCacheEnabled() {
		return nil
	}
	now := relationshipsCacheNow()
	gen := atomic.LoadUint64(&relationshipsCacheGen)

	// 全アクティブユーザーIDの取得
	var userIDs []string
	if err := db.SelectContext(ctx, &userIDs, `SELECT id FROM users WHERE status = 'active'`); err != nil {
		return err
	}

	// 初期フレンド一覧の取得
	var rows []struct {
		RelationID string    `db:"id"`
		UpdatedAt  time.Time `db:"updated_at"`
		UserLowID  string    `db:"user_low_id"`
		UserHighID string    `db:"user_high_id"`
		Low        User      `db:"u_low"`
		High       User      `db:"u_high"`
	}
	err := db.SelectContext(ctx, &rows, `
		SELECT f.id, f.updated_at, f.user_low_id, f.user_high_id,
		       `+userColumnsAs("ul", "u_low")+`,
		       `+userColumnsAs("uh", "u_high")+`
		FROM friendships f
		JOIN users ul ON ul.id = f.user_low_id AND ul.status = 'active'
		JOIN users uh ON uh.id = f.user_high_id AND uh.status = 'active'
		ORDER BY f.updated_at DESC, f.id DESC
	`)
	if err != nil {
		return err
	}

	friendsByUser := make(map[string][]relationshipResponse)
	hasMoreByUser := make(map[string]bool)

	for _, r := range rows {
		// Low user side
		if len(friendsByUser[r.UserLowID]) < 50 {
			uHigh := r.High
			if err := uHigh.setImageKey(); err != nil {
				return err
			}
			friendsByUser[r.UserLowID] = append(friendsByUser[r.UserLowID], relationshipResponse{
				RelationID: r.RelationID,
				Status:     "friends",
				UpdatedAt:  r.UpdatedAt,
				User:       newRelationshipUserResponse(uHigh),
			})
		} else {
			hasMoreByUser[r.UserLowID] = true
		}

		// High user side
		if len(friendsByUser[r.UserHighID]) < 50 {
			uLow := r.Low
			if err := uLow.setImageKey(); err != nil {
				return err
			}
			friendsByUser[r.UserHighID] = append(friendsByUser[r.UserHighID], relationshipResponse{
				RelationID: r.RelationID,
				Status:     "friends",
				UpdatedAt:  r.UpdatedAt,
				User:       newRelationshipUserResponse(uLow),
			})
		} else {
			hasMoreByUser[r.UserHighID] = true
		}
	}

	relationshipsCacheMu.Lock()
	defer relationshipsCacheMu.Unlock()
	// a friendship change committed while loading would make these rows stale
	if atomic.LoadUint64(&relationshipsCacheGen) != gen {
		return nil
	}
	for _, uid := range userIDs {
		items := friendsByUser[uid]
		if items == nil {
			items = []relationshipResponse{}
		}
		relationshipsCache[relationshipsCacheKey{userID: uid, kind: "friends"}] = relationshipsCacheEntry{
			response: getRelationshipsResponse{
				Items:   items,
				HasMore: hasMoreByUser[uid],
			},
			cachedAt: now,
		}
	}
	return nil
}
