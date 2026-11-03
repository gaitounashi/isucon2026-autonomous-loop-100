package isureal

// Registers resets for the optimized implementation's in-process caches.
// This file is intentionally not named integration_*_test.go, so the baseline CI job
// (which copies only integration_*_test.go onto the initial implementation) does not include it.
func init() {
	testEnvResetHooks = append(testEnvResetHooks,
		ResetSignatureCache,
		InvalidateAllSessions,
		InvalidateAllImageMeta,
		InvalidateAllPostingWindowCache,
		InvalidateAllMyCurrentPostCache,
		InvalidateAllRelationshipsCache,
		InvalidateAllMemoriesCache,
		InvalidateAllCommentsCache,
		InvalidateAllRealMojisCache,
		InvalidateAllFeedCache,
	)
}
