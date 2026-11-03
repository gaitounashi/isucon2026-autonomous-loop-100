package isureal

import (
	"strings"
	"testing"
	"time"
)

func floatPtr(f float64) *float64 {
	return &f
}

func stringPtr(s string) *string {
	return &s
}

func TestPostLocation_Valid(t *testing.T) {
	tests := []struct {
		name     string
		location PostLocation
		want     bool
	}{
		// kind: "none"
		{
			name:     "none: valid with all nil",
			location: PostLocation{Kind: "none"},
			want:     true,
		},
		{
			name:     "none: invalid with latitude set",
			location: PostLocation{Kind: "none", Latitude: floatPtr(35.68)},
			want:     false,
		},
		{
			name:     "none: invalid with longitude set",
			location: PostLocation{Kind: "none", Longitude: floatPtr(139.76)},
			want:     false,
		},
		{
			name:     "none: invalid with locality set",
			location: PostLocation{Kind: "none", Locality: stringPtr("Tokyo")},
			want:     false,
		},

		// kind: "exact"
		{
			name: "exact: valid within range",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(35.6812),
				Longitude: floatPtr(139.7671),
			},
			want: true,
		},
		{
			name: "exact: valid boundaries (-90, -180)",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(-90.0),
				Longitude: floatPtr(-180.0),
			},
			want: true,
		},
		{
			name: "exact: valid boundaries (90, 180)",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(90.0),
				Longitude: floatPtr(180.0),
			},
			want: true,
		},
		{
			name: "exact: invalid latitude < -90",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(-90.0001),
				Longitude: floatPtr(0.0),
			},
			want: false,
		},
		{
			name: "exact: invalid latitude > 90",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(90.0001),
				Longitude: floatPtr(0.0),
			},
			want: false,
		},
		{
			name: "exact: invalid longitude < -180",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(0.0),
				Longitude: floatPtr(-180.0001),
			},
			want: false,
		},
		{
			name: "exact: invalid longitude > 180",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(0.0),
				Longitude: floatPtr(180.0001),
			},
			want: false,
		},
		{
			name: "exact: invalid with nil latitude",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  nil,
				Longitude: floatPtr(139.76),
			},
			want: false,
		},
		{
			name: "exact: invalid with nil longitude",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(35.68),
				Longitude: nil,
			},
			want: false,
		},
		{
			name: "exact: invalid with locality present",
			location: PostLocation{
				Kind:      "exact",
				Latitude:  floatPtr(35.68),
				Longitude: floatPtr(139.76),
				Locality:  stringPtr("Tokyo"),
			},
			want: false,
		},

		// kind: "approximate"
		{
			name: "approximate: valid locality length 1",
			location: PostLocation{
				Kind:     "approximate",
				Locality: stringPtr("A"),
			},
			want: true,
		},
		{
			name: "approximate: valid locality length 100",
			location: PostLocation{
				Kind:     "approximate",
				Locality: stringPtr(strings.Repeat("日", 100)),
			},
			want: true,
		},
		{
			name: "approximate: invalid empty locality",
			location: PostLocation{
				Kind:     "approximate",
				Locality: stringPtr(""),
			},
			want: false,
		},
		{
			name: "approximate: invalid locality length 101",
			location: PostLocation{
				Kind:     "approximate",
				Locality: stringPtr(strings.Repeat("a", 101)),
			},
			want: false,
		},
		{
			name: "approximate: invalid nil locality",
			location: PostLocation{
				Kind:     "approximate",
				Locality: nil,
			},
			want: false,
		},
		{
			name: "approximate: invalid non-utf8 locality",
			location: PostLocation{
				Kind:     "approximate",
				Locality: stringPtr(string([]byte{0xff, 0xfe})),
			},
			want: false,
		},
		{
			name: "approximate: invalid with latitude present",
			location: PostLocation{
				Kind:     "approximate",
				Latitude: floatPtr(35.68),
				Locality: stringPtr("Tokyo"),
			},
			want: false,
		},
		{
			name: "approximate: invalid with longitude present",
			location: PostLocation{
				Kind:      "approximate",
				Longitude: floatPtr(139.76),
				Locality:  stringPtr("Tokyo"),
			},
			want: false,
		},

		// default / unknown kinds
		{
			name: "unknown kind: empty string",
			location: PostLocation{
				Kind: "",
			},
			want: false,
		},
		{
			name: "unknown kind: invalid_kind",
			location: PostLocation{
				Kind: "other",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.location.valid()
			if got != tt.want {
				t.Fatalf("location.valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestViewerContext_CanView(t *testing.T) {
	windowEnded := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		viewer viewerContext
		post   Post
		want   bool
	}{
		// Author can always view own post, regardless of other conditions
		{
			name: "author: can view own post even if window ended and not posted today",
			viewer: viewerContext{
				User:           User{ID: "user-1"},
				HasPostedToday: false,
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "user-1",
				WindowEndedAt: &windowEnded,
				Visibility:    "private",
			},
			want: true,
		},
		{
			name: "author: can view own post even if blocked map contains own ID",
			viewer: viewerContext{
				User:           User{ID: "user-1"},
				HasPostedToday: false,
				Blocked:        map[string]bool{"user-1": true},
			},
			post: Post{
				ID:         "post-1",
				AuthorID:   "user-1",
				Visibility: "public",
			},
			want: true,
		},

		// Non-author: Window ended check
		{
			name: "non-author: cannot view if window ended",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: &windowEnded,
				Visibility:    "public",
			},
			want: false,
		},

		// Non-author: HasPostedToday check
		{
			name: "non-author: cannot view if viewer has not posted today",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: false,
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "public",
			},
			want: false,
		},

		// Non-author: Blocked check
		{
			name: "non-author: cannot view if author is in blocked map",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Blocked:        map[string]bool{"author-1": true},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "public",
			},
			want: false,
		},

		// Visibility: public
		{
			name: "public: can view when active window, posted today, not blocked",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Blocked:        map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "public",
			},
			want: true,
		},

		// Visibility: friends
		{
			name: "friends: can view if author is friend",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Friends:        map[string]bool{"author-1": true},
				Blocked:        map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends",
			},
			want: true,
		},
		{
			name: "friends: cannot view if author is not friend",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Friends:        map[string]bool{"other-friend": true},
				Blocked:        map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends",
			},
			want: false,
		},
		{
			name: "friends: cannot view if author is only friend-of-friend",
			viewer: viewerContext{
				User:             User{ID: "viewer-1"},
				HasPostedToday:   true,
				Friends:          map[string]bool{},
				FriendsOfFriends: map[string]bool{"author-1": true},
				Blocked:          map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends",
			},
			want: false,
		},

		// Visibility: friends_of_friends
		{
			name: "friends_of_friends: can view if author is direct friend",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Friends:        map[string]bool{"author-1": true},
				Blocked:        map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends_of_friends",
			},
			want: true,
		},
		{
			name: "friends_of_friends: can view if author is friend of friend",
			viewer: viewerContext{
				User:             User{ID: "viewer-1"},
				HasPostedToday:   true,
				Friends:          map[string]bool{},
				FriendsOfFriends: map[string]bool{"author-1": true},
				Blocked:          map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends_of_friends",
			},
			want: true,
		},
		{
			name: "friends_of_friends: cannot view if neither friend nor friend of friend",
			viewer: viewerContext{
				User:             User{ID: "viewer-1"},
				HasPostedToday:   true,
				Friends:          map[string]bool{"other": true},
				FriendsOfFriends: map[string]bool{"other-fof": true},
				Blocked:          map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "friends_of_friends",
			},
			want: false,
		},

		// Unknown / unsupported visibility
		{
			name: "unsupported visibility: returns false",
			viewer: viewerContext{
				User:           User{ID: "viewer-1"},
				HasPostedToday: true,
				Friends:        map[string]bool{"author-1": true},
				Blocked:        map[string]bool{},
			},
			post: Post{
				ID:            "post-1",
				AuthorID:      "author-1",
				WindowEndedAt: nil,
				Visibility:    "private",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.viewer.canView(tt.post)
			if got != tt.want {
				t.Fatalf("viewer.canView() = %v, want %v", got, tt.want)
			}
		})
	}
}
