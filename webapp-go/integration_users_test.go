package isureal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func usersDoWithContentType(env *testEnv, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "192.0.2.1:12345"

	rec := httptest.NewRecorder()
	env.E.ServeHTTP(rec, req)
	return rec
}

func usersMakeJPEG(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x * 255) / max(width, 1)),
				G: uint8((y * 255) / max(height, 1)),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func usersAssertError(t *testing.T, rec *httptest.ResponseRecorder, expectedStatus int, expectedCode string) {
	t.Helper()
	if rec.Code != expectedStatus {
		t.Fatalf("expected status %d, got %d: %s", expectedStatus, rec.Code, rec.Body.String())
	}
	var errResp APIErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error body: %v, raw: %s", err, rec.Body.String())
	}
	if errResp.Error != expectedCode {
		t.Fatalf("expected error code %q, got %q", expectedCode, errResp.Error)
	}
}

func usersMakeFriendship(env *testEnv, u1, u2 testUser) {
	reqRec := env.do(http.MethodPost, "/api/friend-requests", u1.Token, map[string]string{
		"recipientId": u2.ID,
	})
	if reqRec.Code != http.StatusCreated {
		env.T.Fatalf("usersMakeFriendship create request failed: %d %s", reqRec.Code, reqRec.Body.String())
	}
	var reqResp struct {
		RelationID string `json:"relationId"`
		Status     string `json:"status"`
	}
	reqResp = decodeJSON[struct {
		RelationID string `json:"relationId"`
		Status     string `json:"status"`
	}](env.T, reqRec)

	if reqResp.Status != "accepted" {
		acceptRec := env.do(http.MethodPost, "/api/friend-requests/"+reqResp.RelationID+"/accept", u2.Token, nil)
		if acceptRec.Code != http.StatusOK {
			env.T.Fatalf("usersMakeFriendship accept request failed: %d %s", acceptRec.Code, acceptRec.Body.String())
		}
	}
}

func usersCertify(env *testEnv, userID string) {
	adminTok := env.adminToken()
	rec := env.do(http.MethodPut, "/api/admin/users/"+userID+"/certification", adminTok, nil)
	if rec.Code != http.StatusNoContent {
		env.T.Fatalf("usersCertify failed: %d %s", rec.Code, rec.Body.String())
	}
}

func usersSetBio(env *testEnv, u testUser, bio string) {
	rec := env.do(http.MethodPatch, "/api/me", u.Token, map[string]string{
		"bio": bio,
	})
	if rec.Code != http.StatusOK {
		env.T.Fatalf("usersSetBio failed: %d %s", rec.Code, rec.Body.String())
	}
}

// =========================================================================
// GET /api/users (Search)
// =========================================================================

func TestIntegrationUsers_Search_AuthAndRegion(t *testing.T) {
	env := newTestEnv(t)
	asiaUser := env.registerUser("users_search_asia")

	// 1. Unauthenticated (no token)
	recNoAuth := env.do(http.MethodGet, "/api/users?q=asia", "", nil)
	usersAssertError(t, recNoAuth, http.StatusUnauthorized, "invalid_session")

	// 2. Invalid token
	recBadAuth := env.do(http.MethodGet, "/api/users?q=asia", "bad-token-value", nil)
	usersAssertError(t, recBadAuth, http.StatusUnauthorized, "invalid_session")

	// 3. Region mismatch (europe user on default asia server)
	europeUser := env.registerUserInRegion("users_search_europe", "europe")
	recWrongRegion := env.do(http.MethodGet, "/api/users?q=asia", europeUser.Token, nil)
	usersAssertError(t, recWrongRegion, http.StatusForbidden, "region_mismatch")

	// 4. Correct region succeeds
	recOK := env.do(http.MethodGet, "/api/users?q=asia", asiaUser.Token, nil)
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recOK.Code, recOK.Body.String())
	}
}

func TestIntegrationUsers_Search_Validation(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("users_val_search")

	// 1. Missing q parameter
	recMissingQ := env.do(http.MethodGet, "/api/users", user.Token, nil)
	usersAssertError(t, recMissingQ, http.StatusBadRequest, "invalid_relationship")

	// 2. Empty q parameter
	recEmptyQ := env.do(http.MethodGet, "/api/users?q=", user.Token, nil)
	usersAssertError(t, recEmptyQ, http.StatusBadRequest, "invalid_relationship")

	// 3. q parameter too long (>100 runes)
	longQ := strings.Repeat("a", 101)
	recLongQ := env.do(http.MethodGet, "/api/users?q="+longQ, user.Token, nil)
	usersAssertError(t, recLongQ, http.StatusBadRequest, "invalid_relationship")

	// 4. Incomplete cursor: beforeUsername provided without beforeId
	recOnlyUser := env.do(http.MethodGet, "/api/users?q=test&beforeUsername=someuser", user.Token, nil)
	usersAssertError(t, recOnlyUser, http.StatusBadRequest, "invalid_pagination")

	// 5. Incomplete cursor: beforeId provided without beforeUsername
	recOnlyID := env.do(http.MethodGet, "/api/users?q=test&beforeId=someid", user.Token, nil)
	usersAssertError(t, recOnlyID, http.StatusBadRequest, "invalid_pagination")

	// 6. Both cursor fields present is accepted
	recValidCursor := env.do(http.MethodGet, "/api/users?q=test&beforeUsername=someuser&beforeId=someid", user.Token, nil)
	if recValidCursor.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recValidCursor.Code, recValidCursor.Body.String())
	}
}

func TestIntegrationUsers_Search_QueryNormalizationAndCaseInsensitivity(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_viewer_norm")

	// Register users with specific usernames and display names
	uAlice := env.registerUser("alice_norm")
	recPatchAlice := env.do(http.MethodPatch, "/api/me", uAlice.Token, map[string]string{
		"displayName": "Alice in Wonderland",
	})
	if recPatchAlice.Code != http.StatusOK {
		t.Fatalf("patch alice failed: %s", recPatchAlice.Body.String())
	}

	uBob := env.registerUser("bob_norm")
	recPatchBob := env.do(http.MethodPatch, "/api/me", uBob.Token, map[string]string{
		"displayName": "Bob Builder",
	})
	if recPatchBob.Code != http.StatusOK {
		t.Fatalf("patch bob failed: %s", recPatchBob.Body.String())
	}

	uCafe := env.registerUser("charlie_norm")
	recPatchCafe := env.do(http.MethodPatch, "/api/me", uCafe.Token, map[string]string{
		"displayName": "Café Manager",
	})
	if recPatchCafe.Code != http.StatusOK {
		t.Fatalf("patch cafe failed: %s", recPatchCafe.Body.String())
	}

	// 1. Uppercase query matching lowercase username
	rec1 := env.do(http.MethodGet, "/api/users?q=ALICE", viewer.Token, nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("search ALICE failed: %d: %s", rec1.Code, rec1.Body.String())
	}
	resp1 := decodeJSON[searchUsersResponse](t, rec1)
	if len(resp1.Items) != 1 || resp1.Items[0].ID != uAlice.ID {
		t.Fatalf("expected alice for query ALICE, got %+v", resp1.Items)
	}

	// 2. Lowercase query matching title-case display name
	rec2 := env.do(http.MethodGet, "/api/users?q=wonderland", viewer.Token, nil)
	resp2 := decodeJSON[searchUsersResponse](t, rec2)
	if len(resp2.Items) != 1 || resp2.Items[0].ID != uAlice.ID {
		t.Fatalf("expected alice for query wonderland, got %+v", resp2.Items)
	}

	// 3. Substring matching in display name
	rec3 := env.do(http.MethodGet, "/api/users?q=build", viewer.Token, nil)
	resp3 := decodeJSON[searchUsersResponse](t, rec3)
	if len(resp3.Items) != 1 || resp3.Items[0].ID != uBob.ID {
		t.Fatalf("expected bob for query build, got %+v", resp3.Items)
	}

	// 4. Unicode case folding / NFC normalization: query "CAFÉ" or "café" matches "Café Manager"
	rec4 := env.do(http.MethodGet, "/api/users?q="+url.QueryEscape("CAFÉ"), viewer.Token, nil)
	resp4 := decodeJSON[searchUsersResponse](t, rec4)
	if len(resp4.Items) != 1 || resp4.Items[0].ID != uCafe.ID {
		t.Fatalf("expected cafe for query CAFÉ, got %+v", resp4.Items)
	}

	// 5. Query matching nobody returns empty items slice
	rec5 := env.do(http.MethodGet, "/api/users?q=nonexistent_xyz", viewer.Token, nil)
	resp5 := decodeJSON[searchUsersResponse](t, rec5)
	if len(resp5.Items) != 0 || resp5.Items == nil || resp5.HasMore {
		t.Fatalf("expected empty non-nil items slice, got %+v", resp5)
	}
}

func TestIntegrationUsers_Search_ExclusionRules(t *testing.T) {
	env := newTestEnv(t)

	// Viewer
	viewer := env.registerUser("users_search_self")
	recPatchViewer := env.do(http.MethodPatch, "/api/me", viewer.Token, map[string]string{
		"displayName": "TargetSharedName",
	})
	if recPatchViewer.Code != http.StatusOK {
		t.Fatalf("patch viewer failed: %s", recPatchViewer.Body.String())
	}

	// Target 1: Active
	targetActive := env.registerUser("users_target_active")
	env.do(http.MethodPatch, "/api/me", targetActive.Token, map[string]string{"displayName": "TargetSharedName"})

	// Target 2: Deleted account
	targetDeleted := env.registerUser("users_target_deleted")
	env.do(http.MethodPatch, "/api/me", targetDeleted.Token, map[string]string{"displayName": "TargetSharedName"})
	delRec := env.do(http.MethodPost, "/api/me/account-deletion", targetDeleted.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("account deletion failed: %d %s", delRec.Code, delRec.Body.String())
	}

	// Target 3: Blocked by viewer
	targetBlockedByViewer := env.registerUser("users_target_blk1")
	env.do(http.MethodPatch, "/api/me", targetBlockedByViewer.Token, map[string]string{"displayName": "TargetSharedName"})
	recBlk1 := env.do(http.MethodPut, "/api/blocks/"+targetBlockedByViewer.ID, viewer.Token, nil)
	if recBlk1.Code != http.StatusNoContent {
		t.Fatalf("block target1 failed: %d %s", recBlk1.Code, recBlk1.Body.String())
	}

	// Target 4: Target blocked viewer
	targetBlockedViewer := env.registerUser("users_target_blk2")
	env.do(http.MethodPatch, "/api/me", targetBlockedViewer.Token, map[string]string{"displayName": "TargetSharedName"})
	recBlk2 := env.do(http.MethodPut, "/api/blocks/"+viewer.ID, targetBlockedViewer.Token, nil)
	if recBlk2.Code != http.StatusNoContent {
		t.Fatalf("block viewer failed: %d %s", recBlk2.Code, recBlk2.Body.String())
	}

	// Perform search: viewer searches for "TargetSharedName"
	searchRec := env.do(http.MethodGet, "/api/users?q=TargetSharedName", viewer.Token, nil)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search failed: %d %s", searchRec.Code, searchRec.Body.String())
	}
	res := decodeJSON[searchUsersResponse](t, searchRec)

	// Should contain ONLY targetActive:
	// - Excludes self (viewer)
	// - Excludes deleted target
	// - Excludes target blocked by viewer
	// - Excludes target who blocked viewer
	if len(res.Items) != 1 {
		t.Fatalf("expected exactly 1 item, got %d: %+v", len(res.Items), res.Items)
	}
	if res.Items[0].ID != targetActive.ID {
		t.Fatalf("expected active target ID %s, got %s", targetActive.ID, res.Items[0].ID)
	}

	// Unblock targetBlockedByViewer and verify it now appears in search
	unblkRec := env.do(http.MethodDelete, "/api/blocks/"+targetBlockedByViewer.ID, viewer.Token, nil)
	if unblkRec.Code != http.StatusNoContent {
		t.Fatalf("unblock failed: %d %s", unblkRec.Code, unblkRec.Body.String())
	}

	searchRec2 := env.do(http.MethodGet, "/api/users?q=TargetSharedName", viewer.Token, nil)
	res2 := decodeJSON[searchUsersResponse](t, searchRec2)
	if len(res2.Items) != 2 {
		t.Fatalf("expected 2 items after unblock, got %d: %+v", len(res2.Items), res2.Items)
	}
	found := false
	for _, it := range res2.Items {
		if it.ID == targetBlockedByViewer.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("unblocked user should appear in search results")
	}
}

func TestIntegrationUsers_Search_PaginationAndLimits(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_page_viewer")

	dummySecretKeyHash := hex.EncodeToString(make([]byte, sha256.Size))
	// Insert 52 users with a predictable prefix
	for i := 1; i <= 52; i++ {
		uid := uuid.NewString()
		username := fmt.Sprintf("users_batch_%02d", i)
		displayName := fmt.Sprintf("Batch User %02d", i)
		_, err := env.DB.Exec(`
			INSERT INTO users (id, username, secret_key_hash, display_name, region, status)
			VALUES ($1, $2, $3, $4, 'asia', 'active')
		`, uid, username, dummySecretKeyHash, displayName)
		if err != nil {
			t.Fatalf("failed to insert batch user %d: %v", i, err)
		}
	}

	// Page 1: Query for "users_batch_"
	page1Rec := env.do(http.MethodGet, "/api/users?q=users_batch_", viewer.Token, nil)
	if page1Rec.Code != http.StatusOK {
		t.Fatalf("page 1 failed: %d %s", page1Rec.Code, page1Rec.Body.String())
	}
	page1 := decodeJSON[searchUsersResponse](t, page1Rec)
	if len(page1.Items) != 50 {
		t.Fatalf("expected exactly 50 items on page 1, got %d", len(page1.Items))
	}
	if !page1.HasMore {
		t.Fatalf("expected hasMore=true on page 1")
	}

	// Verify sorting is ascending by (username, id)
	for i := 0; i < len(page1.Items)-1; i++ {
		if page1.Items[i].Username > page1.Items[i+1].Username {
			t.Fatalf("items not sorted: %s > %s", page1.Items[i].Username, page1.Items[i+1].Username)
		}
	}
	if page1.Items[0].Username != "users_batch_01" {
		t.Fatalf("expected first item users_batch_01, got %s", page1.Items[0].Username)
	}
	if page1.Items[49].Username != "users_batch_50" {
		t.Fatalf("expected 50th item users_batch_50, got %s", page1.Items[49].Username)
	}

	// Page 2: pass beforeUsername and beforeId from last item of page 1
	lastItem := page1.Items[49]
	cursorURL := fmt.Sprintf("/api/users?q=users_batch_&beforeUsername=%s&beforeId=%s", url.QueryEscape(lastItem.Username), url.QueryEscape(lastItem.ID))
	page2Rec := env.do(http.MethodGet, cursorURL, viewer.Token, nil)
	if page2Rec.Code != http.StatusOK {
		t.Fatalf("page 2 failed: %d %s", page2Rec.Code, page2Rec.Body.String())
	}
	page2 := decodeJSON[searchUsersResponse](t, page2Rec)
	if len(page2.Items) != 2 {
		t.Fatalf("expected exactly 2 items on page 2, got %d", len(page2.Items))
	}
	if page2.HasMore {
		t.Fatalf("expected hasMore=false on page 2")
	}
	if page2.Items[0].Username != "users_batch_51" || page2.Items[1].Username != "users_batch_52" {
		t.Fatalf("unexpected items on page 2: %s, %s", page2.Items[0].Username, page2.Items[1].Username)
	}
}

func TestIntegrationUsers_Search_ResponseFieldsAndBioVisibility(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_fields_viewer")

	// Regular user with bio
	regUser := env.registerUser("users_reg_target")
	usersSetBio(env, regUser, "Secret regular bio")

	// Certified public user with bio and profile image
	certUser := env.registerUser("users_cert_target")
	usersSetBio(env, certUser, "Public certified bio")
	usersCertify(env, certUser.ID)

	putRec := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", certUser.Token, "image/jpeg", tinyJPEG())
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("upload profile image failed: %d %s", putRec.Code, putRec.Body.String())
	}

	searchRec := env.do(http.MethodGet, "/api/users?q=users_", viewer.Token, nil)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search failed: %d %s", searchRec.Code, searchRec.Body.String())
	}
	res := decodeJSON[searchUsersResponse](t, searchRec)

	var foundReg, foundCert *relationshipUserResponse
	for i := range res.Items {
		if res.Items[i].ID == regUser.ID {
			foundReg = &res.Items[i]
		}
		if res.Items[i].ID == certUser.ID {
			foundCert = &res.Items[i]
		}
	}
	if foundReg == nil || foundCert == nil {
		t.Fatalf("expected to find both reg and cert users in search results")
	}

	// Regular user in search: Bio must be empty string
	if foundReg.Bio != "" {
		t.Fatalf("expected regular user bio to be empty in search, got %q", foundReg.Bio)
	}
	if foundReg.AccountType != "regular" {
		t.Fatalf("expected accountType regular, got %q", foundReg.AccountType)
	}
	if foundReg.ProfileImageURL != nil {
		t.Fatalf("expected nil profileImageUrl for reg user, got %v", foundReg.ProfileImageURL)
	}

	// Certified user in search: Bio is preserved, profileImageUrl is signed
	if foundCert.Bio != "Public certified bio" {
		t.Fatalf("expected certified user bio preserved, got %q", foundCert.Bio)
	}
	if foundCert.AccountType != "certified_public" {
		t.Fatalf("expected accountType certified_public, got %q", foundCert.AccountType)
	}
	if foundCert.ProfileImageURL == nil || !strings.HasPrefix(*foundCert.ProfileImageURL, "/api/images/") {
		t.Fatalf("expected signed profileImageUrl for cert user, got %v", foundCert.ProfileImageURL)
	}
}

// =========================================================================
// GET /api/users/:userId
// =========================================================================

func TestIntegrationUsers_GetUser_AuthAndRegion(t *testing.T) {
	env := newTestEnv(t)
	target := env.registerUser("users_target_single")

	// 1. Unauthenticated
	recNoAuth := env.do(http.MethodGet, "/api/users/"+target.ID, "", nil)
	usersAssertError(t, recNoAuth, http.StatusUnauthorized, "invalid_session")

	// 2. Bad token
	recBadAuth := env.do(http.MethodGet, "/api/users/"+target.ID, "bad-token", nil)
	usersAssertError(t, recBadAuth, http.StatusUnauthorized, "invalid_session")

	// 3. Region mismatch
	europeViewer := env.registerUserInRegion("users_europe_single", "europe")
	recWrongRegion := env.do(http.MethodGet, "/api/users/"+target.ID, europeViewer.Token, nil)
	usersAssertError(t, recWrongRegion, http.StatusForbidden, "region_mismatch")

	// 4. Correct region succeeds
	asiaViewer := env.registerUser("users_asia_single")
	recOK := env.do(http.MethodGet, "/api/users/"+target.ID, asiaViewer.Token, nil)
	if recOK.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recOK.Code, recOK.Body.String())
	}
}

func TestIntegrationUsers_GetUser_NotFound(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_get_nf_viewer")

	// 1. Non-existent user UUID
	recNonExistent := env.do(http.MethodGet, "/api/users/"+uuid.NewString(), viewer.Token, nil)
	usersAssertError(t, recNonExistent, http.StatusNotFound, "relationship_not_found")

	// 2. Deleted user
	deletedUser := env.registerUser("users_get_nf_del")
	delRec := env.do(http.MethodPost, "/api/me/account-deletion", deletedUser.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("account deletion failed: %d %s", delRec.Code, delRec.Body.String())
	}
	recDeleted := env.do(http.MethodGet, "/api/users/"+deletedUser.ID, viewer.Token, nil)
	usersAssertError(t, recDeleted, http.StatusNotFound, "relationship_not_found")

	// 3. User blocked by viewer -> 404
	blockedTarget := env.registerUser("users_get_nf_blk1")
	env.do(http.MethodPut, "/api/blocks/"+blockedTarget.ID, viewer.Token, nil)
	recBlockedByViewer := env.do(http.MethodGet, "/api/users/"+blockedTarget.ID, viewer.Token, nil)
	usersAssertError(t, recBlockedByViewer, http.StatusNotFound, "relationship_not_found")

	// 4. Target blocked viewer -> 404
	targetBlockingViewer := env.registerUser("users_get_nf_blk2")
	env.do(http.MethodPut, "/api/blocks/"+viewer.ID, targetBlockingViewer.Token, nil)
	recBlockedViewer := env.do(http.MethodGet, "/api/users/"+targetBlockingViewer.ID, viewer.Token, nil)
	usersAssertError(t, recBlockedViewer, http.StatusNotFound, "relationship_not_found")
}

func TestIntegrationUsers_GetUser_BioVisibilityAndFriendship(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_bio_viewer")

	// Target regular user with a bio
	regTarget := env.registerUser("users_bio_reg")
	usersSetBio(env, regTarget, "Target private bio")

	// 1. Not friends: bio is masked to empty string
	recNotFriend := env.do(http.MethodGet, "/api/users/"+regTarget.ID, viewer.Token, nil)
	if recNotFriend.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recNotFriend.Code, recNotFriend.Body.String())
	}
	uRespNotFriend := decodeJSON[getUserResponse](t, recNotFriend)
	if uRespNotFriend.Bio != "" {
		t.Fatalf("expected empty bio for non-friend, got %q", uRespNotFriend.Bio)
	}

	// 2. Become friends: bio is now visible
	usersMakeFriendship(env, viewer, regTarget)
	recFriend := env.do(http.MethodGet, "/api/users/"+regTarget.ID, viewer.Token, nil)
	if recFriend.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recFriend.Code, recFriend.Body.String())
	}
	uRespFriend := decodeJSON[getUserResponse](t, recFriend)
	if uRespFriend.Bio != "Target private bio" {
		t.Fatalf("expected bio visible for friend, got %q", uRespFriend.Bio)
	}

	// 3. Remove friendship: bio masked again
	delFriendRec := env.do(http.MethodDelete, "/api/friendships/"+regTarget.ID, viewer.Token, nil)
	if delFriendRec.Code != http.StatusNoContent {
		t.Fatalf("delete friendship failed: %d %s", delFriendRec.Code, delFriendRec.Body.String())
	}
	recUnfriended := env.do(http.MethodGet, "/api/users/"+regTarget.ID, viewer.Token, nil)
	uRespUnfriended := decodeJSON[getUserResponse](t, recUnfriended)
	if uRespUnfriended.Bio != "" {
		t.Fatalf("expected empty bio after friendship deleted, got %q", uRespUnfriended.Bio)
	}

	// 4. Certified public user: bio visible even if not friends
	certTarget := env.registerUser("users_bio_cert")
	usersSetBio(env, certTarget, "Certified public bio")
	usersCertify(env, certTarget.ID)
	recCert := env.do(http.MethodGet, "/api/users/"+certTarget.ID, viewer.Token, nil)
	uRespCert := decodeJSON[getUserResponse](t, recCert)
	if uRespCert.Bio != "Certified public bio" {
		t.Fatalf("expected certified user bio visible without friendship, got %q", uRespCert.Bio)
	}

	// 5. Regular user viewing their own profile via GET /api/users/:userId
	// Under current handler logic, user is not friends with self, so bio is masked to empty.
	usersSetBio(env, viewer, "Viewer own bio")
	recSelf := env.do(http.MethodGet, "/api/users/"+viewer.ID, viewer.Token, nil)
	if recSelf.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recSelf.Code, recSelf.Body.String())
	}
	uRespSelf := decodeJSON[getUserResponse](t, recSelf)
	if uRespSelf.Bio != "" {
		t.Fatalf("expected regular user bio masked when viewing self via /api/users/:userId, got %q", uRespSelf.Bio)
	}
}

func TestIntegrationUsers_GetUser_FieldsAndProfileImage(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("users_fields_viewer2")

	// Target without profile image
	targetNoImg := env.registerUser("users_no_img")
	recNoImg := env.do(http.MethodGet, "/api/users/"+targetNoImg.ID, viewer.Token, nil)
	if recNoImg.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recNoImg.Code, recNoImg.Body.String())
	}
	respNoImg := decodeJSON[getUserResponse](t, recNoImg)
	if respNoImg.ID != targetNoImg.ID || respNoImg.Username != targetNoImg.Username {
		t.Fatalf("mismatched user fields: %+v", respNoImg)
	}
	if respNoImg.ProfileImageURL != nil {
		t.Fatalf("expected nil profileImageUrl, got %v", respNoImg.ProfileImageURL)
	}

	// Target with uploaded profile image
	targetWithImg := env.registerUser("users_with_img")
	putRec := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", targetWithImg.Token, "image/jpeg", tinyJPEG())
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("upload profile image failed: %d %s", putRec.Code, putRec.Body.String())
	}

	recWithImg := env.do(http.MethodGet, "/api/users/"+targetWithImg.ID, viewer.Token, nil)
	respWithImg := decodeJSON[getUserResponse](t, recWithImg)
	if respWithImg.ProfileImageURL == nil || !strings.HasPrefix(*respWithImg.ProfileImageURL, "/api/images/") {
		t.Fatalf("expected signed profileImageUrl, got %v", respWithImg.ProfileImageURL)
	}

	// Fetch image using returned signed URL
	imgRec := env.do(http.MethodGet, *respWithImg.ProfileImageURL, "", nil)
	if imgRec.Code != http.StatusOK {
		t.Fatalf("fetch profile image URL expected 200, got %d: %s", imgRec.Code, imgRec.Body.String())
	}
	if imgRec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("expected image/jpeg Content-Type, got %q", imgRec.Header().Get("Content-Type"))
	}
}

// =========================================================================
// PUT & DELETE /api/me/profile-image
// =========================================================================

func TestIntegrationUsers_ProfileImage_Auth(t *testing.T) {
	env := newTestEnv(t)

	// PUT without token
	recPutNoAuth := env.do(http.MethodPut, "/api/me/profile-image", "", tinyJPEG())
	usersAssertError(t, recPutNoAuth, http.StatusUnauthorized, "invalid_session")

	// PUT with invalid token
	recPutBadAuth := env.do(http.MethodPut, "/api/me/profile-image", "invalid-token", tinyJPEG())
	usersAssertError(t, recPutBadAuth, http.StatusUnauthorized, "invalid_session")

	// DELETE without token
	recDelNoAuth := env.do(http.MethodDelete, "/api/me/profile-image", "", nil)
	usersAssertError(t, recDelNoAuth, http.StatusUnauthorized, "invalid_session")

	// DELETE with invalid token
	recDelBadAuth := env.do(http.MethodDelete, "/api/me/profile-image", "invalid-token", nil)
	usersAssertError(t, recDelBadAuth, http.StatusUnauthorized, "invalid_session")
}

func TestIntegrationUsers_ProfileImage_PutValidation(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("users_img_val")

	// 1. Missing Content-Type (e.g. calling env.do directly with []byte leaves Content-Type empty)
	recNoCT := env.do(http.MethodPut, "/api/me/profile-image", user.Token, tinyJPEG())
	usersAssertError(t, recNoCT, http.StatusUnsupportedMediaType, "unsupported_media_type")

	// 2. Unsupported Content-Type: image/png
	recPNG := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/png", tinyPNG())
	usersAssertError(t, recPNG, http.StatusUnsupportedMediaType, "unsupported_media_type")

	// 3. Unsupported Content-Type: application/json
	recJSON := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "application/json", []byte(`{}`))
	usersAssertError(t, recJSON, http.StatusUnsupportedMediaType, "unsupported_media_type")

	// 4. Content-Type image/jpeg but corrupted body bytes
	recCorrupt := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", []byte("not-an-image"))
	usersAssertError(t, recCorrupt, http.StatusBadRequest, "invalid_profile_image")

	// 5. Content-Type image/jpeg with valid PNG payload (jpeg decode fails)
	recFakeJPEG := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", tinyPNG())
	usersAssertError(t, recFakeJPEG, http.StatusBadRequest, "invalid_profile_image")

	// 6. Image dimension exceeding 512 width: 513x100
	wideJPEG := usersMakeJPEG(513, 100)
	recWide := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", wideJPEG)
	usersAssertError(t, recWide, http.StatusBadRequest, "invalid_profile_image")

	// 7. Image dimension exceeding 512 height: 100x513
	tallJPEG := usersMakeJPEG(100, 513)
	recTall := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", tallJPEG)
	usersAssertError(t, recTall, http.StatusBadRequest, "invalid_profile_image")
}

func TestIntegrationUsers_ProfileImage_PutSuccessAndEffect(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("users_img_success")

	// Initial GET /api/me has no profile image
	meRec1 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	me1 := decodeJSON[getMeResponse](t, meRec1)
	if me1.ProfileImageURL != nil {
		t.Fatalf("expected initial profileImageUrl to be nil, got %v", me1.ProfileImageURL)
	}

	// Upload valid 1x1 JPEG
	putRec1 := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", tinyJPEG())
	if putRec1.Code != http.StatusNoContent {
		t.Fatalf("PUT /api/me/profile-image expected 204, got %d: %s", putRec1.Code, putRec1.Body.String())
	}

	// Subsequent GET /api/me returns signed profileImageUrl
	meRec2 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	me2 := decodeJSON[getMeResponse](t, meRec2)
	if me2.ProfileImageURL == nil || !strings.HasPrefix(*me2.ProfileImageURL, "/api/images/") {
		t.Fatalf("expected signed profileImageUrl, got %v", me2.ProfileImageURL)
	}

	// Fetch image from URL: returns 200 with image/jpeg
	imgRec1 := env.do(http.MethodGet, *me2.ProfileImageURL, "", nil)
	if imgRec1.Code != http.StatusOK {
		t.Fatalf("fetch image expected 200, got %d: %s", imgRec1.Code, imgRec1.Body.String())
	}
	if imgRec1.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("expected Content-Type image/jpeg, got %q", imgRec1.Header().Get("Content-Type"))
	}

	// Upload max allowed dimensions 512x512 JPEG (image replacement)
	maxJPEG := usersMakeJPEG(512, 512)
	putRec2 := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", maxJPEG)
	if putRec2.Code != http.StatusNoContent {
		t.Fatalf("PUT 512x512 JPEG expected 204, got %d: %s", putRec2.Code, putRec2.Body.String())
	}

	// GET /api/me reflects new image URL
	meRec3 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	me3 := decodeJSON[getMeResponse](t, meRec3)
	if me3.ProfileImageURL == nil || *me3.ProfileImageURL == *me2.ProfileImageURL {
		t.Fatalf("expected new profile image URL after replacement, got %v", me3.ProfileImageURL)
	}

	// Fetch new image
	imgRec2 := env.do(http.MethodGet, *me3.ProfileImageURL, "", nil)
	if imgRec2.Code != http.StatusOK {
		t.Fatalf("fetch replaced image expected 200, got %d: %s", imgRec2.Code, imgRec2.Body.String())
	}
	newImg, err := jpeg.Decode(bytes.NewReader(imgRec2.Body.Bytes()))
	if err != nil {
		t.Fatalf("failed to decode replaced image: %v", err)
	}
	if newImg.Bounds().Dx() != 512 || newImg.Bounds().Dy() != 512 {
		t.Fatalf("expected replaced image bounds 512x512, got %dx%d", newImg.Bounds().Dx(), newImg.Bounds().Dy())
	}
}

func TestIntegrationUsers_ProfileImage_Delete(t *testing.T) {
	env := newTestEnv(t)
	user := env.registerUser("users_img_del")
	viewer := env.registerUser("users_img_viewer")

	// 1. DELETE when user has NO profile image returns 404 account_not_found
	delRec1 := env.do(http.MethodDelete, "/api/me/profile-image", user.Token, nil)
	usersAssertError(t, delRec1, http.StatusNotFound, "account_not_found")

	// 2. Upload profile image
	putRec := usersDoWithContentType(env, http.MethodPut, "/api/me/profile-image", user.Token, "image/jpeg", tinyJPEG())
	if putRec.Code != http.StatusNoContent {
		t.Fatalf("PUT profile-image failed: %d %s", putRec.Code, putRec.Body.String())
	}

	// Verify profile image is present on GET /api/me and GET /api/users/:userId
	meRec1 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	me1 := decodeJSON[getMeResponse](t, meRec1)
	if me1.ProfileImageURL == nil {
		t.Fatalf("expected non-nil profileImageUrl")
	}

	userRec1 := env.do(http.MethodGet, "/api/users/"+user.ID, viewer.Token, nil)
	uResp1 := decodeJSON[getUserResponse](t, userRec1)
	if uResp1.ProfileImageURL == nil {
		t.Fatalf("expected non-nil profileImageUrl in GET /api/users/:userId")
	}

	// 3. DELETE profile image returns 204 No Content
	delRec2 := env.do(http.MethodDelete, "/api/me/profile-image", user.Token, nil)
	if delRec2.Code != http.StatusNoContent {
		t.Fatalf("DELETE profile-image expected 204, got %d: %s", delRec2.Code, delRec2.Body.String())
	}

	// 4. GET /api/me and GET /api/users/:userId show profileImageUrl is nil
	meRec2 := env.do(http.MethodGet, "/api/me", user.Token, nil)
	me2 := decodeJSON[getMeResponse](t, meRec2)
	if me2.ProfileImageURL != nil {
		t.Fatalf("expected profileImageUrl to be nil after deletion, got %v", me2.ProfileImageURL)
	}

	userRec2 := env.do(http.MethodGet, "/api/users/"+user.ID, viewer.Token, nil)
	uResp2 := decodeJSON[getUserResponse](t, userRec2)
	if uResp2.ProfileImageURL != nil {
		t.Fatalf("expected profileImageUrl to be nil in GET /api/users/:userId after deletion, got %v", uResp2.ProfileImageURL)
	}

	// 5. Subsequent DELETE returns 404 account_not_found
	delRec3 := env.do(http.MethodDelete, "/api/me/profile-image", user.Token, nil)
	usersAssertError(t, delRec3, http.StatusNotFound, "account_not_found")
}
