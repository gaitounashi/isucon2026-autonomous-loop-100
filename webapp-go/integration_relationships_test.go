package isureal

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

type relErrorResponse struct {
	Error string `json:"error"`
}

type relFriendRequestResponse struct {
	RelationID string    `json:"relationId"`
	Status     string    `json:"status"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type relUserResponse struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	DisplayName     string  `json:"displayName"`
	ProfileImageURL *string `json:"profileImageUrl"`
	Bio             string  `json:"bio"`
	AccountType     string  `json:"accountType"`
}

type relItemResponse struct {
	RelationID string          `json:"relationId"`
	User       relUserResponse `json:"user"`
	Status     string          `json:"status"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type relListResponse struct {
	Items   []relItemResponse `json:"items"`
	HasMore bool              `json:"hasMore"`
}

func relCertifyUser(t *testing.T, env *testEnv, userID string) {
	t.Helper()
	token := env.adminToken()
	rec := env.do(http.MethodPut, "/api/admin/users/"+userID+"/certification", token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("failed to certify user %s: code %d, body %s", userID, rec.Code, rec.Body.String())
	}
}

func relAssertError(t *testing.T, rec *httptest.ResponseRecorder, expectedStatus int, expectedCode string) {
	t.Helper()
	if rec.Code != expectedStatus {
		t.Fatalf("expected status %d, got %d: %s", expectedStatus, rec.Code, rec.Body.String())
	}
	errResp := decodeJSON[relErrorResponse](t, rec)
	if errResp.Error != expectedCode {
		t.Fatalf("expected error code %q, got %q", expectedCode, errResp.Error)
	}
}

// ----------------------------------------------------------------------------
// Friend Request API Tests
// ----------------------------------------------------------------------------

func TestIntegrationRel_FriendRequest_CreateAndAccept(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_ca_a")
	userB := env.registerUser("rel_fr_ca_b")

	// 1. User A sends a friend request to User B
	createRec := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/friend-requests expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	createResp := decodeJSON[relFriendRequestResponse](t, createRec)
	if createResp.RelationID == "" {
		t.Fatalf("expected non-empty relationId")
	}
	if createResp.Status != "pending" {
		t.Fatalf("expected status 'pending', got %q", createResp.Status)
	}
	if createResp.UpdatedAt.IsZero() {
		t.Fatalf("expected non-zero updatedAt")
	}

	// 2. User B sees it in incoming-requests
	inRec := env.do(http.MethodGet, "/api/relationships/incoming-requests", userB.Token, nil)
	if inRec.Code != http.StatusOK {
		t.Fatalf("GET /api/relationships/incoming-requests expected 200, got %d: %s", inRec.Code, inRec.Body.String())
	}
	inList := decodeJSON[relListResponse](t, inRec)
	if len(inList.Items) != 1 {
		t.Fatalf("expected 1 incoming request, got %d", len(inList.Items))
	}
	if inList.Items[0].RelationID != createResp.RelationID {
		t.Fatalf("expected relationId %s, got %s", createResp.RelationID, inList.Items[0].RelationID)
	}
	if inList.Items[0].User.ID != userA.ID {
		t.Fatalf("expected incoming requester ID %s, got %s", userA.ID, inList.Items[0].User.ID)
	}
	if inList.Items[0].Status != "pending" {
		t.Fatalf("expected item status 'pending', got %q", inList.Items[0].Status)
	}

	// 3. User A sees it in outgoing-requests
	outRec := env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil)
	if outRec.Code != http.StatusOK {
		t.Fatalf("GET /api/relationships/outgoing-requests expected 200, got %d: %s", outRec.Code, outRec.Body.String())
	}
	outList := decodeJSON[relListResponse](t, outRec)
	if len(outList.Items) != 1 {
		t.Fatalf("expected 1 outgoing request, got %d", len(outList.Items))
	}
	if outList.Items[0].RelationID != createResp.RelationID {
		t.Fatalf("expected relationId %s, got %s", createResp.RelationID, outList.Items[0].RelationID)
	}
	if outList.Items[0].User.ID != userB.ID {
		t.Fatalf("expected outgoing recipient ID %s, got %s", userB.ID, outList.Items[0].User.ID)
	}
	if outList.Items[0].Status != "pending" {
		t.Fatalf("expected item status 'pending', got %q", outList.Items[0].Status)
	}

	// 4. User B accepts the request
	acceptRec := env.do(http.MethodPost, "/api/friend-requests/"+createResp.RelationID+"/accept", userB.Token, nil)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("POST accept expected 200, got %d: %s", acceptRec.Code, acceptRec.Body.String())
	}
	acceptResp := decodeJSON[relFriendRequestResponse](t, acceptRec)
	if acceptResp.RelationID != createResp.RelationID {
		t.Fatalf("expected relationId %s, got %s", createResp.RelationID, acceptResp.RelationID)
	}
	if acceptResp.Status != "accepted" {
		t.Fatalf("expected status 'accepted', got %q", acceptResp.Status)
	}

	// 5. Both users now see each other in friends list
	friendsRecA := env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil)
	if friendsRecA.Code != http.StatusOK {
		t.Fatalf("GET friends A expected 200, got %d: %s", friendsRecA.Code, friendsRecA.Body.String())
	}
	friendsA := decodeJSON[relListResponse](t, friendsRecA)
	if len(friendsA.Items) != 1 || friendsA.Items[0].User.ID != userB.ID {
		t.Fatalf("user A friends expected user B (%s), got %+v", userB.ID, friendsA.Items)
	}
	if friendsA.Items[0].Status != "friends" {
		t.Fatalf("expected friend status 'friends', got %q", friendsA.Items[0].Status)
	}

	friendsRecB := env.do(http.MethodGet, "/api/relationships/friends", userB.Token, nil)
	if friendsRecB.Code != http.StatusOK {
		t.Fatalf("GET friends B expected 200, got %d: %s", friendsRecB.Code, friendsRecB.Body.String())
	}
	friendsB := decodeJSON[relListResponse](t, friendsRecB)
	if len(friendsB.Items) != 1 || friendsB.Items[0].User.ID != userA.ID {
		t.Fatalf("user B friends expected user A (%s), got %+v", userA.ID, friendsB.Items)
	}

	// 6. Incoming and outgoing requests are now empty
	inRecAfter := env.do(http.MethodGet, "/api/relationships/incoming-requests", userB.Token, nil)
	if len(decodeJSON[relListResponse](t, inRecAfter).Items) != 0 {
		t.Fatalf("expected 0 incoming requests after accept")
	}
	outRecAfter := env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil)
	if len(decodeJSON[relListResponse](t, outRecAfter).Items) != 0 {
		t.Fatalf("expected 0 outgoing requests after accept")
	}
}

func TestIntegrationRel_FriendRequest_MutualAutoAccept(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_auto_a")
	userB := env.registerUser("rel_fr_auto_b")

	// User A creates friend request to B (status: pending)
	rec1 := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	if rec1.Code != http.StatusCreated {
		t.Fatalf("POST friend-request 1 expected 201, got %d: %s", rec1.Code, rec1.Body.String())
	}
	resp1 := decodeJSON[relFriendRequestResponse](t, rec1)
	if resp1.Status != "pending" {
		t.Fatalf("expected status 'pending', got %q", resp1.Status)
	}

	// User B sends friend request back to A while A's request is pending -> auto-accept
	rec2 := env.do(http.MethodPost, "/api/friend-requests", userB.Token, map[string]string{
		"recipientId": userA.ID,
	})
	if rec2.Code != http.StatusCreated {
		t.Fatalf("POST friend-request 2 expected 201, got %d: %s", rec2.Code, rec2.Body.String())
	}
	resp2 := decodeJSON[relFriendRequestResponse](t, rec2)
	if resp2.RelationID != resp1.RelationID {
		t.Fatalf("expected same relationId %s, got %s", resp1.RelationID, resp2.RelationID)
	}
	if resp2.Status != "accepted" {
		t.Fatalf("expected auto-accepted status 'accepted', got %q", resp2.Status)
	}

	// Both users are now friends
	friendsA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsA.Items) != 1 || friendsA.Items[0].User.ID != userB.ID {
		t.Fatalf("expected user A to have friend B, got %+v", friendsA.Items)
	}
	friendsB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userB.Token, nil))
	if len(friendsB.Items) != 1 || friendsB.Items[0].User.ID != userA.ID {
		t.Fatalf("expected user B to have friend A, got %+v", friendsB.Items)
	}
}

func TestIntegrationRel_FriendRequest_Reject(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_rej_a")
	userB := env.registerUser("rel_fr_rej_b")

	createRec := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	createResp := decodeJSON[relFriendRequestResponse](t, createRec)

	// User B rejects the request
	rejRec := env.do(http.MethodPost, "/api/friend-requests/"+createResp.RelationID+"/reject", userB.Token, nil)
	if rejRec.Code != http.StatusOK {
		t.Fatalf("POST reject expected 200, got %d: %s", rejRec.Code, rejRec.Body.String())
	}
	rejResp := decodeJSON[relFriendRequestResponse](t, rejRec)
	if rejResp.Status != "rejected" {
		t.Fatalf("expected status 'rejected', got %q", rejResp.Status)
	}

	// Verify not friends and no pending requests
	friendsA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsA.Items) != 0 {
		t.Fatalf("expected 0 friends, got %d", len(friendsA.Items))
	}
	inB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userB.Token, nil))
	if len(inB.Items) != 0 {
		t.Fatalf("expected 0 incoming requests, got %d", len(inB.Items))
	}
	outA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil))
	if len(outA.Items) != 0 {
		t.Fatalf("expected 0 outgoing requests, got %d", len(outA.Items))
	}
}

func TestIntegrationRel_FriendRequest_Cancel(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_can_a")
	userB := env.registerUser("rel_fr_can_b")

	createRec := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	createResp := decodeJSON[relFriendRequestResponse](t, createRec)

	// User A cancels their own request
	canRec := env.do(http.MethodPost, "/api/friend-requests/"+createResp.RelationID+"/cancel", userA.Token, nil)
	if canRec.Code != http.StatusOK {
		t.Fatalf("POST cancel expected 200, got %d: %s", canRec.Code, canRec.Body.String())
	}
	canResp := decodeJSON[relFriendRequestResponse](t, canRec)
	if canResp.Status != "cancelled" {
		t.Fatalf("expected status 'cancelled', got %q", canResp.Status)
	}

	// Verify not friends and no pending requests
	friendsA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsA.Items) != 0 {
		t.Fatalf("expected 0 friends, got %d", len(friendsA.Items))
	}
	inB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userB.Token, nil))
	if len(inB.Items) != 0 {
		t.Fatalf("expected 0 incoming requests, got %d", len(inB.Items))
	}
	outA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil))
	if len(outA.Items) != 0 {
		t.Fatalf("expected 0 outgoing requests, got %d", len(outA.Items))
	}
}

func TestIntegrationRel_FriendRequest_InvalidTransitionsAndForbidden(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_inv_a")
	userB := env.registerUser("rel_fr_inv_b")
	userC := env.registerUser("rel_fr_inv_c")

	createRec := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	createResp := decodeJSON[relFriendRequestResponse](t, createRec)
	reqID := createResp.RelationID

	// 1. Requester A tries to accept -> 403 relationship_action_forbidden
	rec := env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userA.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	// 2. Requester A tries to reject -> 403 relationship_action_forbidden
	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/reject", userA.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	// 3. Recipient B tries to cancel -> 403 relationship_action_forbidden
	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/cancel", userB.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	// 4. Third-party C tries to accept / reject / cancel -> 403 relationship_action_forbidden
	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userC.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/reject", userC.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/cancel", userC.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	// 5. Invalid action string -> 403 relationship_action_forbidden
	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/unknown_action", userB.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	// 6. Recipient B accepts the request
	acceptRec := env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userB.Token, nil)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("accept expected 200, got %d: %s", acceptRec.Code, acceptRec.Body.String())
	}

	// 7. Transitioning an already accepted request -> 403 relationship_action_forbidden
	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userB.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/reject", userB.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")

	rec = env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/cancel", userA.Token, nil)
	relAssertError(t, rec, http.StatusForbidden, "relationship_action_forbidden")
}

func TestIntegrationRel_FriendRequest_NotFound(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_nf_a")

	fakeID := uuid.NewString()

	// 1. Transition non-existent request ID -> 404 relationship_not_found
	rec := env.do(http.MethodPost, "/api/friend-requests/"+fakeID+"/accept", userA.Token, nil)
	relAssertError(t, rec, http.StatusNotFound, "relationship_not_found")

	// 2. Create friend request with non-existent recipient -> 404 relationship_not_found
	rec = env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": fakeID,
	})
	relAssertError(t, rec, http.StatusNotFound, "relationship_not_found")
}

func TestIntegrationRel_FriendRequest_DuplicatesAndSelf(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_dup_a")
	userB := env.registerUser("rel_fr_dup_b")

	// 1. Self request -> 400 invalid_relationship
	rec := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userA.ID,
	})
	relAssertError(t, rec, http.StatusBadRequest, "invalid_relationship")

	// 2. Empty recipient ID -> 400 invalid_relationship
	rec = env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": "",
	})
	relAssertError(t, rec, http.StatusBadRequest, "invalid_relationship")

	// 3. Duplicate pending request from same requester -> 409 relationship_conflict
	rec = env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial request expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	recDup := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	relAssertError(t, recDup, http.StatusConflict, "relationship_conflict")

	// 4. Request when already friends -> 409 relationship_conflict
	acceptRec := env.do(http.MethodPost, "/api/friend-requests/"+decodeJSON[relFriendRequestResponse](t, rec).RelationID+"/accept", userB.Token, nil)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("accept expected 200, got %d: %s", acceptRec.Code, acceptRec.Body.String())
	}

	recAfterFriendsA := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	relAssertError(t, recAfterFriendsA, http.StatusConflict, "relationship_conflict")

	recAfterFriendsB := env.do(http.MethodPost, "/api/friend-requests", userB.Token, map[string]string{
		"recipientId": userA.ID,
	})
	relAssertError(t, recAfterFriendsB, http.StatusConflict, "relationship_conflict")
}

func TestIntegrationRel_FriendRequest_BlockedUsers(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_blk_a")
	userB := env.registerUser("rel_fr_blk_b")

	// 1. User A blocks User B
	blockRec := env.do(http.MethodPut, "/api/blocks/"+userB.ID, userA.Token, nil)
	if blockRec.Code != http.StatusNoContent {
		t.Fatalf("block expected 204, got %d: %s", blockRec.Code, blockRec.Body.String())
	}

	// 2. User A cannot send friend request to blocked User B -> 409 relationship_conflict
	recA := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{
		"recipientId": userB.ID,
	})
	relAssertError(t, recA, http.StatusConflict, "relationship_conflict")

	// 3. User B cannot send friend request to blocker User A -> 409 relationship_conflict
	recB := env.do(http.MethodPost, "/api/friend-requests", userB.Token, map[string]string{
		"recipientId": userA.ID,
	})
	relAssertError(t, recB, http.StatusConflict, "relationship_conflict")

	// 4. Accept blocked: user C sends to D, then C and D are blocked before D accepts
	userC := env.registerUser("rel_fr_blk_c")
	userD := env.registerUser("rel_fr_blk_d")
	crRec := env.do(http.MethodPost, "/api/friend-requests", userC.Token, map[string]string{
		"recipientId": userD.ID,
	})
	if crRec.Code != http.StatusCreated {
		t.Fatalf("create request C->D expected 201, got %d: %s", crRec.Code, crRec.Body.String())
	}
	reqID := decodeJSON[relFriendRequestResponse](t, crRec).RelationID

	// Insert block relation directly so pending request status is not changed by blockHandler
	_, err := env.DB.Exec(`INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($1, $2, $3)`, uuid.NewString(), userC.ID, userD.ID)
	if err != nil {
		t.Fatalf("failed to insert block_relations: %v", err)
	}

	// User D attempts to accept when blocked -> 409 relationship_conflict
	acceptRec := env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userD.Token, nil)
	relAssertError(t, acceptRec, http.StatusConflict, "relationship_conflict")
}

func TestIntegrationRel_FriendRequest_Unauthorized(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fr_unauth_a")

	// POST /api/friend-requests without token -> 401
	rec := env.do(http.MethodPost, "/api/friend-requests", "", map[string]string{
		"recipientId": userA.ID,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	// POST /api/friend-requests/:id/:action without token -> 401
	rec = env.do(http.MethodPost, "/api/friend-requests/"+uuid.NewString()+"/accept", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Relationship API Tests (Friendship, Follow, Block)
// ----------------------------------------------------------------------------

func TestIntegrationRel_Friendship_DeleteAndErrors(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fship_del_a")
	userB := env.registerUser("rel_fship_del_b")

	// Become friends
	cr := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{"recipientId": userB.ID})
	reqID := decodeJSON[relFriendRequestResponse](t, cr).RelationID
	env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userB.Token, nil)

	// Verify friends
	friendsA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsA.Items) != 1 {
		t.Fatalf("expected 1 friend, got %d", len(friendsA.Items))
	}

	// 1. User A deletes friendship with B -> 204 No Content
	delRec := env.do(http.MethodDelete, "/api/friendships/"+userB.ID, userA.Token, nil)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE friendship expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Verify friendship removed for both
	friendsAAfter := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsAAfter.Items) != 0 {
		t.Fatalf("expected 0 friends for A, got %d", len(friendsAAfter.Items))
	}
	friendsBAfter := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userB.Token, nil))
	if len(friendsBAfter.Items) != 0 {
		t.Fatalf("expected 0 friends for B, got %d", len(friendsBAfter.Items))
	}

	// 2. Consecutive delete when not friends -> 409 relationship_conflict
	delAgainA := env.do(http.MethodDelete, "/api/friendships/"+userB.ID, userA.Token, nil)
	relAssertError(t, delAgainA, http.StatusConflict, "relationship_conflict")

	delAgainB := env.do(http.MethodDelete, "/api/friendships/"+userA.ID, userB.Token, nil)
	relAssertError(t, delAgainB, http.StatusConflict, "relationship_conflict")

	// 3. Delete friendship with self -> 400 invalid_relationship
	delSelf := env.do(http.MethodDelete, "/api/friendships/"+userA.ID, userA.Token, nil)
	relAssertError(t, delSelf, http.StatusBadRequest, "invalid_relationship")

	// 4. Delete friendship with non-existent user -> 404 relationship_not_found
	delNF := env.do(http.MethodDelete, "/api/friendships/"+uuid.NewString(), userA.Token, nil)
	relAssertError(t, delNF, http.StatusNotFound, "relationship_not_found")

	// 5. Unauthorized -> 401
	delUnauth := env.do(http.MethodDelete, "/api/friendships/"+userB.ID, "", nil)
	if delUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", delUnauth.Code)
	}
}

func TestIntegrationRel_Follow_FollowAndUnfollow(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fol_a")
	userB := env.registerUser("rel_fol_b")

	// 1. Following a regular (non-certified) user -> 409 relationship_conflict
	recReg := env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil)
	relAssertError(t, recReg, http.StatusConflict, "relationship_conflict")

	// 2. Certify user B as certified_public
	relCertifyUser(t, env, userB.ID)

	// 3. User A follows certified User B -> 204 No Content
	followRec := env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil)
	if followRec.Code != http.StatusNoContent {
		t.Fatalf("PUT /api/follows expected 204, got %d: %s", followRec.Code, followRec.Body.String())
	}

	// 4. Verify in following / followers lists
	followingA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userA.Token, nil))
	if len(followingA.Items) != 1 || followingA.Items[0].User.ID != userB.ID {
		t.Fatalf("expected user A following user B, got %+v", followingA.Items)
	}
	if followingA.Items[0].Status != "following" {
		t.Fatalf("expected status 'following', got %q", followingA.Items[0].Status)
	}

	followersB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userB.Token, nil))
	if len(followersB.Items) != 1 || followersB.Items[0].User.ID != userA.ID {
		t.Fatalf("expected user B follower to be user A, got %+v", followersB.Items)
	}
	if followersB.Items[0].Status != "followers" {
		t.Fatalf("expected status 'followers', got %q", followersB.Items[0].Status)
	}

	// 5. Repeat follow -> 409 relationship_conflict
	followDup := env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil)
	relAssertError(t, followDup, http.StatusConflict, "relationship_conflict")

	// 6. User A unfollows User B -> 204 No Content
	unfollowRec := env.do(http.MethodDelete, "/api/follows/"+userB.ID, userA.Token, nil)
	if unfollowRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/follows expected 204, got %d: %s", unfollowRec.Code, unfollowRec.Body.String())
	}

	// 7. Verify removed from lists
	followingAAfter := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userA.Token, nil))
	if len(followingAAfter.Items) != 0 {
		t.Fatalf("expected 0 following after unfollow, got %d", len(followingAAfter.Items))
	}
	followersBAfter := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userB.Token, nil))
	if len(followersBAfter.Items) != 0 {
		t.Fatalf("expected 0 followers after unfollow, got %d", len(followersBAfter.Items))
	}

	// 8. Repeat unfollow -> 409 relationship_conflict
	unfollowDup := env.do(http.MethodDelete, "/api/follows/"+userB.ID, userA.Token, nil)
	relAssertError(t, unfollowDup, http.StatusConflict, "relationship_conflict")

	// 9. Self follow / unfollow -> 400 invalid_relationship
	relAssertError(t, env.do(http.MethodPut, "/api/follows/"+userA.ID, userA.Token, nil), http.StatusBadRequest, "invalid_relationship")
	relAssertError(t, env.do(http.MethodDelete, "/api/follows/"+userA.ID, userA.Token, nil), http.StatusBadRequest, "invalid_relationship")

	// 10. Non-existent user -> 404 relationship_not_found
	relAssertError(t, env.do(http.MethodPut, "/api/follows/"+uuid.NewString(), userA.Token, nil), http.StatusNotFound, "relationship_not_found")
	relAssertError(t, env.do(http.MethodDelete, "/api/follows/"+uuid.NewString(), userA.Token, nil), http.StatusNotFound, "relationship_not_found")

	// 11. Unauthorized -> 401
	if env.do(http.MethodPut, "/api/follows/"+userB.ID, "", nil).Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized follow")
	}
	if env.do(http.MethodDelete, "/api/follows/"+userB.ID, "", nil).Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized unfollow")
	}
}

func TestIntegrationRel_Follow_BlockedUsers(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_fol_blk_a")
	userB := env.registerUser("rel_fol_blk_b")
	relCertifyUser(t, env, userB.ID)

	// User A blocks User B
	env.do(http.MethodPut, "/api/blocks/"+userB.ID, userA.Token, nil)

	// User A cannot follow blocked User B -> 409 relationship_conflict
	relAssertError(t, env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil), http.StatusConflict, "relationship_conflict")

	// Unblock B
	env.do(http.MethodDelete, "/api/blocks/"+userB.ID, userA.Token, nil)

	// User B blocks User A
	env.do(http.MethodPut, "/api/blocks/"+userA.ID, userB.Token, nil)

	// User A cannot follow User B when blocked by User B -> 409 relationship_conflict
	relAssertError(t, env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil), http.StatusConflict, "relationship_conflict")
}

func TestIntegrationRel_Block_SideEffects(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_blk_se_a")
	userB := env.registerUser("rel_blk_se_b")
	userC := env.registerUser("rel_blk_se_c")
	userD := env.registerUser("rel_blk_se_d")

	relCertifyUser(t, env, userA.ID)
	relCertifyUser(t, env, userB.ID)

	// Setup:
	// 1. A and B are friends
	crAB := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{"recipientId": userB.ID})
	reqIDAB := decodeJSON[relFriendRequestResponse](t, crAB).RelationID
	env.do(http.MethodPost, "/api/friend-requests/"+reqIDAB+"/accept", userB.Token, nil)

	// 2. A follows B, and B follows A
	env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil)
	env.do(http.MethodPut, "/api/follows/"+userA.ID, userB.Token, nil)

	// 3. A has outgoing request to C (C has incoming from A)
	env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{"recipientId": userC.ID})

	// 4. D has outgoing request to A (A has incoming from D)
	env.do(http.MethodPost, "/api/friend-requests", userD.Token, map[string]string{"recipientId": userA.ID})

	// Verify initial state
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil)).Items) != 1 {
		t.Fatalf("expected initial friends A=1")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userA.Token, nil)).Items) != 1 {
		t.Fatalf("expected initial following A=1")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userA.Token, nil)).Items) != 1 {
		t.Fatalf("expected initial followers A=1")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil)).Items) != 1 {
		t.Fatalf("expected initial outgoing A=1")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userA.Token, nil)).Items) != 1 {
		t.Fatalf("expected initial incoming A=1")
	}

	// --- Action 1: A blocks B ---
	blkRec := env.do(http.MethodPut, "/api/blocks/"+userB.ID, userA.Token, nil)
	if blkRec.Code != http.StatusNoContent {
		t.Fatalf("PUT /api/blocks expected 204, got %d: %s", blkRec.Code, blkRec.Body.String())
	}

	// Side effect: Friendship is deleted for both A and B
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil)).Items) != 0 {
		t.Fatalf("expected A friends to be 0 after block")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userB.Token, nil)).Items) != 0 {
		t.Fatalf("expected B friends to be 0 after block")
	}

	// Side effect: Follows are deleted in BOTH directions
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userA.Token, nil)).Items) != 0 {
		t.Fatalf("expected A following to be 0 after block")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userB.Token, nil)).Items) != 0 {
		t.Fatalf("expected B followers to be 0 after block")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userB.Token, nil)).Items) != 0 {
		t.Fatalf("expected B following to be 0 after block")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userA.Token, nil)).Items) != 0 {
		t.Fatalf("expected A followers to be 0 after block")
	}

	// --- Action 2: A blocks C (cancels outgoing friend request) ---
	blkCRec := env.do(http.MethodPut, "/api/blocks/"+userC.ID, userA.Token, nil)
	if blkCRec.Code != http.StatusNoContent {
		t.Fatalf("block C expected 204, got %d: %s", blkCRec.Code, blkCRec.Body.String())
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil)).Items) != 0 {
		t.Fatalf("expected A outgoing requests to C cancelled")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userC.Token, nil)).Items) != 0 {
		t.Fatalf("expected C incoming requests from A cancelled")
	}

	// --- Action 3: A blocks D (cancels incoming friend request) ---
	blkDRec := env.do(http.MethodPut, "/api/blocks/"+userD.ID, userA.Token, nil)
	if blkDRec.Code != http.StatusNoContent {
		t.Fatalf("block D expected 204, got %d: %s", blkDRec.Code, blkDRec.Body.String())
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userA.Token, nil)).Items) != 0 {
		t.Fatalf("expected A incoming requests from D cancelled")
	}
	if len(decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userD.Token, nil)).Items) != 0 {
		t.Fatalf("expected D outgoing requests to A cancelled")
	}

	// Check DB status for the friend requests
	var cancelledCount int
	err := env.DB.Get(&cancelledCount, `SELECT COUNT(*) FROM friend_requests WHERE status='cancelled'`)
	if err != nil {
		t.Fatalf("failed to query cancelled friend_requests count: %v", err)
	}
	if cancelledCount != 2 {
		t.Fatalf("expected 2 cancelled friend requests in DB, got %d", cancelledCount)
	}
}

func TestIntegrationRel_Block_UnblockAndErrors(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_blk_err_a")
	userB := env.registerUser("rel_blk_err_b")

	// 1. User A blocks User B -> 204 No Content
	blkRec := env.do(http.MethodPut, "/api/blocks/"+userB.ID, userA.Token, nil)
	if blkRec.Code != http.StatusNoContent {
		t.Fatalf("block expected 204, got %d: %s", blkRec.Code, blkRec.Body.String())
	}

	// 2. Repeat block -> 409 relationship_conflict
	blkDup := env.do(http.MethodPut, "/api/blocks/"+userB.ID, userA.Token, nil)
	relAssertError(t, blkDup, http.StatusConflict, "relationship_conflict")

	// 3. User A unblocks User B -> 204 No Content
	unblkRec := env.do(http.MethodDelete, "/api/blocks/"+userB.ID, userA.Token, nil)
	if unblkRec.Code != http.StatusNoContent {
		t.Fatalf("unblock expected 204, got %d: %s", unblkRec.Code, unblkRec.Body.String())
	}

	// 4. Repeat unblock when not blocked -> 403 relationship_action_forbidden
	// Note: unblockHandler returns 403 (relationship_action_forbidden) rather than 409
	unblkDup := env.do(http.MethodDelete, "/api/blocks/"+userB.ID, userA.Token, nil)
	relAssertError(t, unblkDup, http.StatusForbidden, "relationship_action_forbidden")

	// 5. Block / unblock self -> 400 invalid_relationship
	relAssertError(t, env.do(http.MethodPut, "/api/blocks/"+userA.ID, userA.Token, nil), http.StatusBadRequest, "invalid_relationship")
	relAssertError(t, env.do(http.MethodDelete, "/api/blocks/"+userA.ID, userA.Token, nil), http.StatusBadRequest, "invalid_relationship")

	// 6. Block / unblock non-existent user -> 404 relationship_not_found
	relAssertError(t, env.do(http.MethodPut, "/api/blocks/"+uuid.NewString(), userA.Token, nil), http.StatusNotFound, "relationship_not_found")
	relAssertError(t, env.do(http.MethodDelete, "/api/blocks/"+uuid.NewString(), userA.Token, nil), http.StatusNotFound, "relationship_not_found")

	// 7. Unauthorized -> 401
	if env.do(http.MethodPut, "/api/blocks/"+userB.ID, "", nil).Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized block")
	}
	if env.do(http.MethodDelete, "/api/blocks/"+userB.ID, "", nil).Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized unblock")
	}
}

// ----------------------------------------------------------------------------
// Relationship List API Tests
// ----------------------------------------------------------------------------

func TestIntegrationRel_List_KindsAndEmpty(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_list_kinds_a")

	validKinds := []string{
		"friends",
		"following",
		"followers",
		"incoming-requests",
		"outgoing-requests",
	}

	for _, kind := range validKinds {
		rec := env.do(http.MethodGet, "/api/relationships/"+kind, userA.Token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/relationships/%s expected 200, got %d: %s", kind, rec.Code, rec.Body.String())
		}
		list := decodeJSON[relListResponse](t, rec)
		if len(list.Items) != 0 {
			t.Fatalf("expected 0 items for %s, got %d", kind, len(list.Items))
		}
		if list.HasMore {
			t.Fatalf("expected hasMore false for empty %s", kind)
		}
	}

	// Invalid kind -> 400 invalid_relationship
	relAssertError(t, env.do(http.MethodGet, "/api/relationships/blocked", userA.Token, nil), http.StatusBadRequest, "invalid_relationship")
	relAssertError(t, env.do(http.MethodGet, "/api/relationships/invalid_kind", userA.Token, nil), http.StatusBadRequest, "invalid_relationship")

	// Unauthorized -> 401
	if env.do(http.MethodGet, "/api/relationships/friends", "", nil).Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized list")
	}
}

func TestIntegrationRel_List_BioVisibility(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_bio_a") // regular
	userB := env.registerUser("rel_bio_b") // certified
	userC := env.registerUser("rel_bio_c") // regular

	relCertifyUser(t, env, userB.ID)

	// Set bios
	bioA := "Bio of user A"
	bioB := "Bio of user B"
	bioC := "Bio of user C"

	_, err := env.DB.Exec(`UPDATE users SET bio=$2 WHERE id=$1`, userA.ID, bioA)
	if err != nil {
		t.Fatalf("failed to update bio A: %v", err)
	}
	_, err = env.DB.Exec(`UPDATE users SET bio=$2 WHERE id=$1`, userB.ID, bioB)
	if err != nil {
		t.Fatalf("failed to update bio B: %v", err)
	}
	_, err = env.DB.Exec(`UPDATE users SET bio=$2 WHERE id=$1`, userC.ID, bioC)
	if err != nil {
		t.Fatalf("failed to update bio C: %v", err)
	}

	// 1. C and A become friends:
	// Rule: In friends list, bio is kept even for regular users
	cr := env.do(http.MethodPost, "/api/friend-requests", userC.Token, map[string]string{"recipientId": userA.ID})
	reqID := decodeJSON[relFriendRequestResponse](t, cr).RelationID
	env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userA.Token, nil)

	friendsC := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userC.Token, nil))
	if len(friendsC.Items) != 1 || friendsC.Items[0].User.Bio != bioA {
		t.Fatalf("expected user A bio %q in friends list, got %q", bioA, friendsC.Items[0].User.Bio)
	}

	// 2. C follows B (certified_public):
	// Rule: certified_public user's bio is kept in following list
	env.do(http.MethodPut, "/api/follows/"+userB.ID, userC.Token, nil)
	followingC := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/following", userC.Token, nil))
	if len(followingC.Items) != 1 || followingC.Items[0].User.Bio != bioB {
		t.Fatalf("expected user B bio %q in following list, got %q", bioB, followingC.Items[0].User.Bio)
	}

	// 3. A (regular) follows B (certified_public):
	// Rule: regular user's bio is cleared in followers list
	env.do(http.MethodPut, "/api/follows/"+userB.ID, userA.Token, nil)
	followersB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/followers", userB.Token, nil))
	for _, item := range followersB.Items {
		if item.User.ID == userA.ID {
			if item.User.Bio != "" {
				t.Fatalf("expected regular user A's bio to be empty in followers, got %q", item.User.Bio)
			}
		}
	}

	// 4. Pending friend requests bio behavior:
	// User A (regular) sends friend request to User B (certified_public)
	crAB := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{"recipientId": userB.ID})
	if crAB.Code != http.StatusCreated {
		t.Fatalf("create request expected 201, got %d", crAB.Code)
	}

	// B sees A in incoming-requests: A is regular, so A's bio is cleared
	inB := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/incoming-requests", userB.Token, nil))
	if len(inB.Items) != 1 || inB.Items[0].User.Bio != "" {
		t.Fatalf("expected regular user A's bio to be empty in incoming-requests, got %q", inB.Items[0].User.Bio)
	}

	// A sees B in outgoing-requests: B is certified_public, so B's bio is preserved
	outA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/outgoing-requests", userA.Token, nil))
	if len(outA.Items) != 1 || outA.Items[0].User.Bio != bioB {
		t.Fatalf("expected certified user B's bio %q in outgoing-requests, got %q", bioB, outA.Items[0].User.Bio)
	}
}

func TestIntegrationRel_List_PaginationValidation(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_pag_val_a")

	// 1. beforeUpdatedAt provided without beforeId -> 400 invalid_pagination
	rec1 := env.do(http.MethodGet, "/api/relationships/friends?beforeUpdatedAt=2026-10-31T12:00:00Z", userA.Token, nil)
	relAssertError(t, rec1, http.StatusBadRequest, "invalid_pagination")

	// 2. beforeId provided without beforeUpdatedAt -> 400 invalid_pagination
	rec2 := env.do(http.MethodGet, "/api/relationships/friends?beforeId="+uuid.NewString(), userA.Token, nil)
	relAssertError(t, rec2, http.StatusBadRequest, "invalid_pagination")
}

func TestIntegrationRel_List_PaginationLimitAndOrdering(t *testing.T) {
	env := newTestEnv(t)
	userMain := env.registerUser("rel_pag_main")

	// Insert 55 dummy users and friendships directly into DB
	totalFriends := 55
	baseTime := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	for i := 1; i <= totalFriends; i++ {
		friendID := uuid.NewString()
		username := fmt.Sprintf("dummy_friend_%03d", i)
		updatedAt := baseTime.Add(time.Duration(i) * time.Minute)

		dummyHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		_, err := env.DB.Exec(`
			INSERT INTO users (id, username, secret_key_hash, display_name, region, account_type, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'asia', 'regular', 'active', $5, $6)
		`, friendID, username, dummyHash, username, updatedAt, updatedAt)
		if err != nil {
			t.Fatalf("failed to insert dummy user %d: %v", i, err)
		}

		friendshipID := uuid.NewString()
		lowID := min(userMain.ID, friendID)
		highID := max(userMain.ID, friendID)
		_, err = env.DB.Exec(`
			INSERT INTO friendships (id, user_low_id, user_high_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5)
		`, friendshipID, lowID, highID, updatedAt, updatedAt)
		if err != nil {
			t.Fatalf("failed to insert friendship %d: %v", i, err)
		}
	}

	// 1. Fetch first page
	recPage1 := env.do(http.MethodGet, "/api/relationships/friends", userMain.Token, nil)
	if recPage1.Code != http.StatusOK {
		t.Fatalf("GET friends page 1 expected 200, got %d: %s", recPage1.Code, recPage1.Body.String())
	}
	page1 := decodeJSON[relListResponse](t, recPage1)
	if len(page1.Items) != 50 {
		t.Fatalf("expected 50 items on page 1, got %d", len(page1.Items))
	}
	if !page1.HasMore {
		t.Fatalf("expected hasMore true on page 1")
	}

	// Verify ordering: updated_at DESC
	for i := 0; i < len(page1.Items)-1; i++ {
		cur := page1.Items[i].UpdatedAt
		next := page1.Items[i+1].UpdatedAt
		if cur.Before(next) {
			t.Fatalf("items out of order: item %d (%v) is before item %d (%v)", i, cur, i+1, next)
		}
	}

	// 2. Fetch second page using the 50th item's cursor
	lastItem := page1.Items[49]
	beforeUpdatedAtStr := url.QueryEscape(lastItem.UpdatedAt.Format(time.RFC3339Nano))
	beforeIDStr := url.QueryEscape(lastItem.RelationID)

	page2Path := fmt.Sprintf("/api/relationships/friends?beforeUpdatedAt=%s&beforeId=%s", beforeUpdatedAtStr, beforeIDStr)
	recPage2 := env.do(http.MethodGet, page2Path, userMain.Token, nil)
	if recPage2.Code != http.StatusOK {
		t.Fatalf("GET friends page 2 expected 200, got %d: %s", recPage2.Code, recPage2.Body.String())
	}
	page2 := decodeJSON[relListResponse](t, recPage2)
	if len(page2.Items) != 5 {
		t.Fatalf("expected 5 items on page 2, got %d", len(page2.Items))
	}
	if page2.HasMore {
		t.Fatalf("expected hasMore false on page 2")
	}

	// Ensure no duplicate IDs between page 1 and page 2
	seen := make(map[string]bool)
	for _, item := range page1.Items {
		seen[item.RelationID] = true
	}
	for _, item := range page2.Items {
		if seen[item.RelationID] {
			t.Fatalf("duplicate relation ID across pages: %s", item.RelationID)
		}
		seen[item.RelationID] = true
	}
	if len(seen) != totalFriends {
		t.Fatalf("expected %d total unique friends across pages, got %d", totalFriends, len(seen))
	}
}

func TestIntegrationRel_List_ExcludesInactiveUsers(t *testing.T) {
	env := newTestEnv(t)
	userA := env.registerUser("rel_inact_a")
	userB := env.registerUser("rel_inact_b")

	// Become friends
	cr := env.do(http.MethodPost, "/api/friend-requests", userA.Token, map[string]string{"recipientId": userB.ID})
	reqID := decodeJSON[relFriendRequestResponse](t, cr).RelationID
	env.do(http.MethodPost, "/api/friend-requests/"+reqID+"/accept", userB.Token, nil)

	// User B is active -> visible in friends list
	friendsA := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsA.Items) != 1 {
		t.Fatalf("expected 1 friend, got %d", len(friendsA.Items))
	}

	// Soft delete User B
	_, err := env.DB.Exec(`UPDATE users SET status='deleted' WHERE id=$1`, userB.ID)
	if err != nil {
		t.Fatalf("failed to soft delete user B: %v", err)
	}

	// User B should no longer be visible in friends list
	friendsAAfter := decodeJSON[relListResponse](t, env.do(http.MethodGet, "/api/relationships/friends", userA.Token, nil))
	if len(friendsAAfter.Items) != 0 {
		t.Fatalf("expected 0 friends after user B soft-deleted, got %d", len(friendsAAfter.Items))
	}
}
