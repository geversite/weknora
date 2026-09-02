package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// KnowledgeConflictRepository persists and queries file-level content conflicts (M3).
type KnowledgeConflictRepository interface {
	// BatchCreate inserts multiple conflicts in batches.
	BatchCreate(ctx context.Context, conflicts []*types.KnowledgeConflict) error
	// ListByKB lists conflicts for a KB, optionally filtered by status, ordered
	// newest-first with pagination.
	ListByKB(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.KnowledgeConflict, error)
	// ListByKBForClustering returns every conflict in a KB (all statuses) in a
	// deterministic order. C4-Lite uses it to rebuild fact aggregates.
	ListByKBForClustering(ctx context.Context, tenantID uint64, kbID string) ([]*types.KnowledgeConflict, error)
	// CountByKB counts conflicts for a KB, optionally filtered by status.
	CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error)
	// GetByID returns a single conflict by id.
	GetByID(ctx context.Context, id string) (*types.KnowledgeConflict, error)
	// Update persists an updated conflict row.
	Update(ctx context.Context, conflict *types.KnowledgeConflict) error
	// ResolvePendingByClusterID atomically applies a no-disable resolution to
	// every pending member of one C4 DisputedFact and returns those members.
	ResolvePendingByClusterID(ctx context.Context, tenantID uint64, kbID, clusterID, status, resolverUserID, note string) ([]*types.KnowledgeConflict, error)
	// AdoptPendingWinnerProposal atomically re-reads a C4.6 proposal and all
	// current members, validates the caller's optimistic review snapshot, then
	// propagates an explicit global winner. It must never infer a winner from a
	// member row's local A/B orientation.
	AdoptPendingWinnerProposal(ctx context.Context, tenantID uint64, kbID, resolverUserID string, req types.DisputedFactWinnerAdoption) (*types.DisputedFactWinnerAdoptionResult, error)
	// ReopenWinnerAdoption atomically revokes one current C4.7 adoption after
	// validating its durable record, raw members and disabled chunk snapshots.
	ReopenWinnerAdoption(ctx context.Context, tenantID uint64, kbID, resolverUserID string, req types.DisputedFactWinnerRevocation) (*types.DisputedFactWinnerRevocationResult, error)
	// ListPendingByChunkIDs returns pending conflicts where either chunk is in the list.
	// Used by rerank penalty and answer-time divergence tagging.
	ListPendingByChunkIDs(ctx context.Context, chunkIDs []string) ([]*types.KnowledgeConflict, error)
	// ListPendingByKnowledgeID returns pending conflicts where either knowledge is the one given.
	ListPendingByKnowledgeID(ctx context.Context, knowledgeID string) ([]*types.KnowledgeConflict, error)
	// HasPendingByChunkPair reports whether a pending conflict already exists for a
	// chunk pair (either orientation). Used for cross-run detection de-dup.
	HasPendingByChunkPair(ctx context.Context, chunkAID, chunkBID string) (bool, error)
	// DeleteByKnowledge removes all conflicts involving the given knowledge (file delete).
	DeleteByKnowledge(ctx context.Context, knowledgeID string) error
	// DeleteByKB removes all conflicts for a KB (KB delete).
	DeleteByKB(ctx context.Context, kbID string) error
}

// ConflictDetectService performs incremental post-upload conflict detection:
// coarse semantic filter + LLM fine adjudication, persisting pending conflicts.
type ConflictDetectService interface {
	// Enqueue queues a conflict detection task for a freshly-uploaded knowledge.
	Enqueue(ctx context.Context, knowledgeID, kbID string, tenantID uint64) error
	// Handle implements the asynq handler for TypeConflictDetect.
	Handle(ctx context.Context, task *asynq.Task) error
}

// ConflictAdjudicateService exposes the adjudication queue for Owner/Admin.
type ConflictAdjudicateService interface {
	// ListConflicts returns a page of conflicts for a KB.
	ListConflicts(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.KnowledgeConflict, int64, error)
	// GetConflictStats returns pending/resolved counts for a KB.
	GetConflictStats(ctx context.Context, tenantID uint64, kbID string) (map[string]int64, error)
	// Resolve adjudicates a conflict, applying the disable/penalty side-effects.
	Resolve(ctx context.Context, resolverUserID string, req types.ConflictResolution) (*types.ConflictAdjudicationResult, error)
}
