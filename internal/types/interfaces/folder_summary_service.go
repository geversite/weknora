package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

// FolderSummaryTaskService extends FolderSummaryService with the asynq worker
// handler so it can be registered as the TypeFolderSummaryGeneration handler.
type FolderSummaryTaskService interface {
	FolderSummaryService
	// Handle processes a TypeFolderSummaryGeneration task.
	Handle(ctx context.Context, t *asynq.Task) error
}

// FolderSummaryService manages folder-level LLM summaries (M4).
type FolderSummaryService interface {
	// Get returns the summary for a folder (may be empty/none).
	Get(ctx context.Context, kbID, folderID string) (*types.FolderSummary, error)
	// Generate generates/regenerates the summary for a folder (async task).
	Generate(ctx context.Context, kbID, folderID string) error
	// Refresh forces regeneration even if the summary was manually edited.
	Refresh(ctx context.Context, kbID, folderID string) error
	// Edit stores a manually-authored summary (suppresses auto-regeneration).
	Edit(ctx context.Context, kbID, folderID string, req *types.FolderSummaryEditRequest) (*types.FolderSummary, error)
	// IsStale reports whether the summary inputs changed since generation.
	IsStale(ctx context.Context, kbID, folderID string) (bool, error)
	// ScheduleRefresh debounces a summary refresh for a folder: marks it pending
	// (coalescing concurrent changes) and enqueues a delayed task. forceRefresh
	// bypasses the is_manual_edit protection.
	ScheduleRefresh(ctx context.Context, folder *types.KnowledgeFolder, forceRefresh bool)
	// ScheduleRefreshForFolderAndAncestors triggers a debounced refresh for the
	// folder and its entire ancestor chain (parents' "subfolder guide" depends
	// on child themes).
	ScheduleRefreshForFolderAndAncestors(ctx context.Context, folder *types.KnowledgeFolder)
	// ListSummariesByKB returns all folder summaries for a KB (used by the wiki
	// governance system page).
	ListSummariesByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderSummary, error)
}
