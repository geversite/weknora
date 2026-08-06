package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type knowledgeConflictRepo struct {
	db *gorm.DB
}

func NewKnowledgeConflictRepository(db *gorm.DB) interfaces.KnowledgeConflictRepository {
	return &knowledgeConflictRepo{db: db}
}

func (r *knowledgeConflictRepo) BatchCreate(ctx context.Context, conflicts []*types.KnowledgeConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	for _, c := range conflicts {
		if c == nil {
			continue
		}
		if c.ID == "" {
			c.ID = uuid.New().String()
		}
		if c.ConflictType == "" {
			c.ConflictType = types.ConflictTypeFactContradiction
		}
		if c.Status == "" {
			c.Status = types.ConflictStatusPending
		}
		if c.DetectedBy == "" {
			c.DetectedBy = types.ConflictDetectedByUpload
		}
	}
	return r.db.WithContext(ctx).CreateInBatches(conflicts, 100).Error
}

func (r *knowledgeConflictRepo) ListByKB(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.KnowledgeConflict, error) {
	q := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var conflicts []*types.KnowledgeConflict
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

func (r *knowledgeConflictRepo) GetByID(ctx context.Context, id string) (*types.KnowledgeConflict, error) {
	var conflict types.KnowledgeConflict
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&conflict).Error
	if err != nil {
		return nil, err
	}
	return &conflict, nil
}

func (r *knowledgeConflictRepo) Update(ctx context.Context, conflict *types.KnowledgeConflict) error {
	return r.db.WithContext(ctx).Save(conflict).Error
}

func (r *knowledgeConflictRepo) ListPendingByChunkIDs(ctx context.Context, chunkIDs []string) ([]*types.KnowledgeConflict, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	var conflicts []*types.KnowledgeConflict
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("chunk_id_a IN ? OR chunk_id_b IN ?", chunkIDs, chunkIDs).
		Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) ListPendingByKnowledgeID(ctx context.Context, knowledgeID string) ([]*types.KnowledgeConflict, error) {
	var conflicts []*types.KnowledgeConflict
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("knowledge_id_a = ? OR knowledge_id_b = ?", knowledgeID, knowledgeID).
		Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) HasPendingByChunkPair(ctx context.Context, chunkAID, chunkBID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("(chunk_id_a = ? AND chunk_id_b = ?) OR (chunk_id_a = ? AND chunk_id_b = ?)",
			chunkAID, chunkBID, chunkBID, chunkAID).
		Count(&count).Error
	return count > 0, err
}

func (r *knowledgeConflictRepo) DeleteByKnowledge(ctx context.Context, knowledgeID string) error {
	return r.db.WithContext(ctx).
		Where("knowledge_id_a = ? OR knowledge_id_b = ?", knowledgeID, knowledgeID).
		Delete(&types.KnowledgeConflict{}).Error
}

func (r *knowledgeConflictRepo) DeleteByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).
		Delete(&types.KnowledgeConflict{}).Error
}
