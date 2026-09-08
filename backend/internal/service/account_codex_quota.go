package service

// IsCodexQuotaObservationExtraKey identifies provider-owned observations, not
// editable quota policy. Account edits and duplication must not publish them.
func IsCodexQuotaObservationExtraKey(key string) bool {
	switch key {
	case "codex_primary_used_percent", "codex_primary_reset_after_seconds", "codex_primary_window_minutes",
		"codex_secondary_used_percent", "codex_secondary_reset_after_seconds", "codex_secondary_window_minutes",
		"codex_primary_over_secondary_percent", "codex_usage_updated_at", "codex_usage_observed_at_us",
		"codex_5h_used_percent", "codex_5h_reset_after_seconds", "codex_5h_window_minutes", "codex_5h_reset_at",
		"codex_7d_used_percent", "codex_7d_reset_after_seconds", "codex_7d_window_minutes", "codex_7d_reset_at":
		return true
	default:
		return false
	}
}
