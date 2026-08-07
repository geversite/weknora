package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// FolderSummaryRepository persists and queries folder-level summaries.
type FolderSummaryRepository interface {
	GetByFolder(ctx context.Context, folderID string) (*types.FolderSummary, error)
	// GetByFolderIDs returns summaries for the given folder IDs (for context injection).
	GetByFolderIDs(ctx context.Context, folderIDs []string) ([]*types.FolderSummary, error)
	Upsert(ctx context.Context, summary *types.FolderSummary) error
	DeleteByFolder(ctx context.Context, folderID string) error
	DeleteByKB(ctx context.Context, kbID string) error
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderSummary, error)
}
