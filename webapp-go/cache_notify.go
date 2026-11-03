package isureal

import "strings"

// HandleCacheNotify applies an isureal_cache payload (025-cache-notify.sql): comma-separated
// rel:<userID|*> / mem:<userID|*> tokens; an empty payload (also sent on LISTEN reconnect) clears everything.
func HandleCacheNotify(payload string) {
	if payload == "" {
		InvalidateAllRelationshipsCache()
		InvalidateAllMemoriesCache()
		return
	}
	for _, token := range strings.Split(payload, ",") {
		kind, id, ok := strings.Cut(token, ":")
		if !ok || id == "" {
			continue
		}
		switch kind {
		case "rel":
			if id == "*" {
				InvalidateAllRelationshipsCache()
			} else {
				InvalidateRelationshipsCacheForUser(id)
			}
		case "mem":
			if id == "*" {
				InvalidateAllMemoriesCache()
			} else {
				InvalidateMemoriesCacheForUser(id)
			}
		}
	}
}
