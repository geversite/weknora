package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ConflictDetectionRunRepository persists one aggregate measurement row per
// conflict:detect execution. It is deliberately separate from
// KnowledgeConflictRepository: a detector run that finds no conflicts (or
// fails) is still valuable evidence for C1/C2 experiments.
type ConflictDetectionRunRepository interface {
	Create(ctx context.Context, run *types.ConflictDetectionRun) error
	ListByKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) ([]*types.ConflictDetectionRun, error)
}
