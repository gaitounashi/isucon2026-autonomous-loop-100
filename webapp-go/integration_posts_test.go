package isureal

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type postsAPIError struct {
	Error string `json:"error"`
}

type postsWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

type postsCurrentWindowResponse struct {
	Window        *postsWindowResponse `json:"window"`
	HasPosted     bool                 `json:"hasPosted"`
	CanPost       bool                 `json:"canPost"`
	DeletionCount int                  `json:"deletionCount"`
}

type postsAuthorResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type postsLocationResponse struct {
	Kind      string   `json:"kind"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Locality  *string  `json:"locality,omitempty"`
}

type postsPostResponse struct {
	ID              string                `json:"id"`
	Author          postsAuthorResponse   `json:"author"`
	PostingWindowID string                `json:"postingWindowId"`
	ImageURL        string                `json:"imageUrl"`
	Caption         string                `json:"caption"`
	Visibility      string                `json:"visibility"`
	Location        postsLocationResponse `json:"location"`
	PostedAt        time.Time             `json:"postedAt"`
	OnTime          bool                  `json:"onTime"`
	LateSeconds     int64                 `json:"lateSeconds"`
}

type postsCreateOptions struct {
	Caption       *string
	Visibility    *string
	LocationKind  *string
	Latitude      *float64
	Longitude     *float64
	Locality      *string
	ImageData     []byte
	ImageFilename string
}

func postsStringPtr(s string) *string {
	return &s
}

func postsFloatPtr(f float64) *float64 {
	return &f
}

func postsDefaultCreateOptions() postsCreateOptions {
	return postsCreateOptions{
		Caption:      postsStringPtr("default caption"),
		Visibility:   postsStringPtr("public"),
		LocationKind: postsStringPtr("none"),
		ImageData:    tinyJPEG(),
	}
}

func postsDoCreate(env *testEnv, token string, opts postsCreateOptions) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if opts.Caption != nil {
		if err := writer.WriteField("caption", *opts.Caption); err != nil {
			env.T.Fatalf("failed to write caption: %v", err)
		}
	}
	if opts.Visibility != nil {
		if err := writer.WriteField("visibility", *opts.Visibility); err != nil {
			env.T.Fatalf("failed to write visibility: %v", err)
		}
	}
	if opts.LocationKind != nil {
		if err := writer.WriteField("location_kind", *opts.LocationKind); err != nil {
			env.T.Fatalf("failed to write location_kind: %v", err)
		}
	}
	if opts.Latitude != nil {
		if err := writer.WriteField("latitude", strconv.FormatFloat(*opts.Latitude, 'f', -1, 64)); err != nil {
			env.T.Fatalf("failed to write latitude: %v", err)
		}
	}
	if opts.Longitude != nil {
		if err := writer.WriteField("longitude", strconv.FormatFloat(*opts.Longitude, 'f', -1, 64)); err != nil {
			env.T.Fatalf("failed to write longitude: %v", err)
		}
	}
	if opts.Locality != nil {
		if err := writer.WriteField("locality", *opts.Locality); err != nil {
			env.T.Fatalf("failed to write locality: %v", err)
		}
	}
	if opts.ImageData != nil {
		filename := opts.ImageFilename
		if filename == "" {
			filename = "image.jpg"
		}
		part, err := writer.CreateFormFile("image", filename)
		if err != nil {
			env.T.Fatalf("failed to create form file: %v", err)
		}
		if _, err := part.Write(opts.ImageData); err != nil {
			env.T.Fatalf("failed to write image data: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		env.T.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/posts", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func postsMakeFriendship(t *testing.T, env *testEnv, u1, u2 testUser) {
	t.Helper()
	low, high := u1.ID, u2.ID
	if low > high {
		low, high = high, low
	}
	_, err := env.DB.Exec(`INSERT INTO friendships (id, user_low_id, user_high_id) VALUES ($1, $2, $3)`, uuid.NewString(), low, high)
	if err != nil {
		t.Fatalf("failed to insert friendship: %v", err)
	}
}

func postsBlockUser(t *testing.T, env *testEnv, blocker, blocked testUser) {
	t.Helper()
	_, err := env.DB.Exec(`INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($1, $2, $3)`, uuid.NewString(), blocker.ID, blocked.ID)
	if err != nil {
		t.Fatalf("failed to insert block relation: %v", err)
	}
}

// TestIntegrationPosts_PostingWindow_CurrentBeforeAndAfter covers GET /api/posting-window/current
// before and after a window is opened, after posting, and after deleting.
func TestIntegrationPosts_PostingWindow_CurrentBeforeAndAfter(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("posts_win_u1")

	// 1. Unauthorized request
	recUnauth := env.do(http.MethodGet, "/api/posting-window/current", "", nil)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized, got %d: %s", recUnauth.Code, recUnauth.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recUnauth)
	if errResp.Error != "invalid_session" {
		t.Fatalf("expected error invalid_session, got %s", errResp.Error)
	}

	// 2. Before window is opened
	recBefore := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if recBefore.Code != http.StatusOK {
		t.Fatalf("expected 200 before window open, got %d: %s", recBefore.Code, recBefore.Body.String())
	}
	stateBefore := decodeJSON[postsCurrentWindowResponse](t, recBefore)
	if stateBefore.Window != nil {
		t.Fatalf("expected window to be nil before opening, got %+v", stateBefore.Window)
	}
	if stateBefore.HasPosted || stateBefore.CanPost || stateBefore.DeletionCount != 0 {
		t.Fatalf("unexpected state before window: hasPosted=%v, canPost=%v, deletions=%d",
			stateBefore.HasPosted, stateBefore.CanPost, stateBefore.DeletionCount)
	}

	// 3. After window is opened (before posting)
	env.openPostingWindow()

	recAfter := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if recAfter.Code != http.StatusOK {
		t.Fatalf("expected 200 after window open, got %d: %s", recAfter.Code, recAfter.Body.String())
	}
	stateAfter := decodeJSON[postsCurrentWindowResponse](t, recAfter)
	if stateAfter.Window == nil {
		t.Fatalf("expected non-nil window after opening")
	}
	if stateAfter.Window.ID == "" || stateAfter.Window.Region != "asia" || stateAfter.Window.LocalDate == "" {
		t.Fatalf("invalid window fields: %+v", stateAfter.Window)
	}
	if stateAfter.Window.StartsAt.IsZero() || stateAfter.Window.OnTimeDeadline.IsZero() {
		t.Fatalf("expected non-zero window timestamps: %+v", stateAfter.Window)
	}
	if stateAfter.HasPosted != false || stateAfter.CanPost != true || stateAfter.DeletionCount != 0 {
		t.Fatalf("unexpected state after window open: hasPosted=%v, canPost=%v, deletions=%d",
			stateAfter.HasPosted, stateAfter.CanPost, stateAfter.DeletionCount)
	}

	// 4. After user posts
	postID := env.createPost(user)
	if postID == "" {
		t.Fatalf("expected non-empty post ID")
	}

	recPosted := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if recPosted.Code != http.StatusOK {
		t.Fatalf("expected 200 after posting, got %d: %s", recPosted.Code, recPosted.Body.String())
	}
	statePosted := decodeJSON[postsCurrentWindowResponse](t, recPosted)
	if statePosted.HasPosted != true || statePosted.CanPost != false || statePosted.DeletionCount != 0 {
		t.Fatalf("unexpected state after posting: hasPosted=%v, canPost=%v, deletions=%d",
			statePosted.HasPosted, statePosted.CanPost, statePosted.DeletionCount)
	}

	// 5. After user deletes post
	recDel := env.do(http.MethodDelete, "/api/posts/"+postID, user.Token, nil)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete post, got %d: %s", recDel.Code, recDel.Body.String())
	}

	recAfterDel := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if recAfterDel.Code != http.StatusOK {
		t.Fatalf("expected 200 after deleting post, got %d: %s", recAfterDel.Code, recAfterDel.Body.String())
	}
	stateAfterDel := decodeJSON[postsCurrentWindowResponse](t, recAfterDel)
	if stateAfterDel.HasPosted != false || stateAfterDel.CanPost != true || stateAfterDel.DeletionCount != 1 {
		t.Fatalf("unexpected state after post deletion: hasPosted=%v, canPost=%v, deletions=%d",
			stateAfterDel.HasPosted, stateAfterDel.CanPost, stateAfterDel.DeletionCount)
	}
}

// TestIntegrationPosts_CreatePost_MultipartValidation tests POST /api/posts validation:
// missing image, non-jpeg image, bad visibility, and caption length limit.
func TestIntegrationPosts_CreatePost_MultipartValidation(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	user := env.registerUser("posts_val_u1")

	// 1. Missing image
	t.Run("MissingImage", func(t *testing.T) {
		opts := postsDefaultCreateOptions()
		opts.ImageData = nil
		rec := postsDoCreate(env, user.Token, opts)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for missing image, got %d: %s", rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[postsAPIError](t, rec)
		if errResp.Error != "invalid_post" {
			t.Fatalf("expected invalid_post error, got %s", errResp.Error)
		}
	})

	// 2. Non-JPEG image (PNG)
	t.Run("NonJPEGImage", func(t *testing.T) {
		opts := postsDefaultCreateOptions()
		opts.ImageData = tinyPNG()
		opts.ImageFilename = "image.png"
		rec := postsDoCreate(env, user.Token, opts)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for non-jpeg image, got %d: %s", rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[postsAPIError](t, rec)
		if errResp.Error != "invalid_post" {
			t.Fatalf("expected invalid_post error, got %s", errResp.Error)
		}
	})

	// 3. Bad visibility
	t.Run("BadVisibility", func(t *testing.T) {
		badVisibilities := []string{"private", "only_me", "invalid", ""}
		for _, vis := range badVisibilities {
			opts := postsDefaultCreateOptions()
			opts.Visibility = postsStringPtr(vis)
			rec := postsDoCreate(env, user.Token, opts)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for visibility %q, got %d: %s", vis, rec.Code, rec.Body.String())
			}
			errResp := decodeJSON[postsAPIError](t, rec)
			if errResp.Error != "invalid_post" {
				t.Fatalf("expected invalid_post for visibility %q, got %s", vis, errResp.Error)
			}
		}
	})

	// 4. Caption length limit (> 1000 runes)
	t.Run("CaptionTooLong", func(t *testing.T) {
		opts := postsDefaultCreateOptions()
		longCap := strings.Repeat("a", 1001)
		opts.Caption = &longCap
		rec := postsDoCreate(env, user.Token, opts)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for caption > 1000 chars, got %d: %s", rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[postsAPIError](t, rec)
		if errResp.Error != "invalid_post" {
			t.Fatalf("expected invalid_post for caption > 1000 chars, got %s", errResp.Error)
		}
	})

	// 5. Unauthorized request
	t.Run("Unauthorized", func(t *testing.T) {
		opts := postsDefaultCreateOptions()
		rec := postsDoCreate(env, "", opts)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unauthorized create post, got %d: %s", rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[postsAPIError](t, rec)
		if errResp.Error != "invalid_session" {
			t.Fatalf("expected invalid_session, got %s", errResp.Error)
		}
	})
}

// TestIntegrationPosts_CreatePost_LocationValidation tests location kind and coordinate / locality constraints.
func TestIntegrationPosts_CreatePost_LocationValidation(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	user := env.registerUser("posts_loc_u1")

	// Invalid cases
	invalidCases := []struct {
		name string
		opts postsCreateOptions
	}{
		{
			name: "NoneWithLatitude",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("none"),
				Latitude:     postsFloatPtr(35.68),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "NoneWithLocality",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("none"),
				Locality:     postsStringPtr("Tokyo"),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactMissingLongitude",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(35.68),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactMissingLatitude",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Longitude:    postsFloatPtr(139.76),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactLatitudeTooLarge",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(90.1),
				Longitude:    postsFloatPtr(139.76),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactLatitudeTooSmall",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(-90.1),
				Longitude:    postsFloatPtr(139.76),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactLongitudeTooLarge",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(35.68),
				Longitude:    postsFloatPtr(180.1),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactLongitudeTooSmall",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(35.68),
				Longitude:    postsFloatPtr(-180.1),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ExactWithLocality",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("exact"),
				Latitude:     postsFloatPtr(35.68),
				Longitude:    postsFloatPtr(139.76),
				Locality:     postsStringPtr("Tokyo"),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ApproximateMissingLocality",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("approximate"),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ApproximateEmptyLocality",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("approximate"),
				Locality:     postsStringPtr(""),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ApproximateLocalityTooLong",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("approximate"),
				Locality:     postsStringPtr(strings.Repeat("a", 101)),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "ApproximateWithLatitude",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("approximate"),
				Latitude:     postsFloatPtr(35.68),
				Locality:     postsStringPtr("Tokyo"),
				ImageData:    tinyJPEG(),
			},
		},
		{
			name: "InvalidKind",
			opts: postsCreateOptions{
				Caption:      postsStringPtr("test"),
				Visibility:   postsStringPtr("public"),
				LocationKind: postsStringPtr("gps_point"),
				ImageData:    tinyJPEG(),
			},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postsDoCreate(env, user.Token, tc.opts)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
			errResp := decodeJSON[postsAPIError](t, rec)
			if errResp.Error != "invalid_post" {
				t.Fatalf("%s: expected invalid_post error, got %s", tc.name, errResp.Error)
			}
		})
	}
}

// TestIntegrationPosts_CreatePost_WindowRules covers:
// - no open posting window (409 post_slot_unavailable)
// - posting twice in one window (409 post_slot_unavailable)
func TestIntegrationPosts_CreatePost_WindowRules(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("posts_winrules_u1")

	// 1. No open posting window
	opts := postsDefaultCreateOptions()
	recNoWin := postsDoCreate(env, user.Token, opts)
	if recNoWin.Code != http.StatusConflict {
		t.Fatalf("expected 409 without open window, got %d: %s", recNoWin.Code, recNoWin.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recNoWin)
	if errResp.Error != "post_slot_unavailable" {
		t.Fatalf("expected post_slot_unavailable, got %s", errResp.Error)
	}

	// 2. Open window and post first time -> 201 Created
	env.openPostingWindow()

	recFirst := postsDoCreate(env, user.Token, opts)
	if recFirst.Code != http.StatusCreated {
		t.Fatalf("expected 201 for first post, got %d: %s", recFirst.Code, recFirst.Body.String())
	}

	// 3. Post second time in same window -> 409 Conflict
	recSecond := postsDoCreate(env, user.Token, opts)
	if recSecond.Code != http.StatusConflict {
		t.Fatalf("expected 409 for second post in same window, got %d: %s", recSecond.Code, recSecond.Body.String())
	}
	errRespSecond := decodeJSON[postsAPIError](t, recSecond)
	if errRespSecond.Error != "post_slot_unavailable" {
		t.Fatalf("expected post_slot_unavailable, got %s", errRespSecond.Error)
	}
}

// TestIntegrationPosts_CreatePost_Success_LocationsAndResponseFields tests 201 Created
// responses and verifies all fields for none, exact, and approximate locations.
func TestIntegrationPosts_CreatePost_Success_LocationsAndResponseFields(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	// 1. LocationKind = none
	userNone := env.registerUser("posts_succ_none")
	optsNone := postsDefaultCreateOptions()
	optsNone.Caption = postsStringPtr("hello none")
	optsNone.Visibility = postsStringPtr("public")

	recNone := postsDoCreate(env, userNone.Token, optsNone)
	if recNone.Code != http.StatusCreated {
		t.Fatalf("expected 201 for none location, got %d: %s", recNone.Code, recNone.Body.String())
	}
	postNone := decodeJSON[postsPostResponse](t, recNone)
	if postNone.ID == "" {
		t.Fatalf("expected non-empty ID")
	}
	if postNone.Author.ID != userNone.ID || postNone.Author.Username != userNone.Username {
		t.Fatalf("unexpected author: %+v", postNone.Author)
	}
	if postNone.Caption != "hello none" || postNone.Visibility != "public" {
		t.Fatalf("unexpected post fields: %+v", postNone)
	}
	if postNone.Location.Kind != "none" || postNone.Location.Latitude != nil || postNone.Location.Longitude != nil || postNone.Location.Locality != nil {
		t.Fatalf("unexpected location fields for none: %+v", postNone.Location)
	}
	if !strings.HasPrefix(postNone.ImageURL, "/api/images/") {
		t.Fatalf("expected signed image URL starting with /api/images/, got %s", postNone.ImageURL)
	}
	if !postNone.OnTime || postNone.LateSeconds != 0 {
		t.Fatalf("expected onTime=true and lateSeconds=0, got onTime=%v, lateSeconds=%d", postNone.OnTime, postNone.LateSeconds)
	}

	// 2. LocationKind = exact
	userExact := env.registerUser("posts_succ_exact")
	optsExact := postsCreateOptions{
		Caption:      postsStringPtr("hello exact"),
		Visibility:   postsStringPtr("friends"),
		LocationKind: postsStringPtr("exact"),
		Latitude:     postsFloatPtr(35.6812),
		Longitude:    postsFloatPtr(139.7671),
		ImageData:    tinyJPEG(),
	}
	recExact := postsDoCreate(env, userExact.Token, optsExact)
	if recExact.Code != http.StatusCreated {
		t.Fatalf("expected 201 for exact location, got %d: %s", recExact.Code, recExact.Body.String())
	}
	postExact := decodeJSON[postsPostResponse](t, recExact)
	if postExact.Location.Kind != "exact" || postExact.Location.Latitude == nil || postExact.Location.Longitude == nil {
		t.Fatalf("expected valid exact location, got %+v", postExact.Location)
	}
	if *postExact.Location.Latitude < 35.68 || *postExact.Location.Latitude > 35.69 {
		t.Fatalf("latitude mismatch: %f", *postExact.Location.Latitude)
	}
	if *postExact.Location.Longitude < 139.76 || *postExact.Location.Longitude > 139.77 {
		t.Fatalf("longitude mismatch: %f", *postExact.Location.Longitude)
	}
	if postExact.Location.Locality != nil {
		t.Fatalf("expected nil locality for exact location")
	}

	// 3. LocationKind = approximate
	userApprox := env.registerUser("posts_succ_approx")
	optsApprox := postsCreateOptions{
		Caption:      postsStringPtr("hello approx"),
		Visibility:   postsStringPtr("friends_of_friends"),
		LocationKind: postsStringPtr("approximate"),
		Locality:     postsStringPtr("Tokyo, Japan"),
		ImageData:    tinyJPEG(),
	}
	recApprox := postsDoCreate(env, userApprox.Token, optsApprox)
	if recApprox.Code != http.StatusCreated {
		t.Fatalf("expected 201 for approx location, got %d: %s", recApprox.Code, recApprox.Body.String())
	}
	postApprox := decodeJSON[postsPostResponse](t, recApprox)
	if postApprox.Location.Kind != "approximate" || postApprox.Location.Locality == nil || *postApprox.Location.Locality != "Tokyo, Japan" {
		t.Fatalf("unexpected approx location: %+v", postApprox.Location)
	}
	if postApprox.Location.Latitude != nil || postApprox.Location.Longitude != nil {
		t.Fatalf("expected nil lat/lng for approx location")
	}
}

// TestIntegrationPosts_GetPost_Visibility_AuthorAndNotFound tests author access and 404 for nonexistent post.
func TestIntegrationPosts_GetPost_Visibility_AuthorAndNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()
	author := env.registerUser("posts_vis_auth")

	// 1. Unauthenticated request
	recUnauth := env.do(http.MethodGet, "/api/posts/"+uuid.NewString(), "", nil)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated GET /api/posts/:id, got %d: %s", recUnauth.Code, recUnauth.Body.String())
	}

	// 2. Non-existent post
	recNotFound := env.do(http.MethodGet, "/api/posts/"+uuid.NewString(), author.Token, nil)
	if recNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent post, got %d: %s", recNotFound.Code, recNotFound.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recNotFound)
	if errResp.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errResp.Error)
	}

	// 3. Author viewing own post (all visibilities)
	postID := env.createPost(author)

	recAuthor := env.do(http.MethodGet, "/api/posts/"+postID, author.Token, nil)
	if recAuthor.Code != http.StatusOK {
		t.Fatalf("author expected 200 viewing own post, got %d: %s", recAuthor.Code, recAuthor.Body.String())
	}
	p := decodeJSON[postsPostResponse](t, recAuthor)
	if p.ID != postID || p.Author.ID != author.ID {
		t.Fatalf("unexpected post data for author: %+v", p)
	}
}

// TestIntegrationPosts_GetPost_Visibility_ViewerPostedRequirement verifies that a viewer
// who has NOT posted in today's active window cannot view others' posts (returns 404),
// and can view them after posting.
func TestIntegrationPosts_GetPost_Visibility_ViewerPostedRequirement(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("posts_vis_req_a")
	viewer := env.registerUser("posts_vis_req_v")

	postID := env.createPost(author)

	// Viewer has NOT posted today
	recBeforePosting := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil)
	if recBeforePosting.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when viewer has not posted today, got %d: %s", recBeforePosting.Code, recBeforePosting.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recBeforePosting)
	if errResp.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errResp.Error)
	}

	// Viewer posts today
	_ = env.createPost(viewer)

	// Now viewer can view public post
	recAfterPosting := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil)
	if recAfterPosting.Code != http.StatusOK {
		t.Fatalf("expected 200 after viewer posted today, got %d: %s", recAfterPosting.Code, recAfterPosting.Body.String())
	}
	p := decodeJSON[postsPostResponse](t, recAfterPosting)
	if p.ID != postID {
		t.Fatalf("expected post ID %s, got %s", postID, p.ID)
	}
}

// TestIntegrationPosts_GetPost_Visibility_PublicFriendsAndFriendsOfFriends tests
// visibility rules across public, friends, and friends_of_friends settings.
func TestIntegrationPosts_GetPost_Visibility_PublicFriendsAndFriendsOfFriends(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("posts_vrel_a")
	friend := env.registerUser("posts_vrel_b")
	friendOfFriend := env.registerUser("posts_vrel_c")
	stranger := env.registerUser("posts_vrel_d")

	// Set up friendships: A <-> B and B <-> C (so C is friend of friend of A, but not direct friend)
	postsMakeFriendship(t, env, author, friend)
	postsMakeFriendship(t, env, friend, friendOfFriend)

	// Everyone posts so everyone is eligible based on "posted today" rule
	_ = env.createPost(friend)
	_ = env.createPost(friendOfFriend)
	_ = env.createPost(stranger)

	// 1. Visibility = "public"
	optsPublic := postsDefaultCreateOptions()
	optsPublic.Visibility = postsStringPtr("public")
	recPub := postsDoCreate(env, author.Token, optsPublic)
	if recPub.Code != http.StatusCreated {
		t.Fatalf("failed to create public post: %s", recPub.Body.String())
	}
	publicPostID := decodeJSON[postsPostResponse](t, recPub).ID

	for _, u := range []testUser{friend, friendOfFriend, stranger} {
		rec := env.do(http.MethodGet, "/api/posts/"+publicPostID, u.Token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("user %s expected 200 for public post, got %d: %s", u.Username, rec.Code, rec.Body.String())
		}
	}

	// 2. Visibility = "friends": author updates their post to friends
	patchBody := map[string]string{"visibility": "friends"}
	recPatchFriends := env.do(http.MethodPatch, "/api/posts/"+publicPostID, author.Token, patchBody)
	if recPatchFriends.Code != http.StatusOK {
		t.Fatalf("failed to patch visibility to friends: %s", recPatchFriends.Body.String())
	}

	// Friend can view
	recFriend := env.do(http.MethodGet, "/api/posts/"+publicPostID, friend.Token, nil)
	if recFriend.Code != http.StatusOK {
		t.Fatalf("friend expected 200 for friends-only post, got %d: %s", recFriend.Code, recFriend.Body.String())
	}

	// Friend of friend cannot view
	recFoF := env.do(http.MethodGet, "/api/posts/"+publicPostID, friendOfFriend.Token, nil)
	if recFoF.Code != http.StatusNotFound {
		t.Fatalf("friend-of-friend expected 404 for friends-only post, got %d: %s", recFoF.Code, recFoF.Body.String())
	}

	// Stranger cannot view
	recStranger := env.do(http.MethodGet, "/api/posts/"+publicPostID, stranger.Token, nil)
	if recStranger.Code != http.StatusNotFound {
		t.Fatalf("stranger expected 404 for friends-only post, got %d: %s", recStranger.Code, recStranger.Body.String())
	}

	// 3. Visibility = "friends_of_friends": author updates their post
	patchBodyFoF := map[string]string{"visibility": "friends_of_friends"}
	recPatchFoF := env.do(http.MethodPatch, "/api/posts/"+publicPostID, author.Token, patchBodyFoF)
	if recPatchFoF.Code != http.StatusOK {
		t.Fatalf("failed to patch visibility to friends_of_friends: %s", recPatchFoF.Body.String())
	}

	// Direct friend can view
	recFriend2 := env.do(http.MethodGet, "/api/posts/"+publicPostID, friend.Token, nil)
	if recFriend2.Code != http.StatusOK {
		t.Fatalf("friend expected 200 for friends_of_friends post, got %d: %s", recFriend2.Code, recFriend2.Body.String())
	}

	// Friend of friend can view
	recFoF2 := env.do(http.MethodGet, "/api/posts/"+publicPostID, friendOfFriend.Token, nil)
	if recFoF2.Code != http.StatusOK {
		t.Fatalf("friend-of-friend expected 200 for friends_of_friends post, got %d: %s", recFoF2.Code, recFoF2.Body.String())
	}

	// Stranger still cannot view
	recStranger2 := env.do(http.MethodGet, "/api/posts/"+publicPostID, stranger.Token, nil)
	if recStranger2.Code != http.StatusNotFound {
		t.Fatalf("stranger expected 404 for friends_of_friends post, got %d: %s", recStranger2.Code, recStranger2.Body.String())
	}
}

// TestIntegrationPosts_GetPost_Visibility_BlockedRelations verifies that blocking in either
// direction prevents viewing the post.
func TestIntegrationPosts_GetPost_Visibility_BlockedRelations(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("posts_vblk_a")
	viewer1 := env.registerUser("posts_vblk_v1")
	viewer2 := env.registerUser("posts_vblk_v2")

	postID := env.createPost(author)
	_ = env.createPost(viewer1)
	_ = env.createPost(viewer2)

	// Initially, viewer1 and viewer2 can see author's public post
	rec1 := env.do(http.MethodGet, "/api/posts/"+postID, viewer1.Token, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("viewer1 expected 200 before block, got %d", rec1.Code)
	}

	rec2 := env.do(http.MethodGet, "/api/posts/"+postID, viewer2.Token, nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("viewer2 expected 200 before block, got %d", rec2.Code)
	}

	// Case 1: author blocks viewer1 -> viewer1 gets 404
	postsBlockUser(t, env, author, viewer1)
	recBlock1 := env.do(http.MethodGet, "/api/posts/"+postID, viewer1.Token, nil)
	if recBlock1.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when author blocks viewer, got %d: %s", recBlock1.Code, recBlock1.Body.String())
	}

	// Case 2: viewer2 blocks author -> viewer2 gets 404
	postsBlockUser(t, env, viewer2, author)
	recBlock2 := env.do(http.MethodGet, "/api/posts/"+postID, viewer2.Token, nil)
	if recBlock2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when viewer blocks author, got %d: %s", recBlock2.Code, recBlock2.Body.String())
	}
}

// TestIntegrationPosts_GetPost_Visibility_WindowEndedAndDeleted verifies:
// - Viewer cannot see post once window has ended (404), but author can still view it (200).
// - Deleted post returns 404 for both viewer and author.
func TestIntegrationPosts_GetPost_Visibility_WindowEndedAndDeleted(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	author := env.registerUser("posts_vend_a")
	viewer := env.registerUser("posts_vend_v")

	postID := env.createPost(author)
	_ = env.createPost(viewer)

	// Both can view initially
	if rec := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer expected 200 initially, got %d", rec.Code)
	}

	// End the window
	if _, err := env.DB.Exec(`UPDATE posting_windows SET ended_at = clock_timestamp() WHERE ended_at IS NULL`); err != nil {
		t.Fatalf("failed to end posting window: %v", err)
	}

	// Viewer gets 404
	recViewer := env.do(http.MethodGet, "/api/posts/"+postID, viewer.Token, nil)
	if recViewer.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for viewer after window ended, got %d: %s", recViewer.Code, recViewer.Body.String())
	}

	// Author can still view own post after window ended
	recAuthor := env.do(http.MethodGet, "/api/posts/"+postID, author.Token, nil)
	if recAuthor.Code != http.StatusOK {
		t.Fatalf("expected 200 for author after window ended, got %d: %s", recAuthor.Code, recAuthor.Body.String())
	}

	// Author deletes the post
	recDel := env.do(http.MethodDelete, "/api/posts/"+postID, author.Token, nil)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d: %s", recDel.Code, recDel.Body.String())
	}

	// Now author also gets 404
	recAuthorDel := env.do(http.MethodGet, "/api/posts/"+postID, author.Token, nil)
	if recAuthorDel.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for author after post deleted, got %d: %s", recAuthorDel.Code, recAuthorDel.Body.String())
	}
}

// TestIntegrationPosts_GetMyCurrentPost covers GET /api/posts/me/current across
// unauthorized, no window, window without post, with post, and after deletion.
func TestIntegrationPosts_GetMyCurrentPost(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("posts_mecur_u1")

	// 1. Unauthorized
	recUnauth := env.do(http.MethodGet, "/api/posts/me/current", "", nil)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized, got %d: %s", recUnauth.Code, recUnauth.Body.String())
	}

	// 2. No window open
	recNoWin := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recNoWin.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when no window open, got %d: %s", recNoWin.Code, recNoWin.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recNoWin)
	if errResp.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errResp.Error)
	}

	// 3. Window open, but user hasn't posted
	env.openPostingWindow()
	recNoPost := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recNoPost.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when user has not posted, got %d: %s", recNoPost.Code, recNoPost.Body.String())
	}

	// 4. User posts
	postID := env.createPost(user)

	recHasPost := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recHasPost.Code != http.StatusOK {
		t.Fatalf("expected 200 for current post, got %d: %s", recHasPost.Code, recHasPost.Body.String())
	}
	myPost := decodeJSON[postsPostResponse](t, recHasPost)
	if myPost.ID != postID || myPost.Author.ID != user.ID {
		t.Fatalf("unexpected current post data: %+v", myPost)
	}

	// 5. User deletes post
	recDel := env.do(http.MethodDelete, "/api/posts/"+postID, user.Token, nil)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("failed to delete post: %s", recDel.Body.String())
	}

	recAfterDel := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recAfterDel.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after post deleted, got %d: %s", recAfterDel.Code, recAfterDel.Body.String())
	}
}

// TestIntegrationPosts_PatchPost_ValidationAndPermissions covers:
// - unauthorized (401)
// - nonexistent post (404)
// - another user's post (403 post_not_editable)
// - empty body (400 invalid_post)
// - invalid caption > 1000 chars (400 invalid_post)
// - invalid visibility (400 invalid_post)
func TestIntegrationPosts_PatchPost_ValidationAndPermissions(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user1 := env.registerUser("posts_pch_u1")
	user2 := env.registerUser("posts_pch_u2")

	postID := env.createPost(user1)

	// 1. Unauthorized
	recUnauth := env.do(http.MethodPatch, "/api/posts/"+postID, "", map[string]string{"caption": "new"})
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized, got %d: %s", recUnauth.Code, recUnauth.Body.String())
	}

	// 2. Non-existent post
	recNotFound := env.do(http.MethodPatch, "/api/posts/"+uuid.NewString(), user1.Token, map[string]string{"caption": "new"})
	if recNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent post, got %d: %s", recNotFound.Code, recNotFound.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recNotFound)
	if errResp.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errResp.Error)
	}

	// 3. Another user's post (403 post_not_editable)
	recOther := env.do(http.MethodPatch, "/api/posts/"+postID, user2.Token, map[string]string{"caption": "hacked"})
	if recOther.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when updating another user's post, got %d: %s", recOther.Code, recOther.Body.String())
	}
	errRespOther := decodeJSON[postsAPIError](t, recOther)
	if errRespOther.Error != "post_not_editable" {
		t.Fatalf("expected post_not_editable, got %s", errRespOther.Error)
	}

	// 4. Empty body (neither caption nor visibility)
	recEmpty := env.do(http.MethodPatch, "/api/posts/"+postID, user1.Token, map[string]any{})
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty patch body, got %d: %s", recEmpty.Code, recEmpty.Body.String())
	}
	errRespEmpty := decodeJSON[postsAPIError](t, recEmpty)
	if errRespEmpty.Error != "invalid_post" {
		t.Fatalf("expected invalid_post, got %s", errRespEmpty.Error)
	}

	// 5. Invalid caption (> 1000 chars)
	longCap := strings.Repeat("x", 1001)
	recLong := env.do(http.MethodPatch, "/api/posts/"+postID, user1.Token, map[string]string{"caption": longCap})
	if recLong.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for caption > 1000 chars, got %d: %s", recLong.Code, recLong.Body.String())
	}
	errRespLong := decodeJSON[postsAPIError](t, recLong)
	if errRespLong.Error != "invalid_post" {
		t.Fatalf("expected invalid_post, got %s", errRespLong.Error)
	}

	// 6. Invalid visibility
	recBadVis := env.do(http.MethodPatch, "/api/posts/"+postID, user1.Token, map[string]string{"visibility": "secret"})
	if recBadVis.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid visibility, got %d: %s", recBadVis.Code, recBadVis.Body.String())
	}
	errRespBadVis := decodeJSON[postsAPIError](t, recBadVis)
	if errRespBadVis.Error != "invalid_post" {
		t.Fatalf("expected invalid_post, got %s", errRespBadVis.Error)
	}
}

// TestIntegrationPosts_PatchPost_WindowEnded verifies that PATCH returns 403 post_not_editable
// if the posting window has ended.
func TestIntegrationPosts_PatchPost_WindowEnded(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("posts_pchend_u1")
	postID := env.createPost(user)

	// End posting window
	if _, err := env.DB.Exec(`UPDATE posting_windows SET ended_at = clock_timestamp() WHERE ended_at IS NULL`); err != nil {
		t.Fatalf("failed to end posting window: %v", err)
	}

	rec := env.do(http.MethodPatch, "/api/posts/"+postID, user.Token, map[string]string{"caption": "too late"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when updating post after window ended, got %d: %s", rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, rec)
	if errResp.Error != "post_not_editable" {
		t.Fatalf("expected post_not_editable, got %s", errResp.Error)
	}
}

// TestIntegrationPosts_PatchPost_Success tests successfully updating caption only,
// visibility only, and both fields together, asserting return values and GET persistence.
func TestIntegrationPosts_PatchPost_Success(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("posts_pchok_u1")
	postID := env.createPost(user)

	// 1. Update caption only
	recCap := env.do(http.MethodPatch, "/api/posts/"+postID, user.Token, map[string]string{
		"caption": "updated caption 1",
	})
	if recCap.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch caption, got %d: %s", recCap.Code, recCap.Body.String())
	}
	postCap := decodeJSON[postsPostResponse](t, recCap)
	if postCap.Caption != "updated caption 1" || postCap.Visibility != "public" {
		t.Fatalf("unexpected patch response: %+v", postCap)
	}

	// Verify via GET
	getRec1 := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	getPost1 := decodeJSON[postsPostResponse](t, getRec1)
	if getPost1.Caption != "updated caption 1" {
		t.Fatalf("persisted caption expected 'updated caption 1', got %s", getPost1.Caption)
	}

	// 2. Update visibility only
	recVis := env.do(http.MethodPatch, "/api/posts/"+postID, user.Token, map[string]string{
		"visibility": "friends",
	})
	if recVis.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch visibility, got %d: %s", recVis.Code, recVis.Body.String())
	}
	postVis := decodeJSON[postsPostResponse](t, recVis)
	if postVis.Visibility != "friends" || postVis.Caption != "updated caption 1" {
		t.Fatalf("unexpected patch response: %+v", postVis)
	}

	// 3. Update both caption and visibility
	recBoth := env.do(http.MethodPatch, "/api/posts/"+postID, user.Token, map[string]string{
		"caption":    "updated caption 2",
		"visibility": "friends_of_friends",
	})
	if recBoth.Code != http.StatusOK {
		t.Fatalf("expected 200 on patch both, got %d: %s", recBoth.Code, recBoth.Body.String())
	}
	postBoth := decodeJSON[postsPostResponse](t, recBoth)
	if postBoth.Caption != "updated caption 2" || postBoth.Visibility != "friends_of_friends" {
		t.Fatalf("unexpected patch response: %+v", postBoth)
	}

	// Verify via GET
	getRec2 := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	getPost2 := decodeJSON[postsPostResponse](t, getRec2)
	if getPost2.Caption != "updated caption 2" || getPost2.Visibility != "friends_of_friends" {
		t.Fatalf("persisted post mismatch: %+v", getPost2)
	}
}

// TestIntegrationPosts_DeletePost_ValidationAndPermissions covers:
// - unauthorized (401)
// - nonexistent post (404)
// - another user's post (403 post_not_editable)
// - already deleted post (404 post_not_found)
func TestIntegrationPosts_DeletePost_ValidationAndPermissions(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user1 := env.registerUser("posts_delval_u1")
	user2 := env.registerUser("posts_delval_u2")

	postID := env.createPost(user1)

	// 1. Unauthorized
	recUnauth := env.do(http.MethodDelete, "/api/posts/"+postID, "", nil)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthorized, got %d: %s", recUnauth.Code, recUnauth.Body.String())
	}

	// 2. Nonexistent post
	recNotFound := env.do(http.MethodDelete, "/api/posts/"+uuid.NewString(), user1.Token, nil)
	if recNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent post, got %d: %s", recNotFound.Code, recNotFound.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recNotFound)
	if errResp.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errResp.Error)
	}

	// 3. Another user's post (403 post_not_editable)
	recOther := env.do(http.MethodDelete, "/api/posts/"+postID, user2.Token, nil)
	if recOther.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when deleting another user's post, got %d: %s", recOther.Code, recOther.Body.String())
	}
	errRespOther := decodeJSON[postsAPIError](t, recOther)
	if errRespOther.Error != "post_not_editable" {
		t.Fatalf("expected post_not_editable, got %s", errRespOther.Error)
	}

	// 4. Delete own post -> 204
	recDel := env.do(http.MethodDelete, "/api/posts/"+postID, user1.Token, nil)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d: %s", recDel.Code, recDel.Body.String())
	}

	// 5. Delete already deleted post -> 404 post_not_found
	recAlreadyDel := env.do(http.MethodDelete, "/api/posts/"+postID, user1.Token, nil)
	if recAlreadyDel.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when deleting already deleted post, got %d: %s", recAlreadyDel.Code, recAlreadyDel.Body.String())
	}
	errRespAlreadyDel := decodeJSON[postsAPIError](t, recAlreadyDel)
	if errRespAlreadyDel.Error != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errRespAlreadyDel.Error)
	}
}

// TestIntegrationPosts_DeletePost_SuccessAndStateChanges verifies state changes
// after a post is deleted:
// - subsequent GET /api/posts/:postId -> 404
// - subsequent GET /api/posts/me/current -> 404
// - GET /api/posting-window/current -> hasPosted=false, canPost=true, deletionCount=1
func TestIntegrationPosts_DeletePost_SuccessAndStateChanges(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("posts_delstate_u1")
	postID := env.createPost(user)

	// Verify post exists
	if rec := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 before delete, got %d", rec.Code)
	}
	if rec := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on me/current before delete, got %d", rec.Code)
	}

	// Delete post
	recDel := env.do(http.MethodDelete, "/api/posts/"+postID, user.Token, nil)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d: %s", recDel.Code, recDel.Body.String())
	}

	// GET /api/posts/:postId -> 404
	recGetPost := env.do(http.MethodGet, "/api/posts/"+postID, user.Token, nil)
	if recGetPost.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for deleted post ID, got %d: %s", recGetPost.Code, recGetPost.Body.String())
	}

	// GET /api/posts/me/current -> 404
	recGetMeCur := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recGetMeCur.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for me/current after delete, got %d: %s", recGetMeCur.Code, recGetMeCur.Body.String())
	}

	// GET /api/posting-window/current -> state changes
	recWin := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	if recWin.Code != http.StatusOK {
		t.Fatalf("expected 200 for posting-window/current, got %d: %s", recWin.Code, recWin.Body.String())
	}
	winState := decodeJSON[postsCurrentWindowResponse](t, recWin)
	if winState.HasPosted != false || winState.CanPost != true || winState.DeletionCount != 1 {
		t.Fatalf("unexpected window state after delete: hasPosted=%v, canPost=%v, deletions=%d",
			winState.HasPosted, winState.CanPost, winState.DeletionCount)
	}
}

// TestIntegrationPosts_DeletePost_DeletionLimitAndRepost verifies the repost workflow:
// - User creates post 1 -> deletes post 1 (deletion count 1)
// - User can repost (post 2) -> deletes post 2 (deletion count 2)
// - User can no longer post a 3rd time (409 post_slot_unavailable)
func TestIntegrationPosts_DeletePost_DeletionLimitAndRepost(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	user := env.registerUser("posts_dellim_u1")

	// Post 1
	postID1 := env.createPost(user)
	recDel1 := env.do(http.MethodDelete, "/api/posts/"+postID1, user.Token, nil)
	if recDel1.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete post 1, got %d", recDel1.Code)
	}

	// Repost: Post 2
	opts2 := postsDefaultCreateOptions()
	opts2.Caption = postsStringPtr("repost 2")
	recPost2 := postsDoCreate(env, user.Token, opts2)
	if recPost2.Code != http.StatusCreated {
		t.Fatalf("expected 201 on repost 2, got %d: %s", recPost2.Code, recPost2.Body.String())
	}
	postID2 := decodeJSON[postsPostResponse](t, recPost2).ID

	// Verify me/current returns post 2
	recMeCur2 := env.do(http.MethodGet, "/api/posts/me/current", user.Token, nil)
	if recMeCur2.Code != http.StatusOK {
		t.Fatalf("expected 200 on me/current for repost 2, got %d", recMeCur2.Code)
	}
	if decodeJSON[postsPostResponse](t, recMeCur2).ID != postID2 {
		t.Fatalf("me/current should match post 2 ID")
	}

	// Delete Post 2
	recDel2 := env.do(http.MethodDelete, "/api/posts/"+postID2, user.Token, nil)
	if recDel2.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete post 2, got %d", recDel2.Code)
	}

	// Check window state: deletions = 2, canPost = false
	recWin := env.do(http.MethodGet, "/api/posting-window/current", user.Token, nil)
	winState := decodeJSON[postsCurrentWindowResponse](t, recWin)
	if winState.DeletionCount != 2 || winState.CanPost != false || winState.HasPosted != false {
		t.Fatalf("unexpected window state after 2 deletions: deletions=%d, canPost=%v, hasPosted=%v",
			winState.DeletionCount, winState.CanPost, winState.HasPosted)
	}

	// Attempt post 3 -> 409 post_slot_unavailable
	opts3 := postsDefaultCreateOptions()
	opts3.Caption = postsStringPtr("repost 3")
	recPost3 := postsDoCreate(env, user.Token, opts3)
	if recPost3.Code != http.StatusConflict {
		t.Fatalf("expected 409 on post 3 when 2 deletions reached, got %d: %s", recPost3.Code, recPost3.Body.String())
	}
	errResp := decodeJSON[postsAPIError](t, recPost3)
	if errResp.Error != "post_slot_unavailable" {
		t.Fatalf("expected post_slot_unavailable, got %s", errResp.Error)
	}
}
