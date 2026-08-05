package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ReferenceEventRepository persists and queries citation events.
type ReferenceEventRepository interface {
	// BatchCreate inserts multiple reference events (one per cited file).
	BatchCreate(ctx context.Context, events []*types.ReferenceEvent) error

	// CountByKnowledge returns total citation count per knowledge_id
	// for the given KB, optionally filtered by time window.
	// Returns map[knowledgeID]count.
	CountByKnowledge(ctx context.Context, tenantID uint64, kbID string, since *time.Time) (map[string]int64, error)

	// CountByKB returns total citation count for a KB, optionally time-windowed.
	CountByKB(ctx context.Context, tenantID uint64, kbID string, since *time.Time) (int64, error)

	// TopCited returns the most-cited knowledge IDs in a KB, with counts.
	// limit controls top-N; since optionally filters by time window.
	TopCited(ctx context.Context, tenantID uint64, kbID string, limit int, since *time.Time) ([]types.KnowledgeCitationCount, error)

	// ZeroCited returns knowledge IDs in the KB that have never been cited.
	ZeroCited(ctx context.Context, tenantID uint64, kbID string) ([]string, error)

	// DeleteByKnowledge removes all events for a knowledge entry (file deletion cleanup).
	DeleteByKnowledge(ctx context.Context, knowledgeID string) error

	// DeleteBySession removes all events produced by a session.
	DeleteBySession(ctx context.Context, sessionID string) error

	// DeleteByKB removes all events for a KB (KB deletion cleanup).
	DeleteByKB(ctx context.Context, kbID string) error
}
