package isureal

import (
	"testing"
	"time"
)

func TestRankedBefore_TieBreaking(t *testing.T) {
	t0 := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	tEarlier := t0.Add(-1 * time.Minute)
	tLater := t0.Add(1 * time.Minute)

	tests := []struct {
		name           string
		score          int64
		post           Post
		beforeScore    int64
		beforePostedAt time.Time
		beforeID       string
		want           bool
	}{
		// Score differences take absolute precedence
		{
			name:           "score lower than beforeScore: true",
			score:          99,
			post:           Post{ID: "zzz", PostedAt: tLater},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "aaa",
			want:           true,
		},
		{
			name:           "score higher than beforeScore: false",
			score:          101,
			post:           Post{ID: "aaa", PostedAt: tEarlier},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "zzz",
			want:           false,
		},

		// Score equal, tie-breaker: PostedAt
		{
			name:           "score equal, postedAt earlier: true",
			score:          100,
			post:           Post{ID: "zzz", PostedAt: tEarlier},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "aaa",
			want:           true,
		},
		{
			name:           "score equal, postedAt later: false",
			score:          100,
			post:           Post{ID: "aaa", PostedAt: tLater},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "zzz",
			want:           false,
		},

		// Score equal, PostedAt equal (same instant across timezones), tie-breaker: ID
		{
			name:           "score equal, postedAt equal, id smaller: true",
			score:          100,
			post:           Post{ID: "post-1", PostedAt: t0},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "post-2",
			want:           true,
		},
		{
			name:           "score equal, postedAt equal, id greater: false",
			score:          100,
			post:           Post{ID: "post-3", PostedAt: t0},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "post-2",
			want:           false,
		},
		{
			name:           "score equal, postedAt equal, id equal: false",
			score:          100,
			post:           Post{ID: "post-2", PostedAt: t0},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "post-2",
			want:           false,
		},
		{
			name:           "score equal, postedAt same instant different location, id smaller: true",
			score:          100,
			post:           Post{ID: "post-1", PostedAt: t0.In(time.FixedZone("JST", 9*3600))},
			beforeScore:    100,
			beforePostedAt: t0,
			beforeID:       "post-2",
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rankedBefore(tt.score, tt.post, tt.beforeScore, tt.beforePostedAt, tt.beforeID)
			if got != tt.want {
				t.Fatalf("rankedBefore(%d, post{ID: %s, PostedAt: %v}, %d, %v, %s) = %v, want %v",
					tt.score, tt.post.ID, tt.post.PostedAt, tt.beforeScore, tt.beforePostedAt, tt.beforeID, got, tt.want)
			}
		})
	}
}
