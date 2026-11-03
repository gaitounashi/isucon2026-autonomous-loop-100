package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type adminTestErrorResponse struct {
	Error string `json:"error"`
}

type adminTestLoginResponse struct {
	Token           string `json:"token"`
	AdministratorID string `json:"administratorId"`
}

type adminTestPostingWindowResponse struct {
	ID             string    `json:"id"`
	Region         string    `json:"region"`
	LocalDate      string    `json:"localDate"`
	StartsAt       time.Time `json:"startsAt"`
	OnTimeDeadline time.Time `json:"onTimeDeadline"`
}

type adminTestUserResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Bio         string `json:"bio"`
	Region      string `json:"region"`
	AccountType string `json:"accountType"`
	Status      string `json:"status"`
}

type adminTestRelationshipUserResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type adminTestSearchUsersResponse struct {
	Items   []adminTestRelationshipUserResponse `json:"items"`
	HasMore bool                                `json:"hasMore"`
}

func adminDoWithRawAuth(env *testEnv, method, path, authHeader string, body any) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	var contentType string

	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
			contentType = "application/json"
		case string:
			bodyReader = strings.NewReader(v)
			contentType = "application/json"
		case io.Reader:
			bodyReader = v
			contentType = "application/json"
		default:
			data, err := json.Marshal(body)
			if err != nil {
				env.T.Fatalf("failed to marshal request body: %v", err)
			}
			bodyReader = bytes.NewReader(data)
			contentType = "application/json"
		}
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func adminCreateAdminInDB(t *testing.T, env *testEnv, username, password string) string {
	t.Helper()
	adminID := uuid.NewString()
	hash := sha256.Sum256([]byte(password))
	passwordHash := hex.EncodeToString(hash[:])

	_, err := env.DB.Exec(
		`INSERT INTO administrators (id, username, password_hash) VALUES ($1, $2, $3)`,
		adminID, username, passwordHash,
	)
	if err != nil {
		t.Fatalf("failed to insert administrator: %v", err)
	}
	return adminID
}

func TestIntegrationAdminLogin_Success(t *testing.T) {
	env := newTestEnv(t)

	adminUsername := "admin_test_success"
	adminPassword := "secret_admin_pass"
	adminID := adminCreateAdminInDB(t, env, adminUsername, adminPassword)

	loginRec := env.do(http.MethodPost, "/api/admin/auth/login", "", map[string]string{
		"username": adminUsername,
		"password": adminPassword,
	})
	if loginRec.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/auth/login expected 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	loginResp := decodeJSON[adminTestLoginResponse](t, loginRec)
	if loginResp.Token == "" {
		t.Fatalf("expected non-empty admin token")
	}
	if loginResp.AdministratorID != adminID {
		t.Fatalf("expected administratorId %s, got %s", adminID, loginResp.AdministratorID)
	}

	// Verify session recorded in database
	tokenHash := hex.EncodeToString(sha256Hash([]byte(loginResp.Token)))
	var count int
	err := env.DB.Get(&count, `SELECT COUNT(*) FROM administrator_sessions WHERE administrator_id = $1 AND token_hash = $2 AND active = true`, adminID, tokenHash)
	if err != nil {
		t.Fatalf("failed to query administrator_sessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 active session in database, got %d", count)
	}
}

func TestIntegrationAdminLogin_Failures(t *testing.T) {
	env := newTestEnv(t)

	adminUsername := "admin_test_fail"
	adminPassword := "correct_password"
	adminCreateAdminInDB(t, env, adminUsername, adminPassword)

	// Case 1: Wrong password
	recWrongPass := env.do(http.MethodPost, "/api/admin/auth/login", "", map[string]string{
		"username": adminUsername,
		"password": "wrong_password",
	})
	if recWrongPass.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password expected 401, got %d: %s", recWrongPass.Code, recWrongPass.Body.String())
	}
	errResp := decodeJSON[adminTestErrorResponse](t, recWrongPass)
	if errResp.Error != "invalid_credentials" {
		t.Fatalf("expected error 'invalid_credentials', got %q", errResp.Error)
	}

	// Case 2: Non-existent admin username
	recNoUser := env.do(http.MethodPost, "/api/admin/auth/login", "", map[string]string{
		"username": "non_existent_admin",
		"password": "any_password",
	})
	if recNoUser.Code != http.StatusUnauthorized {
		t.Fatalf("non-existent admin expected 401, got %d: %s", recNoUser.Code, recNoUser.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recNoUser)
	if errResp.Error != "invalid_credentials" {
		t.Fatalf("expected error 'invalid_credentials', got %q", errResp.Error)
	}

	// Case 3: Invalid username format (spaces/symbols)
	recInvalidUser := env.do(http.MethodPost, "/api/admin/auth/login", "", map[string]string{
		"username": "invalid admin name!",
		"password": "any_password",
	})
	if recInvalidUser.Code != http.StatusUnauthorized {
		t.Fatalf("invalid username format expected 401, got %d: %s", recInvalidUser.Code, recInvalidUser.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recInvalidUser)
	if errResp.Error != "invalid_credentials" {
		t.Fatalf("expected error 'invalid_credentials', got %q", errResp.Error)
	}

	// Case 4: Malformed request payload
	reqMalformed := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", strings.NewReader("{invalid-json"))
	reqMalformed.Header.Set("Content-Type", "application/json")
	recMalformed := httptest.NewRecorder()
	env.E.ServeHTTP(recMalformed, reqMalformed)
	if recMalformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON expected 400, got %d: %s", recMalformed.Code, recMalformed.Body.String())
	}
}

func TestIntegrationAdminLogout(t *testing.T) {
	env := newTestEnv(t)

	token := env.adminToken()
	tokenHash := hex.EncodeToString(sha256Hash([]byte(token)))

	// Logout
	logoutRec := env.do(http.MethodPost, "/api/admin/auth/logout", token, nil)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/admin/auth/logout expected 204, got %d: %s", logoutRec.Code, logoutRec.Body.String())
	}

	// Check DB: session active is false and invalidated_at is set
	var active bool
	var invalidatedAt *time.Time
	err := env.DB.QueryRow(`SELECT active, invalidated_at FROM administrator_sessions WHERE token_hash = $1`, tokenHash).Scan(&active, &invalidatedAt)
	if err != nil {
		t.Fatalf("failed to query administrator_sessions: %v", err)
	}
	if active {
		t.Fatalf("expected active to be false after logout")
	}
	if invalidatedAt == nil {
		t.Fatalf("expected invalidated_at to be non-nil after logout")
	}

	// Logging out again with the same token must fail with 401
	recLogoutAgain := env.do(http.MethodPost, "/api/admin/auth/logout", token, nil)
	if recLogoutAgain.Code != http.StatusUnauthorized {
		t.Fatalf("second logout expected 401, got %d: %s", recLogoutAgain.Code, recLogoutAgain.Body.String())
	}
	errResp := decodeJSON[adminTestErrorResponse](t, recLogoutAgain)
	if errResp.Error != "invalid_administrator_session" {
		t.Fatalf("expected error 'invalid_administrator_session', got %q", errResp.Error)
	}

	// Other admin endpoint with the logged-out token must fail with 401
	recPostingWindow := env.do(http.MethodPost, "/api/admin/posting-window", token, map[string]any{
		"region":    "asia",
		"localDate": "2026-11-20",
	})
	if recPostingWindow.Code != http.StatusUnauthorized {
		t.Fatalf("posting window with invalidated token expected 401, got %d: %s", recPostingWindow.Code, recPostingWindow.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recPostingWindow)
	if errResp.Error != "invalid_administrator_session" {
		t.Fatalf("expected error 'invalid_administrator_session', got %q", errResp.Error)
	}
}

func TestIntegrationAdminEndpoints_AuthRequired(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("admin_auth_user")

	endpoints := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "logout",
			method: http.MethodPost,
			path:   "/api/admin/auth/logout",
			body:   nil,
		},
		{
			name:   "certify user",
			method: http.MethodPut,
			path:   "/api/admin/users/" + user.ID + "/certification",
			body:   nil,
		},
		{
			name:   "delete user certification",
			method: http.MethodDelete,
			path:   "/api/admin/users/" + user.ID + "/certification",
			body:   nil,
		},
		{
			name:   "open posting window",
			method: http.MethodPost,
			path:   "/api/admin/posting-window",
			body: map[string]any{
				"region":    "asia",
				"localDate": "2026-11-25",
			},
		},
	}

	for _, ep := range endpoints {
		// 1. Without token (no Authorization header)
		recNoToken := env.do(ep.method, ep.path, "", ep.body)
		if recNoToken.Code != http.StatusUnauthorized {
			t.Fatalf("[%s] expected 401 without token, got %d: %s", ep.name, recNoToken.Code, recNoToken.Body.String())
		}
		errResp := decodeJSON[adminTestErrorResponse](t, recNoToken)
		if errResp.Error != "invalid_administrator_session" {
			t.Fatalf("[%s] expected error 'invalid_administrator_session', got %q", ep.name, errResp.Error)
		}

		// 2. With regular user token
		recUserToken := env.do(ep.method, ep.path, user.Token, ep.body)
		if recUserToken.Code != http.StatusUnauthorized {
			t.Fatalf("[%s] expected 401 with user token, got %d: %s", ep.name, recUserToken.Code, recUserToken.Body.String())
		}
		errResp = decodeJSON[adminTestErrorResponse](t, recUserToken)
		if errResp.Error != "invalid_administrator_session" {
			t.Fatalf("[%s] expected error 'invalid_administrator_session', got %q", ep.name, errResp.Error)
		}

		// 3. With invalid random token
		recInvalidToken := env.do(ep.method, ep.path, "invalid-random-token", ep.body)
		if recInvalidToken.Code != http.StatusUnauthorized {
			t.Fatalf("[%s] expected 401 with invalid token, got %d: %s", ep.name, recInvalidToken.Code, recInvalidToken.Body.String())
		}
		errResp = decodeJSON[adminTestErrorResponse](t, recInvalidToken)
		if errResp.Error != "invalid_administrator_session" {
			t.Fatalf("[%s] expected error 'invalid_administrator_session', got %q", ep.name, errResp.Error)
		}

		// 4. With malformed Authorization header (no "Bearer " prefix)
		recMalformedAuth := adminDoWithRawAuth(env, ep.method, ep.path, "Basic some-credentials", ep.body)
		if recMalformedAuth.Code != http.StatusUnauthorized {
			t.Fatalf("[%s] expected 401 with malformed Authorization, got %d: %s", ep.name, recMalformedAuth.Code, recMalformedAuth.Body.String())
		}
		errResp = decodeJSON[adminTestErrorResponse](t, recMalformedAuth)
		if errResp.Error != "invalid_administrator_session" {
			t.Fatalf("[%s] expected error 'invalid_administrator_session', got %q", ep.name, errResp.Error)
		}
	}
}

func TestIntegrationAdminUserCertification_SuccessAndEffects(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	userA := env.registerUser("creator_alice")
	userB := env.registerUser("follower_bob")

	bioText := "Official Verified Creator Bio"
	updateBioRec := env.do(http.MethodPatch, "/api/me", userA.Token, map[string]string{
		"bio": bioText,
	})
	if updateBioRec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/me failed with status %d: %s", updateBioRec.Code, updateBioRec.Body.String())
	}

	// BEFORE certification:
	// 1. GET /api/users/:userId by userB: bio is hidden, accountType is regular
	getUserBefore := env.do(http.MethodGet, "/api/users/"+userA.ID, userB.Token, nil)
	if getUserBefore.Code != http.StatusOK {
		t.Fatalf("GET /api/users/:id before certify expected 200, got %d: %s", getUserBefore.Code, getUserBefore.Body.String())
	}
	uBefore := decodeJSON[adminTestRelationshipUserResponse](t, getUserBefore)
	if uBefore.AccountType != "regular" {
		t.Fatalf("expected accountType 'regular', got %q", uBefore.AccountType)
	}
	if uBefore.Bio != "" {
		t.Fatalf("expected bio to be hidden (''), got %q", uBefore.Bio)
	}

	// 2. GET /api/users?q=... by userB: bio is hidden, accountType is regular
	searchBefore := env.do(http.MethodGet, "/api/users?q="+userA.Username, userB.Token, nil)
	if searchBefore.Code != http.StatusOK {
		t.Fatalf("search users before certify expected 200, got %d: %s", searchBefore.Code, searchBefore.Body.String())
	}
	sBefore := decodeJSON[adminTestSearchUsersResponse](t, searchBefore)
	if len(sBefore.Items) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(sBefore.Items))
	}
	if sBefore.Items[0].AccountType != "regular" {
		t.Fatalf("expected search accountType 'regular', got %q", sBefore.Items[0].AccountType)
	}
	if sBefore.Items[0].Bio != "" {
		t.Fatalf("expected search bio to be hidden (''), got %q", sBefore.Items[0].Bio)
	}

	// 3. Follow userA by userB fails because userA is regular
	followBefore := env.do(http.MethodPut, "/api/follows/"+userA.ID, userB.Token, nil)
	if followBefore.Code != http.StatusConflict {
		t.Fatalf("follow regular user expected 409, got %d: %s", followBefore.Code, followBefore.Body.String())
	}
	followErr := decodeJSON[adminTestErrorResponse](t, followBefore)
	if followErr.Error != "relationship_conflict" {
		t.Fatalf("expected error 'relationship_conflict', got %q", followErr.Error)
	}

	// CERTIFY userA
	certRec := env.do(http.MethodPut, "/api/admin/users/"+userA.ID+"/certification", adminTok, nil)
	if certRec.Code != http.StatusNoContent {
		t.Fatalf("PUT /api/admin/users/:id/certification expected 204, got %d: %s", certRec.Code, certRec.Body.String())
	}

	// DB check
	var accountType string
	err := env.DB.Get(&accountType, `SELECT account_type FROM users WHERE id = $1`, userA.ID)
	if err != nil {
		t.Fatalf("failed to query users.account_type: %v", err)
	}
	if accountType != "certified_public" {
		t.Fatalf("expected db account_type 'certified_public', got %q", accountType)
	}

	// AFTER certification:
	// 1. GET /api/me by userA
	meRec := env.do(http.MethodGet, "/api/me", userA.Token, nil)
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/me after certify expected 200, got %d: %s", meRec.Code, meRec.Body.String())
	}
	meResp := decodeJSON[adminTestUserResponse](t, meRec)
	if meResp.AccountType != "certified_public" {
		t.Fatalf("expected me.AccountType 'certified_public', got %q", meResp.AccountType)
	}
	if meResp.Bio != bioText {
		t.Fatalf("expected me.Bio %q, got %q", bioText, meResp.Bio)
	}

	// 2. GET /api/users/:userId by userB: bio is now VISIBLE to non-friends
	getUserAfter := env.do(http.MethodGet, "/api/users/"+userA.ID, userB.Token, nil)
	if getUserAfter.Code != http.StatusOK {
		t.Fatalf("GET /api/users/:id after certify expected 200, got %d: %s", getUserAfter.Code, getUserAfter.Body.String())
	}
	uAfter := decodeJSON[adminTestRelationshipUserResponse](t, getUserAfter)
	if uAfter.AccountType != "certified_public" {
		t.Fatalf("expected accountType 'certified_public', got %q", uAfter.AccountType)
	}
	if uAfter.Bio != bioText {
		t.Fatalf("expected bio to be visible (%q), got %q", bioText, uAfter.Bio)
	}

	// 3. Search by userB: bio is visible and accountType is certified_public
	searchAfter := env.do(http.MethodGet, "/api/users?q="+userA.Username, userB.Token, nil)
	if searchAfter.Code != http.StatusOK {
		t.Fatalf("search users after certify expected 200, got %d: %s", searchAfter.Code, searchAfter.Body.String())
	}
	sAfter := decodeJSON[adminTestSearchUsersResponse](t, searchAfter)
	if len(sAfter.Items) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(sAfter.Items))
	}
	if sAfter.Items[0].AccountType != "certified_public" {
		t.Fatalf("expected search accountType 'certified_public', got %q", sAfter.Items[0].AccountType)
	}
	if sAfter.Items[0].Bio != bioText {
		t.Fatalf("expected search bio to be visible (%q), got %q", bioText, sAfter.Items[0].Bio)
	}

	// 4. Follow userA by userB now SUCCEEDS (204 No Content)
	followAfter := env.do(http.MethodPut, "/api/follows/"+userA.ID, userB.Token, nil)
	if followAfter.Code != http.StatusNoContent {
		t.Fatalf("follow certified user expected 204, got %d: %s", followAfter.Code, followAfter.Body.String())
	}
}

func TestIntegrationAdminUserCertification_Errors(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	user := env.registerUser("user_cert_err")

	// Certify once
	rec1 := env.do(http.MethodPut, "/api/admin/users/"+user.ID+"/certification", adminTok, nil)
	if rec1.Code != http.StatusNoContent {
		t.Fatalf("initial certify expected 204, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 1. Certify already certified user returns 409 conflict
	recDuplicate := env.do(http.MethodPut, "/api/admin/users/"+user.ID+"/certification", adminTok, nil)
	if recDuplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate certify expected 409, got %d: %s", recDuplicate.Code, recDuplicate.Body.String())
	}
	errResp := decodeJSON[adminTestErrorResponse](t, recDuplicate)
	if errResp.Error != "certification_conflict" {
		t.Fatalf("expected error 'certification_conflict', got %q", errResp.Error)
	}

	// 2. Certify non-existent user returns 409 conflict
	nonExistentID := uuid.NewString()
	recNonExistent := env.do(http.MethodPut, "/api/admin/users/"+nonExistentID+"/certification", adminTok, nil)
	if recNonExistent.Code != http.StatusConflict {
		t.Fatalf("certify non-existent user expected 409, got %d: %s", recNonExistent.Code, recNonExistent.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recNonExistent)
	if errResp.Error != "certification_conflict" {
		t.Fatalf("expected error 'certification_conflict', got %q", errResp.Error)
	}

	// 3. Certify inactive/deleted user returns 409 conflict
	userDeleted := env.registerUser("user_cert_deleted")
	_, err := env.DB.Exec(`UPDATE users SET status = 'deleted' WHERE id = $1`, userDeleted.ID)
	if err != nil {
		t.Fatalf("failed to update user status to deleted: %v", err)
	}
	recDeleted := env.do(http.MethodPut, "/api/admin/users/"+userDeleted.ID+"/certification", adminTok, nil)
	if recDeleted.Code != http.StatusConflict {
		t.Fatalf("certify deleted user expected 409, got %d: %s", recDeleted.Code, recDeleted.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recDeleted)
	if errResp.Error != "certification_conflict" {
		t.Fatalf("expected error 'certification_conflict', got %q", errResp.Error)
	}
}

func TestIntegrationAdminUserDeleteCertification(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	user := env.registerUser("user_uncertify_test")

	// Certify user
	certRec := env.do(http.MethodPut, "/api/admin/users/"+user.ID+"/certification", adminTok, nil)
	if certRec.Code != http.StatusNoContent {
		t.Fatalf("certify user expected 204, got %d: %s", certRec.Code, certRec.Body.String())
	}

	// Attempt to delete certification
	delRec := env.do(http.MethodDelete, "/api/admin/users/"+user.ID+"/certification", adminTok, nil)
	if delRec.Code != http.StatusConflict {
		t.Fatalf("DELETE certification expected 409, got %d: %s", delRec.Code, delRec.Body.String())
	}
	errResp := decodeJSON[adminTestErrorResponse](t, delRec)
	if errResp.Error != "certification_irreversible" {
		t.Fatalf("expected error 'certification_irreversible', got %q", errResp.Error)
	}

	// Verify account_type is STILL certified_public
	var accountType string
	err := env.DB.Get(&accountType, `SELECT account_type FROM users WHERE id = $1`, user.ID)
	if err != nil {
		t.Fatalf("failed to query users.account_type: %v", err)
	}
	if accountType != "certified_public" {
		t.Fatalf("expected account_type to remain 'certified_public', got %q", accountType)
	}

	// Attempting to delete certification for non-existent user also returns 409 certification_irreversible
	nonExistentID := uuid.NewString()
	delNonExistent := env.do(http.MethodDelete, "/api/admin/users/"+nonExistentID+"/certification", adminTok, nil)
	if delNonExistent.Code != http.StatusConflict {
		t.Fatalf("DELETE certification for non-existent user expected 409, got %d: %s", delNonExistent.Code, delNonExistent.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, delNonExistent)
	if errResp.Error != "certification_irreversible" {
		t.Fatalf("expected error 'certification_irreversible', got %q", errResp.Error)
	}
}

func TestIntegrationAdminPostingWindow_Success(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	userAsia := env.registerUserInRegion("pw_user_asia", "asia")
	userEurope := env.registerUserInRegion("pw_user_europe", "europe")

	customSeconds := int64(3600)
	dateStr := "2026-11-01"

	// 1. Open first posting window with custom onTimeSeconds
	beforeCall := time.Now().UTC().Add(-2 * time.Second)
	rec1 := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":        "asia",
		"localDate":     dateStr,
		"onTimeSeconds": customSeconds,
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("POST /api/admin/posting-window expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}
	resp1 := decodeJSON[adminTestPostingWindowResponse](t, rec1)

	if resp1.ID == "" {
		t.Fatalf("expected non-empty posting window id")
	}
	if resp1.Region != "asia" {
		t.Fatalf("expected region 'asia', got %q", resp1.Region)
	}
	if resp1.LocalDate != dateStr {
		t.Fatalf("expected localDate %q, got %q", dateStr, resp1.LocalDate)
	}
	if resp1.StartsAt.Before(beforeCall) {
		t.Fatalf("expected startsAt to be recent, got %v", resp1.StartsAt)
	}
	expectedDeadline1 := resp1.StartsAt.Add(time.Duration(customSeconds) * time.Second)
	if !resp1.OnTimeDeadline.Equal(expectedDeadline1) {
		t.Fatalf("expected onTimeDeadline %v, got %v", expectedDeadline1, resp1.OnTimeDeadline)
	}

	// Verify notification in DB: asia user gets notification, europe user does not
	var asiaNotifCount int
	err := env.DB.Get(&asiaNotifCount, `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND posting_window_id = $2`, userAsia.ID, resp1.ID)
	if err != nil {
		t.Fatalf("failed to query notifications: %v", err)
	}
	if asiaNotifCount != 1 {
		t.Fatalf("expected 1 notification for asia user, got %d", asiaNotifCount)
	}

	var europeNotifCount int
	err = env.DB.Get(&europeNotifCount, `SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND posting_window_id = $2`, userEurope.ID, resp1.ID)
	if err != nil {
		t.Fatalf("failed to query notifications: %v", err)
	}
	if europeNotifCount != 0 {
		t.Fatalf("expected 0 notifications for europe user, got %d", europeNotifCount)
	}

	// 2. Open second posting window in same region for a later date without onTimeSeconds (default 86400s)
	dateStr2 := "2026-11-02"
	rec2 := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":    "asia",
		"localDate": dateStr2,
	})
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second POST /api/admin/posting-window expected 201, got %d: %s", rec2.Code, rec2.Body.String())
	}
	resp2 := decodeJSON[adminTestPostingWindowResponse](t, rec2)

	defaultDeadline := resp2.StartsAt.Add(24 * time.Hour)
	if !resp2.OnTimeDeadline.Equal(defaultDeadline) {
		t.Fatalf("expected default onTimeDeadline %v, got %v", defaultDeadline, resp2.OnTimeDeadline)
	}

	// Check that first window's ended_at was set to second window's startsAt
	var endedAt *time.Time
	err = env.DB.Get(&endedAt, `SELECT ended_at FROM posting_windows WHERE id = $1`, resp1.ID)
	if err != nil {
		t.Fatalf("failed to query ended_at for window 1: %v", err)
	}
	if endedAt == nil || !endedAt.Equal(resp2.StartsAt) {
		t.Fatalf("expected window 1 ended_at to match window 2 startsAt %v, got %v", resp2.StartsAt, endedAt)
	}
}

func TestIntegrationAdminPostingWindow_Validation(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	// Region validation: invalid regions
	badRegions := []string{"invalid", "antarctica", "", "oceania", "ASIA"}
	for _, reg := range badRegions {
		rec := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
			"region":    reg,
			"localDate": "2026-11-10",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("region %q expected 400, got %d: %s", reg, rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[adminTestErrorResponse](t, rec)
		if errResp.Error != "invalid_posting_window" {
			t.Fatalf("region %q expected error 'invalid_posting_window', got %q", reg, errResp.Error)
		}
	}

	// Date format validation: invalid localDate strings
	badDates := []string{
		"2026/11/10",
		"10-11-2026",
		"2026-13-10",
		"2026-11-32",
		"not-a-date",
		"",
	}
	for _, d := range badDates {
		rec := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
			"region":    "asia",
			"localDate": d,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("localDate %q expected 400, got %d: %s", d, rec.Code, rec.Body.String())
		}
		errResp := decodeJSON[adminTestErrorResponse](t, rec)
		if errResp.Error != "invalid_posting_window" {
			t.Fatalf("localDate %q expected error 'invalid_posting_window', got %q", d, errResp.Error)
		}
	}

	// Malformed JSON payload
	recMalformed := adminDoWithRawAuth(env, http.MethodPost, "/api/admin/posting-window", "Bearer "+adminTok, "{not-json")
	if recMalformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON expected 400, got %d: %s", recMalformed.Code, recMalformed.Body.String())
	}
}

func TestIntegrationAdminPostingWindow_Conflict(t *testing.T) {
	env := newTestEnv(t)
	adminTok := env.adminToken()

	baseDate := "2026-11-15"
	recInitial := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":    "asia",
		"localDate": baseDate,
	})
	if recInitial.Code != http.StatusCreated {
		t.Fatalf("initial posting window expected 201, got %d: %s", recInitial.Code, recInitial.Body.String())
	}

	// 1. Exact duplicate date in same region -> 409 conflict
	recDuplicate := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":    "asia",
		"localDate": baseDate,
	})
	if recDuplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate localDate in same region expected 409, got %d: %s", recDuplicate.Code, recDuplicate.Body.String())
	}
	errResp := decodeJSON[adminTestErrorResponse](t, recDuplicate)
	if errResp.Error != "posting_window_conflict" {
		t.Fatalf("expected error 'posting_window_conflict', got %q", errResp.Error)
	}

	// 2. Earlier date in same region -> 409 conflict (since local_date >= $2 exists)
	recEarlier := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":    "asia",
		"localDate": "2026-11-14",
	})
	if recEarlier.Code != http.StatusConflict {
		t.Fatalf("earlier localDate in same region expected 409, got %d: %s", recEarlier.Code, recEarlier.Body.String())
	}
	errResp = decodeJSON[adminTestErrorResponse](t, recEarlier)
	if errResp.Error != "posting_window_conflict" {
		t.Fatalf("expected error 'posting_window_conflict', got %q", errResp.Error)
	}

	// 3. Same date in DIFFERENT region -> 201 Created (no conflict)
	recOtherRegion := env.do(http.MethodPost, "/api/admin/posting-window", adminTok, map[string]any{
		"region":    "europe",
		"localDate": baseDate,
	})
	if recOtherRegion.Code != http.StatusCreated {
		t.Fatalf("same localDate in different region expected 201, got %d: %s", recOtherRegion.Code, recOtherRegion.Body.String())
	}
}

func TestIntegrationAdminIPBan_Rejection(t *testing.T) {
	env := newTestEnv(t)

	// ip_ban.go reloads every 200ms via ticker in reloadLoop.
	// env.do uses RemoteAddr = "192.0.2.1:12345", which translates to client IP "192.0.2.1".

	// Ensure cleanup deletes any ban and waits until unbanned so other tests are unaffected.
	t.Cleanup(func() {
		_, _ = env.DB.Exec(`DELETE FROM ip_bans WHERE ip = '192.0.2.1'`)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			rec := env.do(http.MethodGet, "/api/me", "", nil)
			if rec.Code != http.StatusForbidden {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
	})

	// Verify initially 192.0.2.1 is NOT banned (calling without token returns 401, not 403)
	initRec := env.do(http.MethodGet, "/api/me", "", nil)
	if initRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected initial status 401 Unauthorized, got %d: %s", initRec.Code, initRec.Body.String())
	}

	// Insert ban for 192.0.2.1 into database
	_, err := env.DB.Exec(`INSERT INTO ip_bans (ip, banned_until) VALUES ('192.0.2.1', clock_timestamp() + interval '5 minutes')`)
	if err != nil {
		t.Fatalf("failed to insert into ip_bans: %v", err)
	}

	// Poll until the background reloadLoop (200ms interval) picks up the new ban
	banApplied := false
	pollDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(pollDeadline) {
		rec := env.do(http.MethodGet, "/api/me", "", nil)
		if rec.Code == http.StatusForbidden {
			errResp := decodeJSON[adminTestErrorResponse](t, rec)
			if errResp.Error == "ip_banned" {
				banApplied = true
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}

	if !banApplied {
		t.Fatalf("IP ban was not applied within 3 seconds")
	}

	// Test unbanning: delete from ip_bans and verify reloadLoop clears the ban
	_, err = env.DB.Exec(`DELETE FROM ip_bans WHERE ip = '192.0.2.1'`)
	if err != nil {
		t.Fatalf("failed to delete from ip_bans: %v", err)
	}

	unbanApplied := false
	unbanDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(unbanDeadline) {
		rec := env.do(http.MethodGet, "/api/me", "", nil)
		if rec.Code != http.StatusForbidden {
			unbanApplied = true
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected unbanned request to return 401, got %d: %s", rec.Code, rec.Body.String())
			}
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if !unbanApplied {
		t.Fatalf("IP ban was not cleared within 3 seconds")
	}
}

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}
