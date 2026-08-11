package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeFolderService manages the file-level folder tree.
type KnowledgeFolderService interface {
	// ListTree returns the full folder tree for a KB with file counts.
	ListTree(ctx context.Context, kbID string) (*types.KnowledgeFolderTreeResponse, error)
	// Create creates a folder under parentID.
	Create(ctx context.Context, kbID string, req *types.KnowledgeFolderCreateRequest) (*types.KnowledgeFolder, error)
	// CreateOrGet creates a folder, or if a same-name folder already exists
	// under the same parent, returns the existing one. Used for folder upload
	// merge: uploading folder A (containing C) when A (containing B) already
	// exists merges them into A containing both B and C.
	CreateOrGet(ctx context.Context, kbID string, req *types.KnowledgeFolderCreateRequest) (*types.KnowledgeFolder, error)
	// Update renames and/or reparents a folder; reparenting cascades paths.
	Update(ctx context.Context, kbID, folderID string, req *types.KnowledgeFolderUpdateRequest) (*types.KnowledgeFolder, error)
	// Delete removes a folder; files cascade to its parent; summaries/chunks cleaned.
	Delete(ctx context.Context, kbID, folderID string) error
	// MoveFilesToFolder assigns knowledge entries to a folder (empty = root).
	MoveFilesToFolder(ctx context.Context, kbID, folderID string, knowledgeIDs []string) error
	// GetGovernanceReport builds the folder governance health report (M4).
	GetGovernanceReport(ctx context.Context, kbID string) (*types.FolderGovernanceReport, error)
	// GetByID returns a folder by its ID.
	GetByID(ctx context.Context, folderID string) (*types.KnowledgeFolder, error)

	// ListChildrenWithMeta returns the direct child folders of a folder (or root
	// when folderID="") enriched with file counts and has-children flags.
	ListChildrenWithMeta(ctx context.Context, kbID, folderID string) ([]*types.KnowledgeFolderNode, error)

	// ListByFolderPaged returns knowledge entries directly in a folder (folderID=""
	// for root), paged.
	ListByFolderPaged(ctx context.Context, kbID, folderID string, page, pageSize int) ([]*types.Knowledge, int64, error)

	// MoveKnowledge moves a single knowledge entry to a folder (folderID="" = root),
	// triggering debounced summary refresh for old+new folder ancestor chains.
	MoveKnowledge(ctx context.Context, knowledgeID, targetFolderID string) error
}
