package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// DisputedFactRepository persists C4-Lite's fact-level aggregate rows.
type DisputedFactRepository interface {
	// UpsertByFactKey creates or refreshes a cluster while preserving its ID.
	UpsertByFactKey(ctx context.Context, fact *types.DisputedFact) (*types.DisputedFact, error)
	GetByID(ctx context.Context, tenantID uint64, kbID, factID string) (*types.DisputedFact, error)
	ListByKB(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.DisputedFact, error)
	CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error)
	// DeleteExceptFactKeys removes orphaned aggregates during a full rebuild.
	// An empty key list removes every cluster in the KB.
	DeleteExceptFactKeys(ctx context.Context, tenantID uint64, kbID string, factKeys []string) error
	DeleteByKB(ctx context.Context, tenantID uint64, kbID string) error
}

// ConflictClusterService is C4-Lite's deterministic raw-conflict →
// DisputedFact aggregation interface. Rebuild is idempotent and intentionally
// exposed for scripts: it makes a cluster snapshot reproducible after async
// conflict tasks have completed.
type ConflictClusterService interface {
	Rebuild(ctx context.Context, tenantID uint64, kbID string) (*types.DisputedFactRebuildResult, error)
	ListDisputedFacts(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.DisputedFact, int64, error)
	ResolveDisputedFact(ctx context.Context, tenantID uint64, resolverUserID string, kbID string, req types.DisputedFactResolution) (*types.DisputedFactAdjudicationResult, error)
	// AdoptDisputedFactWinner accepts a currently displayed C4.6 proposal and
	// propagates its one global winner only after optimistic snapshot checks.
	AdoptDisputedFactWinner(ctx context.Context, tenantID uint64, resolverUserID string, kbID string, req types.DisputedFactWinnerAdoption) (*types.DisputedFactWinnerAdoptionResult, error)
	// ReopenDisputedFactWinner revokes one durable active adoption and restores
	// its exact members/chunks to a pending review state after snapshot checks.
	ReopenDisputedFactWinner(ctx context.Context, tenantID uint64, resolverUserID string, kbID string, req types.DisputedFactWinnerRevocation) (*types.DisputedFactWinnerRevocationResult, error)
}
