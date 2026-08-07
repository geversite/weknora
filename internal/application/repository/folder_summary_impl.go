package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type folderSummaryRepo struct {
	db *gorm.DB
}

func NewFolderSummaryRepository(db *gorm.DB) interfaces.FolderSummaryRepository {
	return &folderSummaryRepo{db: db}
}

func (r *folderSummaryRepo) GetByFolder(ctx context.Context, folderID string) (*types.FolderSummary, error) {
	var summary types.FolderSummary
	if err := r.db.WithContext(ctx).Where("folder_id = ?", folderID).First(&summary).Error; err != nil {
		return nil, err
	}
	return &summary, nil
}

func (r *folderSummaryRepo) GetByFolderIDs(ctx context.Context, folderIDs []string) ([]*types.FolderSummary, error) {
	if len(folderIDs) == 0 {
		return nil, nil
	}
	var summaries []*types.FolderSummary
	err := r.db.WithContext(ctx).Where("folder_id IN ?", folderIDs).Find(&summaries).Error
	return summaries, err
}

func (r *folderSummaryRepo) Upsert(ctx context.Context, summary *types.FolderSummary) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "folder_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"content", "content_format", "is_manual_edit", "summary_version", "generated_at", "edited_at", "updated_at", "input_snapshot"}),
	}).Create(summary).Error
}

func (r *folderSummaryRepo) DeleteByFolder(ctx context.Context, folderID string) error {
	return r.db.WithContext(ctx).Where("folder_id = ?", folderID).Delete(&types.FolderSummary{}).Error
}

func (r *folderSummaryRepo) DeleteByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).Delete(&types.FolderSummary{}).Error
}

func (r *folderSummaryRepo) ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderSummary, error) {
	var summaries []*types.FolderSummary
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Find(&summaries).Error
	return summaries, err
}
