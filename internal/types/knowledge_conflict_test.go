package types

import "testing"

func TestConflictStatusesFitPersistedWidth(t *testing.T) {
	const persistedStatusWidth = 32
	statuses := []string{
		ConflictStatusPending,
		ConflictStatusResolvedKeepBoth,
		ConflictStatusResolvedNewer,
		ConflictStatusResolvedOlder,
		ConflictStatusResolvedNotConflict,
	}
	for _, status := range statuses {
		if len(status) > persistedStatusWidth {
			t.Fatalf("status %q has len=%d, exceeds persisted width %d", status, len(status), persistedStatusWidth)
		}
	}
	if len(ConflictStatusResolvedNotConflict) <= 20 {
		t.Fatalf("test must protect the historical VARCHAR(20) overflow; len=%d", len(ConflictStatusResolvedNotConflict))
	}
}
