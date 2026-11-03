package isureal

import (
	"strings"
	"testing"
)

func TestSchema_StorageTuning(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		table string
		want  []string
	}{
		{"users", []string{"fillfactor=80", "autovacuum_vacuum_scale_factor=0.02", "autovacuum_analyze_scale_factor=0.02"}},
		{"login_sessions", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"friend_requests", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"friendships", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"follows", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"realmojis", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"comments", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"posts", []string{"autovacuum_vacuum_scale_factor=0.02"}},
		{"notifications", []string{"autovacuum_vacuum_scale_factor=0.02"}},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			var options string
			if err := env.DB.Get(&options, `SELECT coalesce(array_to_string(reloptions, ','), '') FROM pg_class WHERE relname = $1 AND relkind = 'r'`, tt.table); err != nil {
				t.Fatalf("failed to read reloptions: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(options, w) {
					t.Fatalf("expected %q in reloptions of %s, got %q", w, tt.table, options)
				}
			}
		})
	}
}

func TestSchema_FunctionsExist(t *testing.T) {
	env := newTestEnv(t)
	var names []string
	if err := env.DB.Select(&names, `SELECT proname FROM pg_proc WHERE proname LIKE 'isureal\_%' ORDER BY proname`); err != nil {
		t.Fatalf("failed to list functions: %v", err)
	}
	have := make(map[string]bool, len(names))
	for _, n := range names {
		have[n] = true
	}
	for _, want := range []string{
		"isureal_create_comment",
		"isureal_replace_realmoji",
		"isureal_create_post",
		"isureal_create_friend_request",
		"isureal_transition_friend_request",
		"isureal_delete_friendship",
		"isureal_follow",
		"isureal_unfollow",
		"isureal_block",
		"isureal_unblock",
		"isureal_put_profile_image",
		"isureal_delete_profile_image",
		"isureal_update_post",
		"isureal_delete_post",
		"isureal_delete_comment",
		"isureal_delete_realmoji",
	} {
		if !have[want] {
			t.Errorf("function %s is not defined (have %v)", want, names)
		}
	}
}
