package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeFolderRepository persists and queries the file-level folder tree.
type KnowledgeFolderRepository interface {
	Create(ctx context.Context, folder *types.KnowledgeFolder) error
	GetByID(ctx context.Context, id string) (*types.KnowledgeFolder, error)
	Update(ctx context.Context, folder *types.KnowledgeFolder) error
	Delete(ctx context.Context, id string) error

	// ListByKB returns all non-deleted folders in a KB (for tree building).
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.KnowledgeFolder, error)

	// ListChildren returns direct child folders of parentID (empty = root).
	ListChildren(ctx context.Context, tenantID uint64, kbID, parentID string) ([]*types.KnowledgeFolder, error)

	// ExistsByName reports whether a non-deleted folder with the given name
	// exists under parentID (empty = root) in the KB. excludeID allows the
	// rename path to skip the folder itself.
	ExistsByName(ctx context.Context, tenantID uint64, kbID, parentID, name, excludeID string) (bool, error)

	// GetSubtree returns all folders under the given folder IDs (inclusive),
	// using the materialized path prefix match.
	GetSubtree(ctx context.Context, tenantID uint64, kbID string, folderIDs []string) ([]*types.KnowledgeFolder, error)

	// CountFilesInFolder returns the number of knowledge entries directly in
	// the folder (not recursive). folderID="" counts root-level files.
	CountFilesInFolder(ctx context.Context, kbID, folderID string) (int64, error)

	// HasChildren reports whether a folder has child folders.
	HasChildren(ctx context.Context, tenantID uint64, kbID, folderID string) (bool, error)

	// UpdatePathBatch updates path + depth for a batch of folders (used in move cascade).
	UpdatePathBatch(ctx context.Context, folders []*types.KnowledgeFolder) error
	// UpdateStatus updates only the summary_status column of a folder.
	UpdateStatus(ctx context.Context, folderID, status string) error

	// MoveFiles updates folder_id for all knowledge entries from oldFolderID to newFolderID.
	// Used when deleting a folder (cascade files to parent).
	MoveFiles(ctx context.Context, kbID, oldFolderID, newFolderID string) (int64, error)

	// DeleteByKB removes all folders for a KB (KB deletion cleanup).
	DeleteByKB(ctx context.Context, kbID string) error
}
