package isureal

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type searchUsersRequest struct {
	Q              string `query:"q"`
	BeforeUsername string `query:"beforeUsername"`
	BeforeID       string `query:"beforeId"`
}

type searchUsersResponse struct {
	Items   []relationshipUserResponse `json:"items"`
	HasMore bool                       `json:"hasMore"`
}

func isPureASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func normalizeSearchText(value string) string {
	hasNonASCII := false
	hasUpperASCII := false
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b >= 0x80 {
			hasNonASCII = true
			break
		}
		if b >= 'A' && b <= 'Z' {
			hasUpperASCII = true
		}
	}
	if !hasNonASCII {
		if !hasUpperASCII {
			return value
		}
		return strings.ToLower(value)
	}
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

func searchUsersHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input searchUsersRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if !validUserText(input.Q, 1, 100) {
			return NewAPIError(http.StatusBadRequest, "invalid_relationship")
		}
		if (input.BeforeUsername == "") != (input.BeforeID == "") {
			return NewAPIError(http.StatusBadRequest, "invalid_pagination")
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID

		query := normalizeSearchText(input.Q)
		pureASCII := isPureASCII(input.Q)
		batchLimit := 300
		if pureASCII {
			batchLimit = 100
		}

		curUsername := input.BeforeUsername
		curID := input.BeforeID
		items := make([]relationshipUserResponse, 0, 51)

		for {
			args := []any{viewer}
			var sqlBuilder strings.Builder
			sqlBuilder.WriteString("SELECT " + strings.Join(userColumns, ", ") + " FROM users u WHERE u.status = 'active' AND u.id <> $1")
			sqlBuilder.WriteString(" AND NOT EXISTS (SELECT 1 FROM block_relations b WHERE (b.blocker_id = $1 AND b.blocked_id = u.id) OR (b.blocker_id = u.id AND b.blocked_id = $1))")

			if curUsername != "" {
				args = append(args, curUsername, curID)
				sqlBuilder.WriteString(fmt.Sprintf(" AND (u.username, u.id) > ($%d, $%d)", len(args)-1, len(args)))
			}

			if pureASCII {
				args = append(args, "%"+query+"%")
				if strings.ContainsAny(query, "si") {
					sqlBuilder.WriteString(fmt.Sprintf(" AND (u.username LIKE $%d OR lower(u.display_name) LIKE $%d OR u.display_name ~ '[ßİŉſ;ẚẞ`Kﬀ-ﬆ]')", len(args), len(args)))
				} else {
					sqlBuilder.WriteString(fmt.Sprintf(" AND (u.username LIKE $%d OR lower(u.display_name) LIKE $%d)", len(args), len(args)))
				}
			}

			sqlBuilder.WriteString(fmt.Sprintf(" ORDER BY u.username, u.id LIMIT %d", batchLimit))

			var batch []User
			if err := h.DB.SelectContext(ctx, &batch, sqlBuilder.String(), args...); err != nil {
				return err
			}

			for _, user := range batch {
				curUsername = user.Username
				curID = user.ID
				if !strings.Contains(normalizeSearchText(user.Username), query) && !strings.Contains(normalizeSearchText(user.DisplayName), query) {
					continue
				}
				if err := user.setImageKey(); err != nil {
					return err
				}
				if user.AccountType != "certified_public" {
					user.Bio = ""
				}
				items = append(items, newRelationshipUserResponse(user))
				if len(items) == 51 {
					break
				}
			}

			if len(items) == 51 || len(batch) < batchLimit {
				break
			}
		}

		hasMore := len(items) > 50
		if hasMore {
			items = items[:50]
		}
		return c.JSON(http.StatusOK, searchUsersResponse{Items: items, HasMore: hasMore})
	}
}

type getUserRequest struct {
	UserID string `param:"userId"`
}

type getUserResponse relationshipUserResponse

func getUserHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getUserRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		viewer := GetSession(c).UserID
		var row struct {
			User
			IsBlocked bool `db:"is_blocked"`
			IsFriend  bool `db:"is_friend"`
		}
		err := h.DB.GetContext(ctx, &row, `
			SELECT `+strings.Join(userColumns, ", ")+`,
			       EXISTS (
				       SELECT 1 FROM block_relations b
				       WHERE (b.blocker_id = $1 AND b.blocked_id = users.id)
				          OR (b.blocker_id = users.id AND b.blocked_id = $1)
			       ) AS is_blocked,
			       EXISTS (
				       SELECT 1 FROM friendships f
				       WHERE f.user_low_id = LEAST($1, users.id)
				         AND f.user_high_id = GREATEST($1, users.id)
			       ) AS is_friend
			FROM users
			WHERE id = $2 AND status = 'active'
		`, viewer, input.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		if err != nil {
			return err
		}
		if row.IsBlocked {
			return NewAPIError(http.StatusNotFound, "relationship_not_found")
		}
		user := row.User
		if err := user.setImageKey(); err != nil {
			return err
		}
		if user.AccountType != "certified_public" && !row.IsFriend {
			user.Bio = ""
		}
		return c.JSON(http.StatusOK, getUserResponse(newRelationshipUserResponse(user)))
	}
}

func newRelationshipUserResponse(user User) relationshipUserResponse {
	return relationshipUserResponse{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, ProfileImageURL: newGetMeResponse(user).ProfileImageURL, Bio: user.Bio, AccountType: user.AccountType}
}
