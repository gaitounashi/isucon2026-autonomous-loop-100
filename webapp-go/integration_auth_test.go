package isureal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func authDecodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp APIErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error response: %v, body: %s", err, rec.Body.String())
	}
	return resp.Error
}

func TestIntegrationAuthRegister_Validation(t *testing.T) {
	env := newTestEnv(t)

	tests := []struct {
		name        string
		username    string
		displayName string
		region      string
		expectCode  int
		expectErr   string
	}{
		// Username validation: ^[a-z0-9_.-]{1,64}$
		{
			name:        "empty username",
			username:    "",
			displayName: "Valid Name",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "uppercase username",
			username:    "InvalidUser",
			displayName: "Valid Name",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "username with space",
			username:    "user name",
			displayName: "Valid Name",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "username with special character",
			username:    "user@domain",
			displayName: "Valid Name",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "username too long (65 chars)",
			username:    strings.Repeat("a", 65),
			displayName: "Valid Name",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},

		// DisplayName validation: validUserText(displayName, 1, 100)
		{
			name:        "empty displayName",
			username:    "valid_user_1",
			displayName: "",
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "displayName too long (101 runes)",
			username:    "valid_user_2",
			displayName: strings.Repeat("a", 101),
			region:      "asia",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},

		// Region validation: america, europe, asia
		{
			name:        "invalid region",
			username:    "valid_user_3",
			displayName: "Valid Name",
			region:      "antarctica",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
		{
			name:        "empty region",
			username:    "valid_user_4",
			displayName: "Valid Name",
			region:      "",
			expectCode:  http.StatusBadRequest,
			expectErr:   "invalid_registration",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
				"username":    tc.username,
				"displayName": tc.displayName,
				"region":      tc.region,
			})
			if rec.Code != tc.expectCode {
				t.Fatalf("expected status %d, got %d: %s", tc.expectCode, rec.Code, rec.Body.String())
			}
			if errCode := authDecodeError(t, rec); errCode != tc.expectErr {
				t.Fatalf("expected error code %q, got %q", tc.expectErr, errCode)
			}
		})
	}
}

func TestIntegrationAuthRegister_Success(t *testing.T) {
	env := newTestEnv(t)

	validCases := []struct {
		name        string
		username    string
		displayName string
		region      string
	}{
		{
			name:        "minimal username and unicode display name",
			username:    "u",
			displayName: "田中 太郎",
			region:      "asia",
		},
		{
			name:        "64-char username with allowed symbols",
			username:    "user.name_test-0123456789abcdefghijklmnopqrstuvwxyz0123456789abc",
			displayName: "Symbols User",
			region:      "europe",
		},
		{
			name:        "max length display name (100 runes) in america",
			username:    "user_america",
			displayName: strings.Repeat("日", 100),
			region:      "america",
		},
	}

	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
				"username":    tc.username,
				"displayName": tc.displayName,
				"region":      tc.region,
			})
			if rec.Code != http.StatusCreated {
				t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
			}
			resp := decodeJSON[registerResponse](t, rec)
			if resp.SecretKey == "" {
				t.Fatalf("expected non-empty secretKey")
			}
			if resp.User.ID == "" {
				t.Fatalf("expected non-empty user.id")
			}
			if resp.User.Username != tc.username {
				t.Fatalf("expected username %s, got %s", tc.username, resp.User.Username)
			}
			if resp.User.DisplayName != tc.displayName {
				t.Fatalf("expected displayName %s, got %s", tc.displayName, resp.User.DisplayName)
			}
			if resp.User.Region != tc.region {
				t.Fatalf("expected region %s, got %s", tc.region, resp.User.Region)
			}
			if resp.User.Bio != "" {
				t.Fatalf("expected empty bio, got %s", resp.User.Bio)
			}
			if resp.User.AccountType != "regular" {
				t.Fatalf("expected accountType regular, got %s", resp.User.AccountType)
			}
			if resp.User.Status != "active" {
				t.Fatalf("expected status active, got %s", resp.User.Status)
			}
		})
	}
}

func TestIntegrationAuthRegister_DuplicateUsername(t *testing.T) {
	env := newTestEnv(t)

	// First registration succeeds
	rec1 := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username":    "duplicate_target",
		"displayName": "Original",
		"region":      "asia",
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Second registration with the same username fails with 409 Conflict
	rec2 := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username":    "duplicate_target",
		"displayName": "Different Display Name",
		"region":      "europe",
	})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if errCode := authDecodeError(t, rec2); errCode != "username_taken" {
		t.Fatalf("expected error username_taken, got %s", errCode)
	}

	// Different username can still register
	rec3 := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username":    "duplicate_target_other",
		"displayName": "Other",
		"region":      "asia",
	})
	if rec3.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec3.Code, rec3.Body.String())
	}
}

func TestIntegrationAuthLogin_UnknownUser(t *testing.T) {
	env := newTestEnv(t)

	// Unknown user with valid format
	rec := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username":  "nonexistent_user",
		"secretKey": "some-secret-key",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if errCode := authDecodeError(t, rec); errCode != "invalid_credentials" {
		t.Fatalf("expected invalid_credentials, got %s", errCode)
	}

	// Unknown user with invalid username format (regex fails)
	recInvalid := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username":  "INVALID_USER_FORMAT",
		"secretKey": "some-secret-key",
	})
	if recInvalid.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d: %s", recInvalid.Code, recInvalid.Body.String())
	}
	if errCode := authDecodeError(t, recInvalid); errCode != "invalid_credentials" {
		t.Fatalf("expected invalid_credentials, got %s", errCode)
	}
}

func TestIntegrationAuthLogin_WrongSecret(t *testing.T) {
	env := newTestEnv(t)

	user := env.registerUser("auth_wrong_secret_user")

	tests := []struct {
		name      string
		secretKey string
	}{
		{"wrong key", "wrong-secret-key-12345"},
		{"empty key", ""},
		{"partial key", user.SecretKey[:len(user.SecretKey)-1]},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
				"username":  user.Username,
				"secretKey": tc.secretKey,
			})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status 401, got %d: %s", rec.Code, rec.Body.String())
			}
			if errCode := authDecodeError(t, rec); errCode != "invalid_credentials" {
				t.Fatalf("expected invalid_credentials, got %s", errCode)
			}
		})
	}
}

func TestIntegrationAuthLogin_SuccessAndReplacesPriorSession(t *testing.T) {
	env := newTestEnv(t)

	regRec := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username":    "auth_login_user",
		"displayName": "Auth Login Display",
		"region":      "asia",
	})
	if regRec.Code != http.StatusCreated {
		t.Fatalf("register failed with status %d: %s", regRec.Code, regRec.Body.String())
	}
	reg := decodeJSON[registerResponse](t, regRec)

	// First login
	loginRec1 := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username":  "auth_login_user",
		"secretKey": reg.SecretKey,
	})
	if loginRec1.Code != http.StatusOK {
		t.Fatalf("login 1 expected status 200, got %d: %s", loginRec1.Code, loginRec1.Body.String())
	}
	login1 := decodeJSON[loginResponse](t, loginRec1)
	if login1.Token == "" {
		t.Fatalf("expected non-empty token")
	}
	if login1.User.ID != reg.User.ID {
		t.Fatalf("expected user ID %s, got %s", reg.User.ID, login1.User.ID)
	}
	if login1.User.Username != "auth_login_user" {
		t.Fatalf("expected username auth_login_user, got %s", login1.User.Username)
	}
	if login1.User.DisplayName != "Auth Login Display" {
		t.Fatalf("expected displayName Auth Login Display, got %s", login1.User.DisplayName)
	}
	if login1.User.Region != "asia" {
		t.Fatalf("expected region asia, got %s", login1.User.Region)
	}
	if login1.User.AccountType != "regular" {
		t.Fatalf("expected accountType regular, got %s", login1.User.AccountType)
	}
	if login1.User.Status != "active" {
		t.Fatalf("expected status active, got %s", login1.User.Status)
	}

	// Verify token 1 works before second login
	meRec1 := env.do(http.MethodGet, "/api/me", login1.Token, nil)
	if meRec1.Code != http.StatusOK {
		t.Fatalf("GET /api/me with token1 before second login expected 200, got %d", meRec1.Code)
	}

	// Second login replaces the prior session
	loginRec2 := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username":  "auth_login_user",
		"secretKey": reg.SecretKey,
	})
	if loginRec2.Code != http.StatusOK {
		t.Fatalf("login 2 expected status 200, got %d: %s", loginRec2.Code, loginRec2.Body.String())
	}
	login2 := decodeJSON[loginResponse](t, loginRec2)
	if login2.Token == "" || login2.Token == login1.Token {
		t.Fatalf("expected distinct non-empty token, got %s and %s", login1.Token, login2.Token)
	}

	// Token 2 works
	meRec2 := env.do(http.MethodGet, "/api/me", login2.Token, nil)
	if meRec2.Code != http.StatusOK {
		t.Fatalf("GET /api/me with token2 expected 200, got %d", meRec2.Code)
	}

	// Token 1 has been invalidated by the new login
	meRec1After := env.do(http.MethodGet, "/api/me", login1.Token, nil)
	if meRec1After.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me with replaced token1 expected 401, got %d", meRec1After.Code)
	}
	if errCode := authDecodeError(t, meRec1After); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Verify DB state for token 1's session: active=false, reason='replaced'
	token1Hash := hex.EncodeToString(sha256Sum([]byte(login1.Token)))
	var token1Active bool
	var token1Reason string
	err := env.DB.QueryRow(`
		SELECT active, reason FROM login_sessions WHERE token_hash = $1
	`, token1Hash).Scan(&token1Active, &token1Reason)
	if err != nil {
		t.Fatalf("failed to query token1 session: %v", err)
	}
	if token1Active {
		t.Fatalf("expected token1 session to be inactive")
	}
	if token1Reason != "replaced" {
		t.Fatalf("expected token1 session reason 'replaced', got %s", token1Reason)
	}
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func TestIntegrationAuthLogout_Unauthorized(t *testing.T) {
	env := newTestEnv(t)

	// No token
	recNoToken := env.do(http.MethodPost, "/api/auth/logout", "", nil)
	if recNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/auth/logout without token expected 401, got %d: %s", recNoToken.Code, recNoToken.Body.String())
	}
	if errCode := authDecodeError(t, recNoToken); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Bogus token
	recBogus := env.do(http.MethodPost, "/api/auth/logout", "bogus-token-xyz", nil)
	if recBogus.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/auth/logout with bogus token expected 401, got %d: %s", recBogus.Code, recBogus.Body.String())
	}
	if errCode := authDecodeError(t, recBogus); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}
}

func TestIntegrationAuthLogout_InvalidatesToken(t *testing.T) {
	env := newTestEnv(t)

	user := env.registerUser("auth_logout_user")

	// Verify token works before logout
	meRec := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/me before logout expected 200, got %d", meRec.Code)
	}

	// Logout
	logoutRec := env.do(http.MethodPost, "/api/auth/logout", user.Token, nil)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/auth/logout expected 204, got %d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	if len(logoutRec.Body.Bytes()) > 0 {
		t.Fatalf("expected empty response body for 204, got %s", logoutRec.Body.String())
	}

	// Subsequent GET /api/me with logged-out token fails
	meAfter := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if meAfter.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/me after logout expected 401, got %d: %s", meAfter.Code, meAfter.Body.String())
	}
	if errCode := authDecodeError(t, meAfter); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Subsequent logout with same token fails
	logoutAgain := env.do(http.MethodPost, "/api/auth/logout", user.Token, nil)
	if logoutAgain.Code != http.StatusUnauthorized {
		t.Fatalf("repeated POST /api/auth/logout expected 401, got %d: %s", logoutAgain.Code, logoutAgain.Body.String())
	}
	if errCode := authDecodeError(t, logoutAgain); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Check DB state: session active is false and reason is 'logout'
	var sessionActive bool
	var sessionReason string
	var invalidatedAt *time.Time
	err := env.DB.QueryRow(`
		SELECT active, reason, invalidated_at FROM login_sessions WHERE user_id = $1
	`, user.ID).Scan(&sessionActive, &sessionReason, &invalidatedAt)
	if err != nil {
		t.Fatalf("failed to query login_sessions: %v", err)
	}
	if sessionActive {
		t.Fatalf("expected session active to be false")
	}
	if sessionReason != "logout" {
		t.Fatalf("expected session reason 'logout', got %q", sessionReason)
	}
	if invalidatedAt == nil {
		t.Fatalf("expected invalidated_at to be non-nil")
	}
}

func TestIntegrationAuthLogout_UserIsolation(t *testing.T) {
	env := newTestEnv(t)

	userA := env.registerUser("auth_logout_iso_a")
	userB := env.registerUser("auth_logout_iso_b")

	// Logout user A
	logoutRec := env.do(http.MethodPost, "/api/auth/logout", userA.Token, nil)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout user A failed: %d", logoutRec.Code)
	}

	// User A token is now invalid
	recA := env.do(http.MethodGet, "/api/me", userA.Token, nil)
	if recA.Code != http.StatusUnauthorized {
		t.Fatalf("user A expected 401, got %d", recA.Code)
	}

	// User B token remains valid
	recB := env.do(http.MethodGet, "/api/me", userB.Token, nil)
	if recB.Code != http.StatusOK {
		t.Fatalf("user B expected 200, got %d", recB.Code)
	}
}

func TestIntegrationAuthGetMe_AuthAndRegionChecks(t *testing.T) {
	env := newTestEnv(t)

	// 1. Without token: 401
	recNoAuth := env.do(http.MethodGet, "/api/me", "", nil)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recNoAuth.Code)
	}
	if errCode := authDecodeError(t, recNoAuth); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// 2. Bad token: 401
	recBadToken := env.do(http.MethodGet, "/api/me", "invalid-token", nil)
	if recBadToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recBadToken.Code)
	}
	if errCode := authDecodeError(t, recBadToken); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// 3. User in different region (europe when server is asia): 403 region_mismatch
	userEurope := env.registerUserInRegion("auth_user_europe", "europe")
	recEurope := env.do(http.MethodGet, "/api/me", userEurope.Token, nil)
	if recEurope.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recEurope.Code, recEurope.Body.String())
	}
	if errCode := authDecodeError(t, recEurope); errCode != "region_mismatch" {
		t.Fatalf("expected region_mismatch, got %s", errCode)
	}

	// 4. User in same region (asia): 200 OK
	userAsia := env.registerUserInRegion("auth_user_asia", "asia")
	recAsia := env.do(http.MethodGet, "/api/me", userAsia.Token, nil)
	if recAsia.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recAsia.Code, recAsia.Body.String())
	}
	me := decodeJSON[getMeResponse](t, recAsia)
	if me.ID != userAsia.ID {
		t.Fatalf("expected id %s, got %s", userAsia.ID, me.ID)
	}
	if me.Username != "auth_user_asia" {
		t.Fatalf("expected username auth_user_asia, got %s", me.Username)
	}
	if me.DisplayName != "auth_user_asia" {
		t.Fatalf("expected displayName auth_user_asia, got %s", me.DisplayName)
	}
	if me.Bio != "" {
		t.Fatalf("expected empty bio, got %s", me.Bio)
	}
	if me.Region != "asia" {
		t.Fatalf("expected region asia, got %s", me.Region)
	}
	if me.AccountType != "regular" {
		t.Fatalf("expected accountType regular, got %s", me.AccountType)
	}
	if me.Status != "active" {
		t.Fatalf("expected status active, got %s", me.Status)
	}
	if me.ProfileImageURL != nil {
		t.Fatalf("expected nil profileImageUrl, got %v", *me.ProfileImageURL)
	}
}

func TestIntegrationAuthUpdateMe_Validation(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("auth_patch_val_user")

	tests := []struct {
		name       string
		body       any
		expectCode int
		expectErr  string
	}{
		{
			name:       "empty json object",
			body:       map[string]any{},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "all fields explicitly null",
			body: map[string]any{
				"displayName": nil,
				"bio":         nil,
				"region":      nil,
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "displayName empty string",
			body: map[string]any{
				"displayName": "",
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "displayName too long (> 100 runes)",
			body: map[string]any{
				"displayName": strings.Repeat("x", 101),
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "bio too long (> 500 runes)",
			body: map[string]any{
				"bio": strings.Repeat("b", 501),
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "invalid region name",
			body: map[string]any{
				"region": "antarctica",
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
		{
			name: "empty region string",
			body: map[string]any{
				"region": "",
			},
			expectCode: http.StatusBadRequest,
			expectErr:  "invalid_profile",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPatch, "/api/me", user.Token, tc.body)
			if rec.Code != tc.expectCode {
				t.Fatalf("expected status %d, got %d: %s", tc.expectCode, rec.Code, rec.Body.String())
			}
			if errCode := authDecodeError(t, rec); errCode != tc.expectErr {
				t.Fatalf("expected error %q, got %q", tc.expectErr, errCode)
			}
		})
	}
}

func TestIntegrationAuthUpdateMe_Unauthorized(t *testing.T) {
	env := newTestEnv(t)

	// Without token
	recNoToken := env.do(http.MethodPatch, "/api/me", "", map[string]string{
		"displayName": "New Name",
	})
	if recNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recNoToken.Code)
	}
	if errCode := authDecodeError(t, recNoToken); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Bogus token
	recBogus := env.do(http.MethodPatch, "/api/me", "bogus-patch-token", map[string]string{
		"displayName": "New Name",
	})
	if recBogus.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recBogus.Code)
	}
	if errCode := authDecodeError(t, recBogus); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}
}

func TestIntegrationAuthUpdateMe_PersistenceAndPartial(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("auth_patch_persist_user")

	// 1. Partial update: displayName only
	recName := env.do(http.MethodPatch, "/api/me", user.Token, map[string]any{
		"displayName": "Updated Name",
	})
	if recName.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recName.Code, recName.Body.String())
	}
	respName := decodeJSON[updateMeResponse](t, recName)
	if respName.DisplayName != "Updated Name" {
		t.Fatalf("expected displayName 'Updated Name', got %s", respName.DisplayName)
	}
	if respName.Bio != "" {
		t.Fatalf("expected bio to remain empty, got %s", respName.Bio)
	}
	if respName.Region != "asia" {
		t.Fatalf("expected region to remain asia, got %s", respName.Region)
	}

	// Verify persistence via GET /api/me
	getRec1 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	get1 := decodeJSON[getMeResponse](t, getRec1)
	if get1.DisplayName != "Updated Name" {
		t.Fatalf("GET /api/me expected displayName 'Updated Name', got %s", get1.DisplayName)
	}

	// 2. Partial update: bio only (including max 500 runes boundary)
	bioText := strings.Repeat("★", 500)
	recBio := env.do(http.MethodPatch, "/api/me", user.Token, map[string]any{
		"bio": bioText,
	})
	if recBio.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recBio.Code, recBio.Body.String())
	}
	respBio := decodeJSON[updateMeResponse](t, recBio)
	if respBio.Bio != bioText {
		t.Fatalf("expected 500-rune bio in response")
	}
	if respBio.DisplayName != "Updated Name" {
		t.Fatalf("expected displayName to remain 'Updated Name', got %s", respBio.DisplayName)
	}

	// Verify persistence via GET /api/me
	getRec2 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	get2 := decodeJSON[getMeResponse](t, getRec2)
	if get2.Bio != bioText {
		t.Fatalf("GET /api/me expected updated bio")
	}

	// Reset bio to empty string (valid minimum length 0)
	recBioEmpty := env.do(http.MethodPatch, "/api/me", user.Token, map[string]any{
		"bio": "",
	})
	if recBioEmpty.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty bio, got %d: %s", recBioEmpty.Code, recBioEmpty.Body.String())
	}
	respBioEmpty := decodeJSON[updateMeResponse](t, recBioEmpty)
	if respBioEmpty.Bio != "" {
		t.Fatalf("expected empty bio, got %s", respBioEmpty.Bio)
	}

	// 3. Update all fields at once
	recAll := env.do(http.MethodPatch, "/api/me", user.Token, map[string]any{
		"displayName": "Final Name",
		"bio":         "Final Bio",
		"region":      "asia",
	})
	if recAll.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recAll.Code, recAll.Body.String())
	}
	respAll := decodeJSON[updateMeResponse](t, recAll)
	if respAll.DisplayName != "Final Name" || respAll.Bio != "Final Bio" || respAll.Region != "asia" {
		t.Fatalf("unexpected fields in response: %+v", respAll)
	}

	// 4. Update region: PATCH succeeds, DB is updated, but subsequent GET /api/me triggers RegionMiddleware mismatch
	recRegion := env.do(http.MethodPatch, "/api/me", user.Token, map[string]any{
		"region": "europe",
	})
	if recRegion.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recRegion.Code, recRegion.Body.String())
	}

	// Verify in DB that region changed to europe
	var dbRegion string
	if err := env.DB.Get(&dbRegion, `SELECT region FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("failed to query user region: %v", err)
	}
	if dbRegion != "europe" {
		t.Fatalf("expected db region 'europe', got %s", dbRegion)
	}

	// Subsequent GET /api/me now fails with 403 region_mismatch because server is configured for asia
	getRecEurope := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if getRecEurope.Code != http.StatusForbidden {
		t.Fatalf("expected 403 region_mismatch, got %d: %s", getRecEurope.Code, getRecEurope.Body.String())
	}
	if errCode := authDecodeError(t, getRecEurope); errCode != "region_mismatch" {
		t.Fatalf("expected region_mismatch, got %s", errCode)
	}
}

func TestIntegrationAuthUpdateMe_UserIsolation(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("auth_user_a")
	userB := env.registerUser("auth_user_b")

	// User A updates their profile
	patchRec := env.do(http.MethodPatch, "/api/me", userA.Token, map[string]any{
		"displayName": "User A Updated",
		"bio":         "User A Bio",
	})
	if patchRec.Code != http.StatusOK {
		t.Fatalf("User A patch failed: %d", patchRec.Code)
	}

	// User B's profile remains untouched
	getRecB := env.do(http.MethodGet, "/api/me", userB.Token, nil)
	if getRecB.Code != http.StatusOK {
		t.Fatalf("User B get failed: %d", getRecB.Code)
	}
	meB := decodeJSON[getMeResponse](t, getRecB)
	if meB.DisplayName != "auth_user_b" {
		t.Fatalf("User B displayName changed unexpectedly to %s", meB.DisplayName)
	}
	if meB.Bio != "" {
		t.Fatalf("User B bio changed unexpectedly to %s", meB.Bio)
	}
}

func TestIntegrationAuthAccountDeletion_AuthAndRegion(t *testing.T) {
	env := newTestEnv(t)

	// No token
	recNoToken := env.do(http.MethodPost, "/api/me/account-deletion", "", nil)
	if recNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recNoToken.Code)
	}
	if errCode := authDecodeError(t, recNoToken); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// Bogus token
	recBogus := env.do(http.MethodPost, "/api/me/account-deletion", "invalid-token", nil)
	if recBogus.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recBogus.Code)
	}
	if errCode := authDecodeError(t, recBogus); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// User in different region (europe when server is asia)
	userEurope := env.registerUserInRegion("auth_del_europe", "europe")
	recEurope := env.do(http.MethodPost, "/api/me/account-deletion", userEurope.Token, nil)
	if recEurope.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", recEurope.Code)
	}
	if errCode := authDecodeError(t, recEurope); errCode != "region_mismatch" {
		t.Fatalf("expected region_mismatch, got %s", errCode)
	}
}

func TestIntegrationAuthAccountDeletion_SessionAndLoginEffects(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("auth_delete_target")

	// Verify token works before deletion
	if rec := env.do(http.MethodGet, "/api/me", user.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("token failed before deletion: %d", rec.Code)
	}

	// Insert an extra active session directly to test that account deletion invalidates all sessions of the user
	extraToken := "extra-active-session-" + uuid.NewString()
	extraTokenHash := hex.EncodeToString(sha256Sum([]byte(extraToken)))
	_, err := env.DB.Exec(`
		INSERT INTO login_sessions (id, user_id, token_hash, active)
		VALUES ($1, $2, $3, true)
	`, uuid.NewString(), user.ID, extraTokenHash)
	if err != nil {
		t.Fatalf("failed to insert extra active session: %v", err)
	}

	// Verify extra token works before deletion
	if recExtra := env.do(http.MethodGet, "/api/me", extraToken, nil); recExtra.Code != http.StatusOK {
		t.Fatalf("extraToken failed before deletion: %d: %s", recExtra.Code, recExtra.Body.String())
	}

	// Execute account deletion with primary token
	delRec := env.do(http.MethodPost, "/api/me/account-deletion", user.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", delRec.Code, delRec.Body.String())
	}
	if len(delRec.Body.Bytes()) > 0 {
		t.Fatalf("expected empty body for 204, got %s", delRec.Body.String())
	}

	// 1. Primary token is invalidated
	getRec1 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	if getRec1.Code != http.StatusUnauthorized {
		t.Fatalf("primary token expected 401 after deletion, got %d", getRec1.Code)
	}
	if errCode := authDecodeError(t, getRec1); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// 2. Extra active session token is also invalidated
	getRecExtra := env.do(http.MethodGet, "/api/me", extraToken, nil)
	if getRecExtra.Code != http.StatusUnauthorized {
		t.Fatalf("extra token expected 401 after deletion, got %d", getRecExtra.Code)
	}
	if errCode := authDecodeError(t, getRecExtra); errCode != "invalid_session" {
		t.Fatalf("expected invalid_session, got %s", errCode)
	}

	// 3. Repeated deletion call fails (token is invalid)
	delAgainRec := env.do(http.MethodPost, "/api/me/account-deletion", user.Token, nil)
	if delAgainRec.Code != http.StatusUnauthorized {
		t.Fatalf("repeated deletion expected 401, got %d", delAgainRec.Code)
	}

	// 4. Login attempt with valid secretKey fails with invalid_credentials
	loginAgain := env.do(http.MethodPost, "/api/auth/login", "", map[string]string{
		"username":  user.Username,
		"secretKey": user.SecretKey,
	})
	if loginAgain.Code != http.StatusUnauthorized {
		t.Fatalf("login of deleted user expected 401, got %d: %s", loginAgain.Code, loginAgain.Body.String())
	}
	if errCode := authDecodeError(t, loginAgain); errCode != "invalid_credentials" {
		t.Fatalf("expected invalid_credentials, got %s", errCode)
	}

	// 5. Direct DB assertions
	var userStatus string
	var deletedAt *time.Time
	err = env.DB.QueryRow(`SELECT status, deleted_at FROM users WHERE id = $1`, user.ID).Scan(&userStatus, &deletedAt)
	if err != nil {
		t.Fatalf("failed to query users table: %v", err)
	}
	if userStatus != "deleted" {
		t.Fatalf("expected user status 'deleted', got %s", userStatus)
	}
	if deletedAt == nil {
		t.Fatalf("expected deleted_at to be non-nil")
	}

	var activeSessionCount int
	if err := env.DB.Get(&activeSessionCount, `SELECT count(*) FROM login_sessions WHERE user_id = $1 AND active = true`, user.ID); err != nil {
		t.Fatalf("failed to query login_sessions: %v", err)
	}
	if activeSessionCount != 0 {
		t.Fatalf("expected 0 active sessions, got %d", activeSessionCount)
	}

	var reasons []string
	if err := env.DB.Select(&reasons, `SELECT reason FROM login_sessions WHERE user_id = $1`, user.ID); err != nil {
		t.Fatalf("failed to query login_sessions reasons: %v", err)
	}
	for _, r := range reasons {
		if r != "account_deletion" {
			t.Fatalf("expected reason 'account_deletion', got %s", r)
		}
	}

	// 6. Attempting to register again with same username fails (username still reserved)
	reRegisterRec := env.do(http.MethodPost, "/api/auth/register", "", map[string]string{
		"username":    user.Username,
		"displayName": "Re-registration",
		"region":      "asia",
	})
	if reRegisterRec.Code != http.StatusConflict {
		t.Fatalf("re-registration expected 409 Conflict, got %d: %s", reRegisterRec.Code, reRegisterRec.Body.String())
	}
	if errCode := authDecodeError(t, reRegisterRec); errCode != "username_taken" {
		t.Fatalf("expected username_taken, got %s", errCode)
	}
}

func TestIntegrationAuthAccountDeletion_VisibilityEffects(t *testing.T) {
	env := newTestEnv(t)
	env.openPostingWindow()

	alice := env.registerUser("auth_alice")
	bob := env.registerUser("auth_bob")

	// Both Alice and Bob create a post so Bob has posted today and can view feeds
	alicePostID := env.createPost(alice)
	if alicePostID == "" {
		t.Fatalf("expected alicePostID from createPost")
	}
	bobPostID := env.createPost(bob)
	if bobPostID == "" {
		t.Fatalf("expected bobPostID from createPost")
	}

	// Before deletion: Bob can see Alice's profile, post, and search Alice
	recUserPre := env.do(http.MethodGet, "/api/users/"+alice.ID, bob.Token, nil)
	if recUserPre.Code != http.StatusOK {
		t.Fatalf("GET /api/users/:id before deletion expected 200, got %d", recUserPre.Code)
	}

	recPostPre := env.do(http.MethodGet, "/api/posts/"+alicePostID, bob.Token, nil)
	if recPostPre.Code != http.StatusOK {
		t.Fatalf("GET /api/posts/:id before deletion expected 200, got %d", recPostPre.Code)
	}

	recSearchPre := env.do(http.MethodGet, "/api/users?q=alice", bob.Token, nil)
	if recSearchPre.Code != http.StatusOK {
		t.Fatalf("GET /api/users?q=alice before deletion expected 200, got %d", recSearchPre.Code)
	}
	searchPre := decodeJSON[searchUsersResponse](t, recSearchPre)
	foundAlicePre := false
	for _, u := range searchPre.Items {
		if u.ID == alice.ID {
			foundAlicePre = true
			break
		}
	}
	if !foundAlicePre {
		t.Fatalf("expected alice to be found in search before deletion")
	}

	// Alice deletes her account
	delRec := env.do(http.MethodPost, "/api/me/account-deletion", alice.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("account deletion failed: %d", delRec.Code)
	}

	// After deletion:
	// 1. Bob requests Alice's profile -> 404 relationship_not_found
	recUserPost := env.do(http.MethodGet, "/api/users/"+alice.ID, bob.Token, nil)
	if recUserPost.Code != http.StatusNotFound {
		t.Fatalf("GET /api/users/:id after deletion expected 404, got %d: %s", recUserPost.Code, recUserPost.Body.String())
	}
	if errCode := authDecodeError(t, recUserPost); errCode != "relationship_not_found" {
		t.Fatalf("expected relationship_not_found, got %s", errCode)
	}

	// 2. Bob requests Alice's post -> 404 post_not_found
	recPostPost := env.do(http.MethodGet, "/api/posts/"+alicePostID, bob.Token, nil)
	if recPostPost.Code != http.StatusNotFound {
		t.Fatalf("GET /api/posts/:id after deletion expected 404, got %d: %s", recPostPost.Code, recPostPost.Body.String())
	}
	if errCode := authDecodeError(t, recPostPost); errCode != "post_not_found" {
		t.Fatalf("expected post_not_found, got %s", errCode)
	}

	// 3. Bob searches for alice -> not in search results
	recSearchPost := env.do(http.MethodGet, "/api/users?q=alice", bob.Token, nil)
	if recSearchPost.Code != http.StatusOK {
		t.Fatalf("search after deletion expected 200, got %d", recSearchPost.Code)
	}
	searchPost := decodeJSON[searchUsersResponse](t, recSearchPost)
	for _, u := range searchPost.Items {
		if u.ID == alice.ID {
			t.Fatalf("expected alice to be excluded from search after deletion")
		}
	}
}
