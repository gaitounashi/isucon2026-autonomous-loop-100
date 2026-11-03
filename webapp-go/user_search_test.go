package isureal

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func referenceNormalizeSearchText(value string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(value)))
}

func TestNormalizeSearchTextCompatibility(t *testing.T) {
	allASCII := make([]byte, 128)
	for i := range 128 {
		allASCII[i] = byte(i)
	}
	samples := []string{
		"",
		"alice_01.test-user",
		"Alice_Bob-123",
		string(allASCII),
		"Kelvin",
		"Straße",
		"ガイトウナシ",
		"カ\u3099イトウ",
		"İstanbul",
	}
	for _, s := range samples {
		got := normalizeSearchText(s)
		want := referenceNormalizeSearchText(s)
		if got != want {
			t.Fatalf("normalizeSearchText(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestASCIIPushdownCompleteness(t *testing.T) {
	expected := []rune{
		0x00DF, 0x0130, 0x0149, 0x017F, 0x037E, 0x1E9A, 0x1E9E, 0x1FEF,
		0x212A, 0xFB00, 0xFB01, 0xFB02, 0xFB03, 0xFB04, 0xFB05, 0xFB06,
	}

	var foldedToASCII []rune
	for r := rune(0x80); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		folded := referenceNormalizeSearchText(string(r))
		hasASCII := false
		for i := 0; i < len(folded); i++ {
			if folded[i] < 0x80 {
				hasASCII = true
				break
			}
		}
		if hasASCII {
			foldedToASCII = append(foldedToASCII, r)
		}
	}
	if !slices.Equal(foldedToASCII, expected) {
		t.Fatalf("foldedToASCII = %v, want %v", foldedToASCII, expected)
	}
}

func TestSearchUsers_ExclusionAndMatching(t *testing.T) {
	env := newTestEnv(t)

	// Viewer
	viewer := env.registerUser("viewer_search_test")
	env.do(http.MethodPatch, "/api/me", viewer.Token, map[string]string{
		"displayName": "ViewerUniqueSearchTarget",
	})

	// User matching by username substring
	uUsernameMatch := env.registerUser("match_sub_user")
	env.do(http.MethodPatch, "/api/me", uUsernameMatch.Token, map[string]string{
		"displayName": "Completely Different Name",
	})

	// User matching by display_name substring
	uDisplayNameMatch := env.registerUser("other_user_xyz")
	env.do(http.MethodPatch, "/api/me", uDisplayNameMatch.Token, map[string]string{
		"displayName": "TargetSubstringDisplay",
	})

	// User blocked by viewer
	uBlockedByViewer := env.registerUser("match_blocked_by_viewer")
	env.do(http.MethodPatch, "/api/me", uBlockedByViewer.Token, map[string]string{
		"displayName": "TargetSubstringDisplay Blocked",
	})
	if rec := env.do(http.MethodPut, "/api/blocks/"+uBlockedByViewer.ID, viewer.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("failed to block user: %d %s", rec.Code, rec.Body.String())
	}

	// User who blocked viewer
	uBlockerOfViewer := env.registerUser("match_blocker_of_viewer")
	env.do(http.MethodPatch, "/api/me", uBlockerOfViewer.Token, map[string]string{
		"displayName": "TargetSubstringDisplay Blocker",
	})
	if rec := env.do(http.MethodPut, "/api/blocks/"+viewer.ID, uBlockerOfViewer.Token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("failed to block viewer: %d %s", rec.Code, rec.Body.String())
	}

	// 1. Viewer is never returned even when query matches viewer's username or display_name
	recViewerSearch := env.do(http.MethodGet, "/api/users?q=viewer_search_test", viewer.Token, nil)
	if recViewerSearch.Code != http.StatusOK {
		t.Fatalf("viewer search failed: %d %s", recViewerSearch.Code, recViewerSearch.Body.String())
	}
	respViewerSearch := decodeJSON[searchUsersResponse](t, recViewerSearch)
	for _, it := range respViewerSearch.Items {
		if it.ID == viewer.ID {
			t.Fatalf("viewer should never be returned in search results")
		}
	}

	recViewerDisplaySearch := env.do(http.MethodGet, "/api/users?q=ViewerUniqueSearchTarget", viewer.Token, nil)
	if recViewerDisplaySearch.Code != http.StatusOK {
		t.Fatalf("viewer display search failed: %d %s", recViewerDisplaySearch.Code, recViewerDisplaySearch.Body.String())
	}
	respViewerDisplaySearch := decodeJSON[searchUsersResponse](t, recViewerDisplaySearch)
	for _, it := range respViewerDisplaySearch.Items {
		if it.ID == viewer.ID {
			t.Fatalf("viewer should never be returned in search results")
		}
	}

	// 2. Matching by username substring
	recUserSearch := env.do(http.MethodGet, "/api/users?q=sub_user", viewer.Token, nil)
	if recUserSearch.Code != http.StatusOK {
		t.Fatalf("search by username substring failed: %d %s", recUserSearch.Code, recUserSearch.Body.String())
	}
	respUserSearch := decodeJSON[searchUsersResponse](t, recUserSearch)
	if len(respUserSearch.Items) != 1 || respUserSearch.Items[0].ID != uUsernameMatch.ID {
		t.Fatalf("expected uUsernameMatch, got %+v", respUserSearch.Items)
	}

	// 3. Matching by display_name substring and excluding blocked / blocker users
	recDisplaySearch := env.do(http.MethodGet, "/api/users?q=SubstringDisplay", viewer.Token, nil)
	if recDisplaySearch.Code != http.StatusOK {
		t.Fatalf("search by display name substring failed: %d %s", recDisplaySearch.Code, recDisplaySearch.Body.String())
	}
	respDisplaySearch := decodeJSON[searchUsersResponse](t, recDisplaySearch)
	if len(respDisplaySearch.Items) != 1 || respDisplaySearch.Items[0].ID != uDisplayNameMatch.ID {
		t.Fatalf("expected only uDisplayNameMatch, got %+v", respDisplaySearch.Items)
	}
}

func TestSearchUsers_PaginationWithBlockedUsersBetweenPages(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.registerUser("page_viewer_test")

	// Create 55 users in alphabetical order:
	// p_user_00 through p_user_49 (50 users for page 1)
	// p_user_50 through p_user_52 (3 users blocked either by viewer or blocking viewer)
	// p_user_53 through p_user_54 (2 users for page 2)
	dummySecretKeyHash := "0000000000000000000000000000000000000000000000000000000000000000"
	type createdUser struct {
		id       string
		username string
	}
	var users []createdUser
	for i := 0; i < 55; i++ {
		uid := uuid.NewString()
		username := fmt.Sprintf("p_user_%02d", i)
		displayName := fmt.Sprintf("Pagination User %02d", i)
		_, err := env.DB.Exec(`
			INSERT INTO users (id, username, secret_key_hash, display_name, region, status)
			VALUES ($1, $2, $3, $4, 'asia', 'active')
		`, uid, username, dummySecretKeyHash, displayName)
		if err != nil {
			t.Fatalf("failed to insert user %d: %v", i, err)
		}
		users = append(users, createdUser{id: uid, username: username})
	}

	// Block p_user_50 and p_user_51 (viewer blocks them)
	_, err := env.DB.Exec(`
		INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($4, $1, $2), ($5, $1, $3)
	`, viewer.ID, users[50].id, users[51].id, uuid.NewString(), uuid.NewString())
	if err != nil {
		t.Fatalf("failed to insert block relations: %v", err)
	}

	// p_user_52 blocks viewer
	_, err = env.DB.Exec(`
		INSERT INTO block_relations (id, blocker_id, blocked_id) VALUES ($3, $1, $2)
	`, users[52].id, viewer.ID, uuid.NewString())
	if err != nil {
		t.Fatalf("failed to insert block relations: %v", err)
	}

	// Page 1: Query for "p_user_"
	page1Rec := env.do(http.MethodGet, "/api/users?q=p_user_", viewer.Token, nil)
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
	if page1.Items[0].Username != "p_user_00" || page1.Items[49].Username != "p_user_49" {
		t.Fatalf("unexpected items on page 1: first=%s, last=%s", page1.Items[0].Username, page1.Items[49].Username)
	}

	// Page 2: pass beforeUsername and beforeId from last item of page 1
	lastItem := page1.Items[49]
	cursorURL := fmt.Sprintf("/api/users?q=p_user_&beforeUsername=%s&beforeId=%s", url.QueryEscape(lastItem.Username), url.QueryEscape(lastItem.ID))
	page2Rec := env.do(http.MethodGet, cursorURL, viewer.Token, nil)
	if page2Rec.Code != http.StatusOK {
		t.Fatalf("page 2 failed: %d %s", page2Rec.Code, page2Rec.Body.String())
	}
	page2 := decodeJSON[searchUsersResponse](t, page2Rec)

	// Since p_user_50, p_user_51, p_user_52 are blocked, page 2 should contain p_user_53 and p_user_54
	if len(page2.Items) != 2 {
		t.Fatalf("expected exactly 2 items on page 2, got %d: %+v", len(page2.Items), page2.Items)
	}
	if page2.HasMore {
		t.Fatalf("expected hasMore=false on page 2")
	}
	if page2.Items[0].Username != "p_user_53" || page2.Items[1].Username != "p_user_54" {
		t.Fatalf("unexpected items on page 2: %s, %s", page2.Items[0].Username, page2.Items[1].Username)
	}
}

