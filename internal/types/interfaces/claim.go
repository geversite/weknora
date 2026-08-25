package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// ClaimRepository persists atomic claims extracted from chunks and wiki pages
// (C1). Implementations must keep writes idempotent via the replace-by-batch
// contract documented on ReplaceBySource.
type ClaimRepository interface {
	// ReplaceBySource atomically replaces the claim set of one source: the
	// new rows (sharing one ExtractBatchID) are inserted and rows of the same
	// (sourceType, sourceID) with a different batch ID are deleted, in a
	// single transaction. Passing an empty slice only performs the delete.
	ReplaceBySource(ctx context.Context, sourceType, sourceID, batchID string, claims []*types.Claim) error
	// DeleteBySource removes every claim of one source (chunk disabled,
	// wiki page deleted, ...).
	DeleteBySource(ctx context.Context, sourceType, sourceID string) error
	// DeleteByKnowledge removes every claim belonging to one knowledge file.
	DeleteByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error
	// DeleteByKnowledgeBase removes every claim of one KB.
	DeleteByKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) error
	// ListBySource returns the current claims of one source.
	ListBySource(ctx context.Context, sourceType, sourceID string) ([]*types.Claim, error)
	// ListByKnowledge returns every claim of one knowledge file (all chunks).
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.Claim, error)
	// ListByKeys returns claims in the KB whose ClaimKey is in keys,
	// excluding rows whose SourceID equals excludeSourceID and rows whose
	// KnowledgeID equals excludeKnowledgeID (pass "" to skip either filter).
	ListByKeys(ctx context.Context, tenantID uint64, kbID string, keys []string,
		excludeSourceID, excludeKnowledgeID string) ([]*types.Claim, error)
	// CountBySource reports how many claims one source currently has.
	CountBySource(ctx context.Context, sourceType, sourceID string) (int64, error)
}

// ClaimExtractService extracts atomic claims from freshly ingested documents
// and user-edited wiki pages (C1). All entry points are best-effort: failures
// are logged and never block ingestion or wiki editing.
type ClaimExtractService interface {
	// Handle is the asynq entry point for types.TypeClaimExtract.
	Handle(ctx context.Context, task *asynq.Task) error
	// EnqueueForKnowledge queues extraction for all enabled chunks of one
	// knowledge file.
	EnqueueForKnowledge(ctx context.Context, tenantID uint64, kbID, knowledgeID string, reason string) error
	// EnqueueForWikiPage queues (debounced) extraction for one wiki page.
	EnqueueForWikiPage(ctx context.Context, tenantID uint64, kbID, pageID string, reason string) error
}
