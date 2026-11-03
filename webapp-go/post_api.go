package isureal

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v5"
)

type createPostRequest struct {
	Image        *multipart.FileHeader `form:"image"`
	Caption      string                `form:"caption"`
	Visibility   string                `form:"visibility"`
	LocationKind string                `form:"location_kind"`
	Latitude     *float64              `form:"latitude"`
	Longitude    *float64              `form:"longitude"`
	Locality     *string               `form:"locality"`
}

type postAuthorResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type createPostResponse struct {
	ID              string             `json:"id"`
	Author          postAuthorResponse `json:"author"`
	PostingWindowID string             `json:"postingWindowId"`
	ImageURL        string             `json:"imageUrl"`
	Caption         string             `json:"caption"`
	Visibility      string             `json:"visibility"`
	Location        PostLocation       `json:"location"`
	PostedAt        time.Time          `json:"postedAt"`
	OnTime          bool               `json:"onTime"`
	LateSeconds     int64              `json:"lateSeconds"`
}

type createPostResult struct {
	Status               string     `db:"status"`
	PostID               *string    `db:"out_post_id"`
	PostingWindowID      *string    `db:"out_posting_window_id"`
	PostedAt             *time.Time `db:"out_posted_at"`
	Latitude             *float64   `db:"out_latitude"`
	Longitude            *float64   `db:"out_longitude"`
	Locality             *string    `db:"out_locality"`
	Caption              *string    `db:"out_caption"`
	Visibility           *string    `db:"out_visibility"`
	LocationKind         *string    `db:"out_location_kind"`
	OnTimeDeadline       *time.Time `db:"out_on_time_deadline"`
	WindowEndedAt        *time.Time `db:"out_window_ended_at"`
	AuthorID             *string    `db:"out_author_id"`
	AuthorUsername       *string    `db:"out_author_username"`
	AuthorSecretKeyHash  *string    `db:"out_author_secret_key_hash"`
	AuthorDisplayName    *string    `db:"out_author_display_name"`
	AuthorBio            *string    `db:"out_author_bio"`
	AuthorProfileImageID *string    `db:"out_author_profile_image_id"`
	AuthorRegion         *string    `db:"out_author_region"`
	AuthorAccountType    *string    `db:"out_author_account_type"`
	AuthorStatus         *string    `db:"out_author_status"`
}

func createPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input createPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Image == nil || !validUserText(input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		switch input.Visibility {
		case "friends", "friends_of_friends", "public":
		default:
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		location := PostLocation{
			Kind: input.LocationKind, Latitude: input.Latitude,
			Longitude: input.Longitude, Locality: input.Locality,
		}
		if !location.valid() {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		imageFile, err := input.Image.Open()
		if err != nil {
			return err
		}
		defer imageFile.Close()
		imageData, err := io.ReadAll(imageFile)
		if err != nil {
			return err
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(imageData))
		if err != nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if cfg.Width > 4096 || cfg.Height > 4096 {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}

		ctx := c.Request().Context()
		userID := GetSession(c).UserID
		imageID := newUUID()
		postID := newUUID()
		postedAt := time.Now().UTC()
		h.Prefetch.MarkLocal(imageID)
		defer h.Prefetch.UnmarkLocal(imageID)
		metaGen := imageMetaCacheGeneration()

		var res createPostResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_post_id, out_posting_window_id, out_posted_at,
			       out_latitude, out_longitude, out_locality,
			       out_caption, out_visibility, out_location_kind,
			       out_on_time_deadline, out_window_ended_at, out_author_id,
			       out_author_username, out_author_secret_key_hash, out_author_display_name,
			       out_author_bio, out_author_profile_image_id, out_author_region,
			       out_author_account_type, out_author_status
			FROM isureal_create_post($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`, postID, userID, imageID, imageData, input.Caption, input.Visibility,
			location.Kind, location.Latitude, location.Longitude, location.Locality, postedAt); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "user_not_found":
			return sql.ErrNoRows
		case "post_slot_unavailable":
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		default:
			return fmt.Errorf("unexpected create post status: %s", res.Status)
		}

		user := User{
			ID:             *res.AuthorID,
			Username:       *res.AuthorUsername,
			SecretKeyHash:  *res.AuthorSecretKeyHash,
			DisplayName:    *res.AuthorDisplayName,
			Bio:            *res.AuthorBio,
			ProfileImageID: res.AuthorProfileImageID,
			Region:         *res.AuthorRegion,
			AccountType:    *res.AuthorAccountType,
			Status:         *res.AuthorStatus,
		}
		if err := user.setImageKey(); err != nil {
			return err
		}

		post := Post{
			ID:              *res.PostID,
			AuthorID:        *res.AuthorID,
			PostingWindowID: *res.PostingWindowID,
			ImageID:         imageID,
			Caption:         *res.Caption,
			Visibility:      *res.Visibility,
			PostLocation: PostLocation{
				Kind:      *res.LocationKind,
				Latitude:  res.Latitude,
				Longitude: res.Longitude,
				Locality:  res.Locality,
			},
			PostedAt:       *res.PostedAt,
			OnTimeDeadline: *res.OnTimeDeadline,
			WindowEndedAt:  res.WindowEndedAt,
		}

		h.Images.Put(imageID, postedAt, imageData)
		// the post (and so its image) is committed and alive: viewers on this server skip the meta query
		setCachedImageMeta(imageID, cachedImageMeta{OwnerUserID: user.ID, SigningKey: user.ImageKey, MediaType: "image/jpeg", CreatedAt: postedAt}, metaGen)
		InvalidatePostingWindowCacheForUser(user.ID)
		InvalidateMyCurrentPostCacheForUser(user.ID)
		InvalidateMemoriesCacheForUser(user.ID)
		InvalidateAllFeedCache()
		return c.JSON(http.StatusCreated, newPostResponse(post, user))
	}
}

func newPostResponse(post Post, user User) createPostResponse {
	response := createPostResponse{
		ID: post.ID,
		Author: postAuthorResponse{
			ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
			ProfileImageURL: newGetMeResponse(user).ProfileImageURL,
			Bio:             user.Bio, AccountType: user.AccountType,
		},
		PostingWindowID: post.PostingWindowID, ImageURL: signedImageURL(user, post.ImageID),
		Caption: post.Caption, Visibility: post.Visibility, Location: post.PostLocation,
		PostedAt: post.PostedAt, OnTime: !post.PostedAt.After(post.OnTimeDeadline),
	}
	if !response.OnTime {
		response.LateSeconds = int64(post.PostedAt.Sub(post.OnTimeDeadline) / time.Second)
	}
	return response
}

type getMyCurrentPostResponse createPostResponse

type myCurrentPostCacheEntry struct {
	post     *Post // nil if 404
	cachedAt time.Time
}

var (
	myCurrentPostCacheMu       sync.RWMutex
	myCurrentPostCacheByUserID = make(map[string]myCurrentPostCacheEntry)
	myCurrentPostCacheTTL      = 60 * time.Second
	myCurrentPostCacheNow      = time.Now
	// bumped on every invalidation so a load that raced with one is not cached
	myCurrentPostCacheGen uint64
)

func InvalidateMyCurrentPostCacheForUser(userID string) {
	if userID == "" {
		return
	}
	myCurrentPostCacheMu.Lock()
	atomic.AddUint64(&myCurrentPostCacheGen, 1)
	delete(myCurrentPostCacheByUserID, userID)
	myCurrentPostCacheMu.Unlock()
}

func InvalidateAllMyCurrentPostCache() {
	myCurrentPostCacheMu.Lock()
	atomic.AddUint64(&myCurrentPostCacheGen, 1)
	myCurrentPostCacheByUserID = make(map[string]myCurrentPostCacheEntry)
	myCurrentPostCacheMu.Unlock()
}

func sendMyCurrentPostResponse(c *echo.Context, post Post, user User) error {
	etag := fmt.Sprintf("\"%s\"", post.ID)
	c.Response().Header().Set("ETag", etag)

	if reqETag := c.Request().Header.Get("If-None-Match"); reqETag != "" {
		if reqETag == etag || strings.Trim(reqETag, "\"") == post.ID {
			return c.NoContent(http.StatusNotModified)
		}
	}

	return c.JSON(http.StatusOK, getMyCurrentPostResponse(newPostResponse(post, user)))
}

func getMyCurrentPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := c.Request().Context()
		session := GetSession(c)
		userID := session.UserID
		now := myCurrentPostCacheNow()
		gen := atomic.LoadUint64(&myCurrentPostCacheGen)

		myCurrentPostCacheMu.RLock()
		cached, ok := myCurrentPostCacheByUserID[userID]
		myCurrentPostCacheMu.RUnlock()

		if ok && now.Sub(cached.cachedAt) < myCurrentPostCacheTTL && !now.Before(cached.cachedAt) {
			if cached.post == nil {
				return NewAPIError(http.StatusNotFound, "post_not_found")
			}
			return sendMyCurrentPostResponse(c, *cached.post, session.User)
		}

		var post Post
		err := h.DB.GetContext(ctx, &post, `
			SELECT `+postColumns+`
			FROM posts p
			JOIN posting_windows w ON w.id = p.posting_window_id
			JOIN users u ON u.id = p.author_id
			JOIN posting_windows current ON current.region = u.region AND current.ended_at IS NULL
			                            AND current.local_date = w.local_date
			WHERE p.author_id = $1 AND p.deleted_at IS NULL
		`, userID)
		if errors.Is(err, sql.ErrNoRows) {
			myCurrentPostCacheMu.Lock()
			if atomic.LoadUint64(&myCurrentPostCacheGen) == gen {
				myCurrentPostCacheByUserID[userID] = myCurrentPostCacheEntry{
					post:     nil,
					cachedAt: now,
				}
			}
			myCurrentPostCacheMu.Unlock()
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}

		myCurrentPostCacheMu.Lock()
		if atomic.LoadUint64(&myCurrentPostCacheGen) == gen {
			myCurrentPostCacheByUserID[userID] = myCurrentPostCacheEntry{
				post:     &post,
				cachedAt: now,
			}
		}
		myCurrentPostCacheMu.Unlock()

		return sendMyCurrentPostResponse(c, post, session.User)
	}
}

type getPostRequest struct {
	PostID string `param:"postId"`
}

type getPostResponse createPostResponse

func getPostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input getPostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		viewerID := GetSession(c).UserID
		post, visible, err := getPostWithAuthorIfVisible(ctx, h.DB, viewerID, input.PostID)
		if errors.Is(err, sql.ErrNoRows) {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		if err != nil {
			return err
		}
		if !visible {
			return NewAPIError(http.StatusNotFound, "post_not_found")
		}
		return c.JSON(http.StatusOK, getPostResponse(newPostResponse(post.Post, post.Author)))
	}
}

type updatePostRequest struct {
	PostID     string  `param:"postId" json:"-"`
	Caption    *string `json:"caption"`
	Visibility *string `json:"visibility"`
}

type updatePostResponse createPostResponse

type updatePostResult struct {
	Status               string     `db:"status"`
	PostID               *string    `db:"out_post_id"`
	PostingWindowID      *string    `db:"out_posting_window_id"`
	ImageID              *string    `db:"out_image_id"`
	PostedAt             *time.Time `db:"out_posted_at"`
	Latitude             *float64   `db:"out_latitude"`
	Longitude            *float64   `db:"out_longitude"`
	Locality             *string    `db:"out_locality"`
	Caption              *string    `db:"out_caption"`
	Visibility           *string    `db:"out_visibility"`
	LocationKind         *string    `db:"out_location_kind"`
	OnTimeDeadline       *time.Time `db:"out_on_time_deadline"`
	WindowEndedAt        *time.Time `db:"out_window_ended_at"`
	AuthorID             *string    `db:"out_author_id"`
	AuthorUsername       *string    `db:"out_author_username"`
	AuthorSecretKeyHash  *string    `db:"out_author_secret_key_hash"`
	AuthorDisplayName    *string    `db:"out_author_display_name"`
	AuthorBio            *string    `db:"out_author_bio"`
	AuthorProfileImageID *string    `db:"out_author_profile_image_id"`
	AuthorRegion         *string    `db:"out_author_region"`
	AuthorAccountType    *string    `db:"out_author_account_type"`
	AuthorStatus         *string    `db:"out_author_status"`
}

func updatePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input updatePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		if input.Caption == nil && input.Visibility == nil {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Caption != nil && !validUserText(*input.Caption, 0, 1000) {
			return NewAPIError(http.StatusBadRequest, "invalid_post")
		}
		if input.Visibility != nil {
			switch *input.Visibility {
			case "friends", "friends_of_friends", "public":
			default:
				return NewAPIError(http.StatusBadRequest, "invalid_post")
			}
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID

		var res updatePostResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status, out_post_id, out_posting_window_id, out_image_id, out_posted_at,
			       out_latitude, out_longitude, out_locality,
			       out_caption, out_visibility, out_location_kind,
			       out_on_time_deadline, out_window_ended_at, out_author_id,
			       out_author_username, out_author_secret_key_hash, out_author_display_name,
			       out_author_bio, out_author_profile_image_id, out_author_region,
			       out_author_account_type, out_author_status
			FROM isureal_update_post($1, $2, $3, $4)
		`, input.PostID, userID, input.Caption, input.Visibility); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			// proceed
		case "user_not_found":
			return sql.ErrNoRows
		case "post_not_found":
			return NewAPIError(http.StatusNotFound, "post_not_found")
		case "post_not_editable":
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		default:
			return fmt.Errorf("unexpected update post status: %s", res.Status)
		}

		user := User{
			ID:             *res.AuthorID,
			Username:       *res.AuthorUsername,
			SecretKeyHash:  *res.AuthorSecretKeyHash,
			DisplayName:    *res.AuthorDisplayName,
			Bio:            *res.AuthorBio,
			ProfileImageID: res.AuthorProfileImageID,
			Region:         *res.AuthorRegion,
			AccountType:    *res.AuthorAccountType,
			Status:         *res.AuthorStatus,
		}
		if err := user.setImageKey(); err != nil {
			return err
		}

		post := Post{
			ID:              *res.PostID,
			AuthorID:        *res.AuthorID,
			PostingWindowID: *res.PostingWindowID,
			ImageID:         *res.ImageID,
			Caption:         *res.Caption,
			Visibility:      *res.Visibility,
			PostLocation: PostLocation{
				Kind:      *res.LocationKind,
				Latitude:  res.Latitude,
				Longitude: res.Longitude,
				Locality:  res.Locality,
			},
			PostedAt:       *res.PostedAt,
			OnTimeDeadline: *res.OnTimeDeadline,
			WindowEndedAt:  res.WindowEndedAt,
		}
		InvalidateMyCurrentPostCacheForUser(user.ID)
		return c.JSON(http.StatusOK, updatePostResponse(newPostResponse(post, user)))
	}
}

type deletePostRequest struct {
	PostID string `param:"postId" json:"-"`
}

type deletePostResult struct {
	Status string `db:"status"`
}

func deletePostHandler(h *HandlerContext) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var input deletePostRequest
		if err := c.Bind(&input); err != nil {
			return err
		}
		ctx := c.Request().Context()
		userID := GetSession(c).UserID

		var res deletePostResult
		if err := h.DB.GetContext(ctx, &res, `
			SELECT status FROM isureal_delete_post($1, $2)
		`, input.PostID, userID); err != nil {
			return err
		}

		switch res.Status {
		case "ok":
			InvalidatePostingWindowCacheForUser(userID)
			InvalidateMyCurrentPostCacheForUser(userID)
			InvalidateMemoriesCacheForUser(userID)
			InvalidateAllFeedCache()
			// realmoji images on the deleted post die too; their owners are other users.
			InvalidateAllImageMeta()
			return c.NoContent(http.StatusNoContent)
		case "user_not_found":
			return sql.ErrNoRows
		case "post_not_found":
			return NewAPIError(http.StatusNotFound, "post_not_found")
		case "post_not_editable":
			return NewAPIError(http.StatusForbidden, "post_not_editable")
		case "post_slot_unavailable":
			return NewAPIError(http.StatusConflict, "post_slot_unavailable")
		default:
			return fmt.Errorf("unexpected delete post status: %s", res.Status)
		}
	}
}
