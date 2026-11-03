package isureal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

type registerRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
}

type registerResponse struct {
	SecretKey string              `json:"secretKey"`
	User      sessionUserResponse `json:"user"`
}

type loginRequest struct {
	Username  string `json:"username"`
	SecretKey string `json:"secretKey"`
}

type loginResponse struct {
	Token string              `json:"token"`
	User  sessionUserResponse `json:"user"`
}

type sessionUserResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Bio         string `json:"bio"`
	Region      string `json:"region"`
	AccountType string `json:"accountType"`
	Status      string `json:"status"`
}

type getMeResponse struct {
	sessionUserResponse
	ProfileImageURL *string `json:"profileImageUrl"`
}

type updateMeRequest struct {
	DisplayName *string `json:"displayName"`
	Bio         *string `json:"bio"`
	Region      *string `json:"region"`
}

type updateMeResponse getMeResponse

func newSessionUserResponse(user User) sessionUserResponse {
	return sessionUserResponse{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		Bio: user.Bio, Region: user.Region, AccountType: user.AccountType, Status: user.Status,
	}
}

func newGetMeResponse(user User) getMeResponse {
	response := getMeResponse{sessionUserResponse: newSessionUserResponse(user)}
	if user.ProfileImageID != nil {
		url := signedImageURL(user, *user.ProfileImageID)
		response.ProfileImageURL = &url
	}
	return response
}

func registerHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input registerRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		c.Set("username", input.Username)
		if !usernamePattern.MatchString(input.Username) || !validUserText(input.DisplayName, 1, 100) || !validRegion(input.Region) {
			return NewAPIError(http.StatusBadRequest, "invalid_registration")
		}
		secretKey := uuid.NewString()
		hash := sha256.Sum256([]byte(secretKey))
		ctx := c.Request().Context()
		userID := uuid.NewString()
		var user User
		err := h.DB.GetContext(ctx, &user, `
			INSERT INTO users (id, username, secret_key_hash, display_name, region)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (username) DO NOTHING
			RETURNING id, username, secret_key_hash, display_name, bio, profile_image_id, region, account_type, status
		`, userID, input.Username, hex.EncodeToString(hash[:]), input.DisplayName, input.Region)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusConflict, "username_taken")
		}
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, registerResponse{SecretKey: secretKey, User: newSessionUserResponse(user)})
	}
}

func loginHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input loginRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		c.Set("username", input.Username)
		if !usernamePattern.MatchString(input.Username) {
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}
		ctx := c.Request().Context()

		hash := sha256.Sum256([]byte(input.SecretKey))
		expectedHash := hex.EncodeToString(hash[:])
		randomToken := uuid.NewString()
		tokenHashBytes := sha256.Sum256([]byte(randomToken))
		tokenHash := hex.EncodeToString(tokenHashBytes[:])
		sessionID := uuid.NewString()

		var res struct {
			Status         string  `db:"status"`
			UserID         *string `db:"out_user_id"`
			Username       *string `db:"out_username"`
			DisplayName    *string `db:"out_display_name"`
			Bio            *string `db:"out_bio"`
			ProfileImageID *string `db:"out_profile_image_id"`
			Region         *string `db:"out_region"`
			AccountType    *string `db:"out_account_type"`
		}

		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_user_id, out_username, out_display_name, out_bio, out_profile_image_id, out_region, out_account_type
			FROM isureal_login($1, $2, $3, $4)
		`, input.Username, expectedHash, sessionID, tokenHash); err != nil {
			return err
		}

		if res.Status != "ok" || res.UserID == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_credentials")
		}

		user := User{
			ID:             *res.UserID,
			Username:       *res.Username,
			DisplayName:    *res.DisplayName,
			Bio:            *res.Bio,
			ProfileImageID: res.ProfileImageID,
			Region:         *res.Region,
			AccountType:    *res.AccountType,
			Status:         "active",
		}

		InvalidateSessionsForUser(user.ID)
		if err := user.setImageKey(); err == nil {
			gen := sessionCacheGeneration()
			setCachedSession(tokenHash, &Session{
				Region:    user.Region,
				UserID:    user.ID,
				TokenHash: tokenHash,
				CreatedAt: sessionCacheNow(),
				User:      user,
			}, gen)
		}
		return c.JSON(http.StatusOK, loginResponse{Token: randomToken, User: newSessionUserResponse(user)})
	}
}

func logoutHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		if err := InvalidateLoginSession(h.DB, session.UserID, session.TokenHash); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	}
}

func getMeHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		return c.JSON(http.StatusOK, newGetMeResponse(session.User))
	}
}

func updateMeHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		session := GetSession(c)
		if session == nil {
			return NewAPIError(http.StatusUnauthorized, "invalid_session")
		}
		var input updateMeRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if (input.DisplayName == nil && input.Bio == nil && input.Region == nil) ||
			(input.DisplayName != nil && !validUserText(*input.DisplayName, 1, 100)) ||
			(input.Bio != nil && !validUserText(*input.Bio, 0, 500)) ||
			(input.Region != nil && !validRegion(*input.Region)) {
			return NewAPIError(http.StatusBadRequest, "invalid_profile")
		}
		ctx := c.Request().Context()
		var user User
		err := h.DB.GetContext(ctx, &user, `
			UPDATE users
			SET display_name = COALESCE($2, display_name),
			    bio = COALESCE($3, bio),
			    region = COALESCE($4, region),
			    updated_at = clock_timestamp()
			WHERE id = $1 AND status = 'active'
			RETURNING id, username, secret_key_hash, display_name, bio, profile_image_id, region, account_type, status
		`, session.UserID, input.DisplayName, input.Bio, input.Region)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		if err := user.setImageKey(); err != nil {
			return err
		}
		InvalidateSessionsForUser(session.UserID)
		InvalidatePostingWindowCacheForUser(session.UserID)
		InvalidateMyCurrentPostCacheForUser(session.UserID)
		return c.JSON(http.StatusOK, updateMeResponse(newGetMeResponse(user)))
	}
}
